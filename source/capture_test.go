package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"gitlab.com/badsectorlabs/ludus/ludus-plugin-sdk/protocol"
)

// fakeRFBServer plays a QEMU VNC server. sendRows limits how many rows of the
// screen it actually sends.
func fakeRFBServer(conn io.ReadWriter, password string, width, height, sendRows int) error {
	read := func(n int) ([]byte, error) {
		buffer := make([]byte, n)
		_, err := io.ReadFull(conn, buffer)
		return buffer, err
	}
	if _, err := conn.Write([]byte("RFB 003.008\n")); err != nil {
		return err
	}
	if _, err := read(12); err != nil {
		return err
	}
	if _, err := conn.Write([]byte{1, rfbSecurityVNC}); err != nil {
		return err
	}
	choice, err := read(1)
	if err != nil {
		return err
	}
	if choice[0] != rfbSecurityVNC {
		return fmt.Errorf("security choice %d", choice[0])
	}
	challenge := []byte("0123456789abcdef")
	if _, err := conn.Write(challenge); err != nil {
		return err
	}
	answer, err := read(16)
	if err != nil {
		return err
	}
	expected, err := vncResponse(password, challenge)
	if err != nil {
		return err
	}
	if !bytes.Equal(answer, expected) {
		if _, err := conn.Write([]byte{0, 0, 0, 1, 0, 0, 0, 0}); err != nil {
			return err
		}
		return errors.New("wrong VNC password")
	}
	if _, err := conn.Write([]byte{0, 0, 0, 0}); err != nil {
		return err
	}
	shared, err := read(1)
	if err != nil {
		return err
	}
	if shared[0] != 1 {
		return errors.New("client did not ask for a shared session")
	}
	serverInit := binary.BigEndian.AppendUint16(nil, uint16(width))
	serverInit = binary.BigEndian.AppendUint16(serverInit, uint16(height))
	serverInit = append(serverInit, make([]byte, 16)...)
	serverInit = binary.BigEndian.AppendUint32(serverInit, 3)
	serverInit = append(serverInit, "qem"...)
	if _, err := conn.Write(serverInit); err != nil {
		return err
	}
	// SetPixelFormat (20), SetEncodings with one entry (8), update request (10).
	request, err := read(38)
	if err != nil {
		return err
	}
	if request[0] != 0 || request[20] != 2 || request[28] != 3 {
		return fmt.Errorf("unexpected client messages %v", request)
	}
	if _, err := conn.Write([]byte{2}); err != nil { // A bell must be ignored.
		return err
	}
	update := []byte{0, 0, 0, 1}
	update = binary.BigEndian.AppendUint16(update, 0)
	update = binary.BigEndian.AppendUint16(update, 0)
	update = binary.BigEndian.AppendUint16(update, uint16(width))
	update = binary.BigEndian.AppendUint16(update, uint16(sendRows))
	update = append(update, 0, 0, 0, 0)
	for i := 0; i < width*sendRows; i++ {
		update = append(update, 10, 20, 30, 0) // B, G, R, X
	}
	_, err = conn.Write(update)
	return err
}

func TestReadScreen(t *testing.T) {
	client, peer := net.Pipe()
	defer client.Close()
	defer peer.Close()
	done := make(chan error, 1)
	go func() { done <- fakeRFBServer(peer, "secret12", 4, 3, 3) }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	screen, partial, err := readScreen(ctx, client, "secret12")
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if partial || screen.Bounds().Dx() != 4 || screen.Bounds().Dy() != 3 {
		t.Fatalf("partial=%v bounds=%v", partial, screen.Bounds())
	}
	if got := screen.RGBAAt(3, 2); got.R != 30 || got.G != 20 || got.B != 10 || got.A != 255 {
		t.Fatalf("pixel = %+v", got)
	}
}

func TestReadScreenReturnsPartialUpdate(t *testing.T) {
	client, peer := net.Pipe()
	defer client.Close()
	defer peer.Close()
	go func() { _ = fakeRFBServer(peer, "secret12", 4, 3, 1) }()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	screen, partial, err := readScreen(ctx, client, "secret12")
	if err != nil {
		t.Fatal(err)
	}
	if !partial || screen.RGBAAt(0, 0).R != 30 || screen.RGBAAt(0, 2).A != 0 {
		t.Fatalf("partial=%v first=%+v last=%+v", partial, screen.RGBAAt(0, 0), screen.RGBAAt(0, 2))
	}
}

func TestReadScreenWrongPassword(t *testing.T) {
	client, peer := net.Pipe()
	defer client.Close()
	defer peer.Close()
	go func() { _ = fakeRFBServer(peer, "secret12", 4, 3, 3) }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readScreen(ctx, client, "other"); err == nil {
		t.Fatal("wrong password accepted")
	}
}

func TestEncodeScreenScalesDown(t *testing.T) {
	screen := image.NewRGBA(image.Rect(0, 0, 1280, 720))
	for i := range screen.Pix {
		screen.Pix[i] = 200
	}
	data, width, height, err := encodeScreen(screen, 640)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if width != 640 || height != 360 || decoded.Bounds().Dx() != 640 || decoded.Bounds().Dy() != 360 {
		t.Fatalf("size %dx%d, decoded %v", width, height, decoded.Bounds())
	}
	_, width, height, err = encodeScreen(image.NewRGBA(image.Rect(0, 0, 320, 200)), 640)
	if err != nil || width != 320 || height != 200 {
		t.Fatalf("small screen scaled: %dx%d, %v", width, height, err)
	}
}

