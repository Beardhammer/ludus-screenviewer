package main

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"gitlab.com/badsectorlabs/ludus/ludus-plugin-sdk/protocol"
)

// errConsoleDenied means Proxmox refused the caller's token, or does not show
// it this VM at all.
var errConsoleDenied = errors.New("proxmox denied console access")

// screenCapture is one VM's state and, when it is running, a JPEG of its screen.
type screenCapture struct {
	State         string
	JPEG          []byte
	Width, Height int
	Partial       bool
}

func (s *screenCapture) dataURI() string {
	if len(s.JPEG) == 0 {
		return ""
	}
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(s.JPEG)
}

// vmLocks serializes captures of one VM. Each vncproxy call replaces the VM's
// temporary VNC password, so overlapping captures would break each other.
var vmLocks sync.Map // int -> *sync.Mutex

func lockVM(vmid int) func() {
	value, _ := vmLocks.LoadOrStore(vmid, &sync.Mutex{})
	mu := value.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// captureVMScreen tries each configured Proxmox endpoint until one answers.
func captureVMScreen(ctx context.Context, creds protocol.ProxmoxCredentials, vmid, maxWidth int) (*screenCapture, error) {
	if len(creds.Endpoints) == 0 {
		return nil, errors.New("no Proxmox endpoints configured")
	}
	defer lockVM(vmid)()
	var lastErr error
	for _, endpoint := range creds.Endpoints {
		shot, err := newPVEClient(creds, endpoint).capture(ctx, vmid, maxWidth)
		if err == nil {
			return shot, nil
		}
		if errors.Is(err, errConsoleDenied) || ctx.Err() != nil {
			return nil, err
		}
		lastErr = err
	}
	return nil, lastErr
}

type pveClient struct {
	base      string
	auth      string
	tlsConfig *tls.Config
	http      *http.Client
}

func newPVEClient(creds protocol.ProxmoxCredentials, endpoint string) *pveClient {
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: creds.InsecureTLS}
	base := strings.TrimRight(endpoint, "/")
	base = strings.TrimSuffix(base, "/api2/json")
	return &pveClient{
		base:      base,
		auth:      "PVEAPIToken=" + creds.TokenID + "=" + creds.TokenSecret,
		tlsConfig: tlsConfig,
		http: &http.Client{Transport: &http.Transport{
			TLSClientConfig:   tlsConfig,
			DisableKeepAlives: true, // A client lives for one capture.
		}},
	}
}

func (p *pveClient) call(ctx context.Context, method, path string, form url.Values, out any) error {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, p.base+path, body)
	if err != nil {
		return fmt.Errorf("build Proxmox request: %w", err)
	}
	req.Header.Set("Authorization", p.auth)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := p.http.Do(req)
	if err != nil {
		return fmt.Errorf("Proxmox %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return fmt.Errorf("read Proxmox response: %w", err)
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("%w: %s returned HTTP %d", errConsoleDenied, path, resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("Proxmox %s %s returned HTTP %d", method, path, resp.StatusCode)
	}
	envelope := struct {
		Data any `json:"data"`
	}{Data: out}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("decode Proxmox response: %w", err)
	}
	return nil
}

type pveResource struct {
	VMID   int    `json:"vmid"`
	Node   string `json:"node"`
	Status string `json:"status"`
	Type   string `json:"type"`
}

// findVM reads the VM's node and live status. Ludus's recorded inventory can
// lag behind, and a VM can live on any node of a cluster.
func (p *pveClient) findVM(ctx context.Context, vmid int) (pveResource, error) {
	var resources []pveResource
	if err := p.call(ctx, http.MethodGet, "/api2/json/cluster/resources?type=vm", nil, &resources); err != nil {
		return pveResource{}, err
	}
	for _, resource := range resources {
		if resource.VMID == vmid {
			return resource, nil
		}
	}
	// Proxmox omits VMs the token has no rights on.
	return pveResource{}, fmt.Errorf("%w: VM %d is not visible to this token", errConsoleDenied, vmid)
}

type vncProxyReply struct {
	Port     json.Number `json:"port"` // Proxmox may send a number or a string.
	Ticket   string      `json:"ticket"`
	Password string      `json:"password"`
}

func (p *pveClient) capture(ctx context.Context, vmid, maxWidth int) (*screenCapture, error) {
	vm, err := p.findVM(ctx, vmid)
	if err != nil {
		return nil, err
	}
	if vm.Type != "qemu" {
		return nil, fmt.Errorf("VM %d has unsupported type %q", vmid, vm.Type)
	}
	if vm.Status != "running" {
		return &screenCapture{State: vm.Status}, nil
	}

	vmPath := "/api2/json/nodes/" + url.PathEscape(vm.Node) + "/qemu/" + strconv.Itoa(vmid)
	var proxy vncProxyReply
	form := url.Values{"websocket": {"1"}, "generate-password": {"1"}}
	if err := p.call(ctx, http.MethodPost, vmPath+"/vncproxy", form, &proxy); err != nil {
		return nil, err
	}
	if proxy.Port == "" || proxy.Ticket == "" {
		return nil, errors.New("Proxmox vncproxy reply is missing its port or ticket")
	}
	password := proxy.Password
	if password == "" {
		password = proxy.Ticket
	}

	query := url.Values{"port": {proxy.Port.String()}, "vncticket": {proxy.Ticket}}
	conn, err := dialWebsocket(ctx, p, vmPath+"/vncwebsocket", query)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	screen, partial, err := readScreen(ctx, conn, password)
	if err != nil {
		return nil, err
	}
	jpegBytes, width, height, err := encodeScreen(screen, maxWidth)
	if err != nil {
		return nil, err
	}
	return &screenCapture{State: "running", JPEG: jpegBytes, Width: width, Height: height, Partial: partial}, nil
}