// serverWebsocket is the server side of a websocket: it reads masked frames
// and writes unmasked ones.
type serverWebsocket struct {
	conn    net.Conn
	reader  *bufio.Reader
	pending []byte
}

func (s *serverWebsocket) Read(p []byte) (int, error) {
	for len(s.pending) == 0 {
		var header [2]byte
		if _, err := io.ReadFull(s.reader, header[:]); err != nil {
			return 0, err
		}
		length := int(header[1] & 0x7f)
		if header[1]&0x80 == 0 || length >= 126 {
			return 0, errors.New("client frame must be masked and short")
		}
		var mask [4]byte
		if _, err := io.ReadFull(s.reader, mask[:]); err != nil {
			return 0, err
		}
		payload := make([]byte, length)
		if _, err := io.ReadFull(s.reader, payload); err != nil {
			return 0, err
		}
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
		if header[0]&0x0f == 0x8 {
			return 0, io.EOF
		}
		s.pending = payload
	}
	n := copy(p, s.pending)
	s.pending = s.pending[n:]
	return n, nil
}

func (s *serverWebsocket) Write(p []byte) (int, error) {
	frame := []byte{0x82}
	switch {
	case len(p) <= 125:
		frame = append(frame, byte(len(p)))
	case len(p) <= 0xffff:
		frame = append(frame, 126)
		frame = binary.BigEndian.AppendUint16(frame, uint16(len(p)))
	default:
		frame = append(frame, 127)
		frame = binary.BigEndian.AppendUint64(frame, uint64(len(p)))
	}
	if _, err := s.conn.Write(append(frame, p...)); err != nil {
		return 0, err
	}
	return len(p), nil
}

type fakePVE struct {
	*httptest.Server
	status  string
	serving chan error
}

const testTicket = "PVEVNC:abc+/=def"

func newFakePVE(t *testing.T, status string) *fakePVE {
	t.Helper()
	fake := &fakePVE{status: status, serving: make(chan error, 1)}
	const auth = "PVEAPIToken=u@pve!t=secret"
	mux := http.NewServeMux()
	mux.HandleFunc("/api2/json/cluster/resources", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != auth {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{
			{"vmid": 999, "node": "other", "status": "running", "type": "qemu"},
			{"vmid": 104, "node": "pve1", "status": fake.status, "type": "qemu"},
		}})
	})
	mux.HandleFunc("/api2/json/nodes/pve1/qemu/104/vncproxy", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.FormValue("websocket") != "1" || r.FormValue("generate-password") != "1" || r.Header.Get("Authorization") != auth {
			t.Errorf("vncproxy request: %s %v", r.Method, r.Form)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"port": "5901", "ticket": testTicket, "password": "secret12"}})
	})
	mux.HandleFunc("/api2/json/nodes/pve1/qemu/104/vncwebsocket", func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		if query.Get("port") != "5901" || query.Get("vncticket") != testTicket || r.Header.Get("Authorization") != auth ||
			r.Header.Get("Sec-WebSocket-Protocol") != "binary" {
			t.Errorf("websocket request: %v %v", query, r.Header)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		conn, buffered, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		fmt.Fprintf(conn, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"+
			"Sec-WebSocket-Protocol: binary\r\nSec-WebSocket-Accept: %s\r\n\r\n", websocketAccept(r.Header.Get("Sec-WebSocket-Key")))
		socket := &serverWebsocket{conn: conn, reader: buffered.Reader}
		err = fakeRFBServer(socket, "secret12", 800, 600, 600)
		if err == nil {
			// Hold the socket until the client has read everything and closed.
			_, _ = io.Copy(io.Discard, socket)
		}
		fake.serving <- err
	})
	fake.Server = httptest.NewTLSServer(mux)
	t.Cleanup(fake.Close)
	return fake
}

func testCreds(endpoints ...string) protocol.ProxmoxCredentials {
	return protocol.ProxmoxCredentials{Endpoints: endpoints, TokenID: "u@pve!t", TokenSecret: "secret", InsecureTLS: true}
}

func TestCaptureVMScreenEndToEnd(t *testing.T) {
	fake := newFakePVE(t, "running")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	// The first endpoint is unreachable, so this also covers failover.
	shot, err := captureVMScreen(ctx, testCreds("https://127.0.0.1:1", fake.URL+"/"), 104, 640)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-fake.serving; err != nil {
		t.Fatal(err)
	}
	if shot.State != "running" || shot.Partial || shot.Width != 640 || shot.Height != 480 {
		t.Fatalf("capture: %+v", shot)
	}
	if _, err := jpeg.Decode(bytes.NewReader(shot.JPEG)); err != nil {
		t.Fatal(err)
	}
}

func TestCaptureVMScreenStoppedSkipsConsole(t *testing.T) {
	fake := newFakePVE(t, "stopped")
	shot, err := captureVMScreen(context.Background(), testCreds(fake.URL), 104, 640)
	if err != nil || shot.State != "stopped" || shot.dataURI() != "" {
		t.Fatalf("capture: %+v, %v", shot, err)
	}
}

func TestCaptureVMScreenDenied(t *testing.T) {
	fake := newFakePVE(t, "running")
	// Proxmox hides VMs the token has no rights on.
	if _, err := captureVMScreen(context.Background(), testCreds(fake.URL), 105, 640); !errors.Is(err, errConsoleDenied) {
		t.Fatalf("hidden VM: %v", err)
	}
	wrong := testCreds(fake.URL)
	wrong.TokenSecret = "wrong"
	if _, err := captureVMScreen(context.Background(), wrong, 104, 640); !errors.Is(err, errConsoleDenied) {
		t.Fatalf("wrong token: %v", err)
	}
}
