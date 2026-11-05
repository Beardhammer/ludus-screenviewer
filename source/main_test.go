package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	ludus "gitlab.com/badsectorlabs/ludus/ludus-plugin-sdk"
	"gitlab.com/badsectorlabs/ludus/ludus-plugin-sdk/ludustest"
	"gitlab.com/badsectorlabs/ludus/ludus-plugin-sdk/protocol"
)

func caller() []ludustest.RequestOption {
	return []ludustest.RequestOption{
		ludustest.WithAuth(protocol.Principal{RecordID: "authenticated-user", UserID: "TEST"}),
		ludustest.WithUser(&protocol.User{RecordID: "effective-user", UserID: "TEST"}),
	}
}

func noCapture(t *testing.T) captureFunc {
	return func(context.Context, protocol.ProxmoxCredentials, int, int) (*screenCapture, error) {
		t.Error("capture must not run")
		return nil, errors.New("unexpected capture")
	}
}

func TestScreensListsEveryAccessibleRange(t *testing.T) {
	host := &ludustest.Host{
		AccessibleRangesFunc: func(protocol.CallArgs) ([]protocol.Range, error) {
			return []protocol.Range{
				{RecordID: "b", RangeID: "SHARED", Number: 7},
				{RangeID: "ROOT"},
				{RecordID: "a", RangeID: "MINE", Number: 3},
				{RecordID: "c", RangeID: "BROKEN", Number: 9},
			}, nil
		},
		RangeVMsFunc: func(args protocol.RangeArgs) ([]protocol.VM, error) {
			switch args.Number {
			case 3:
				return []protocol.VM{{Name: "z-box", ProxmoxID: 301}, {Name: "a-box", ProxmoxID: 302}, {Name: "undeployed"}}, nil
			case 7:
				return []protocol.VM{{Name: "shared-box", ProxmoxID: 701, IsRouter: true}}, nil
			}
			return nil, protocol.HostErrorf(protocol.CodeForbidden, "no")
		},
	}
	h := ludustest.Start(t, newPlugin(noCapture(t)), host, ludus.Env{})
	options := append(caller(), ludustest.WithQuery(url.Values{"rangeID": {"OTHER"}, "userID": {"OTHER"}}))
	response := h.Do(http.MethodGet, "/screens", options...)
	var body screensResponse
	if err := json.Unmarshal(response.Body, &body); err != nil {
		t.Fatal(err)
	}
	if response.Status != http.StatusOK || len(body.Ranges) != 3 {
		t.Fatalf("status=%d body=%s", response.Status, response.Body)
	}
	broken, mine, shared := body.Ranges[0], body.Ranges[1], body.Ranges[2]
	if broken.RangeID != "BROKEN" || broken.Error == "" || broken.VMs == nil || len(broken.VMs) != 0 {
		t.Errorf("broken range: %+v", broken)
	}
	if mine.RangeID != "MINE" || len(mine.VMs) != 2 || mine.VMs[0].Name != "a-box" || mine.VMs[1].ProxmoxID != 301 {
		t.Errorf("own range: %+v", mine)
	}
	if shared.RangeID != "SHARED" || len(shared.VMs) != 1 || !shared.VMs[0].IsRouter {
		t.Errorf("shared range: %+v", shared)
	}
}

func TestScreensEmpty(t *testing.T) {
	host := &ludustest.Host{AccessibleRangesFunc: func(protocol.CallArgs) ([]protocol.Range, error) { return nil, nil }}
	h := ludustest.Start(t, newPlugin(noCapture(t)), host, ludus.Env{})
	response := h.Do(http.MethodGet, "/screens", caller()...)
	if response.Status != http.StatusOK || !strings.Contains(string(response.Body), `"ranges":[]`) {
		t.Fatalf("status=%d body=%s", response.Status, response.Body)
	}
}

func TestRoutesRequireUser(t *testing.T) {
	for _, tc := range []struct {
		name   string
		option ludustest.RequestOption
	}{
		{"no authentication", ludustest.WithAuth(protocol.Principal{})},
		{"no effective user", ludustest.WithUser(nil)},
	} {
		for _, route := range []string{"/screens", "/screen"} {
			t.Run(tc.name+route, func(t *testing.T) {
				h := ludustest.Start(t, newPlugin(noCapture(t)), &ludustest.Host{}, ludus.Env{})
				options := append(caller(), tc.option, ludustest.WithQuery(url.Values{"vmid": {"1"}}))
				if response := h.Do(http.MethodGet, route, options...); response.Status != http.StatusForbidden {
					t.Fatalf("status=%d body=%s", response.Status, response.Body)
				}
			})
		}
	}
	plugin := &server{capture: noCapture(t)}
	w := httptest.NewRecorder()
	plugin.screen(w, httptest.NewRequest(http.MethodGet, "/screen?vmid=1", nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("request outside SDK accepted: %d", w.Code)
	}
}

func TestScreenRejectsInvalidInput(t *testing.T) {
	for _, query := range []url.Values{
		{},
		{"vmid": {"abc"}},
		{"vmid": {"0"}},
		{"vmid": {"-4"}},
		{"vmid": {"101"}, "width": {"99999"}},
		{"vmid": {"101"}, "width": {"wide"}},
	} {
		host := &ludustest.Host{FindVMFunc: func(protocol.FindVMArgs) (protocol.VM, protocol.Range, error) {
			t.Error("invalid input reached the host")
			return protocol.VM{}, protocol.Range{}, nil
		}}
		h := ludustest.Start(t, newPlugin(noCapture(t)), host, ludus.Env{})
		response := h.Do(http.MethodGet, "/screen", append(caller(), ludustest.WithQuery(query))...)
		if response.Status != http.StatusBadRequest {
			t.Errorf("query %v: status=%d body=%s", query, response.Status, response.Body)
		}
	}
}

func TestScreenHostDenialsSkipCapture(t *testing.T) {
	for _, tc := range []struct {
		code   protocol.ErrorCode
		status int
	}{
		{protocol.CodeForbidden, 403}, {protocol.CodeNotFound, 404},
		{protocol.CodeInvalid, 400}, {protocol.CodeExpired, 503}, {protocol.CodeUnavailable, 503},
	} {
		t.Run(string(tc.code), func(t *testing.T) {
			host := &ludustest.Host{FindVMFunc: func(protocol.FindVMArgs) (protocol.VM, protocol.Range, error) {
				return protocol.VM{}, protocol.Range{}, protocol.HostErrorf(tc.code, "denied")
			}}
			h := ludustest.Start(t, newPlugin(noCapture(t)), host, ludus.Env{})
			response := h.Do(http.MethodGet, "/screen", append(caller(), ludustest.WithQuery(url.Values{"vmid": {"101"}}))...)
			if response.Status != tc.status {
				t.Fatalf("status=%d body=%s", response.Status, response.Body)
			}
		})
	}
}

func screenHost() *ludustest.Host {
	return &ludustest.Host{
		FindVMFunc: func(args protocol.FindVMArgs) (protocol.VM, protocol.Range, error) {
			return protocol.VM{Name: "box", ProxmoxID: args.ProxmoxID}, protocol.Range{RecordID: "r", RangeID: "MINE", Number: 3}, nil
		},
		ProxmoxCredentialsFunc: func(protocol.CallArgs) (protocol.ProxmoxCredentials, error) {
			return protocol.ProxmoxCredentials{Endpoints: []string{"https://pve:8006"}, TokenID: "u@pve!t", TokenSecret: "top-secret"}, nil
		},
	}
}

func TestScreenReturnsCapture(t *testing.T) {
	capture := func(_ context.Context, creds protocol.ProxmoxCredentials, vmid, width int) (*screenCapture, error) {
		if creds.TokenID != "u@pve!t" || vmid != 101 || width != 1280 {
			t.Errorf("capture scope: %q %d %d", creds.TokenID, vmid, width)
		}
		return &screenCapture{State: "running", JPEG: []byte("jpeg"), Width: 1280, Height: 720}, nil
	}
	h := ludustest.Start(t, newPlugin(capture), screenHost(), ludus.Env{})
	response := h.Do(http.MethodGet, "/screen", append(caller(), ludustest.WithQuery(url.Values{"vmid": {"101"}, "width": {"1280"}}))...)
	var body screenResponse
	if err := json.Unmarshal(response.Body, &body); err != nil {
		t.Fatal(err)
	}
	if response.Status != http.StatusOK || body.RangeID != "MINE" || body.Name != "box" || body.State != "running" ||
		body.Image != "data:image/jpeg;base64,anBlZw==" || body.Width != 1280 {
		t.Fatalf("status=%d body=%s", response.Status, response.Body)
	}
	if strings.Contains(string(response.Body), "top-secret") {
		t.Fatal("response leaked the Proxmox token")
	}
}

func TestScreenStoppedVMHasNoImage(t *testing.T) {
	capture := func(context.Context, protocol.ProxmoxCredentials, int, int) (*screenCapture, error) {
		return &screenCapture{State: "stopped"}, nil
	}
	h := ludustest.Start(t, newPlugin(capture), screenHost(), ludus.Env{})
	response := h.Do(http.MethodGet, "/screen", append(caller(), ludustest.WithQuery(url.Values{"vmid": {"101"}}))...)
	var body screenResponse
	if err := json.Unmarshal(response.Body, &body); err != nil {
		t.Fatal(err)
	}
	if response.Status != http.StatusOK || body.State != "stopped" || body.Image != "" {
		t.Fatalf("status=%d body=%s", response.Status, response.Body)
	}
}

func TestScreenCaptureFailures(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
	}{
		{errConsoleDenied, http.StatusForbidden},
		{context.DeadlineExceeded, http.StatusGatewayTimeout},
		{errors.New("detail with top-secret"), http.StatusBadGateway},
	} {
		capture := func(context.Context, protocol.ProxmoxCredentials, int, int) (*screenCapture, error) { return nil, tc.err }
		h := ludustest.Start(t, newPlugin(capture), screenHost(), ludus.Env{})
		response := h.Do(http.MethodGet, "/screen", append(caller(), ludustest.WithQuery(url.Values{"vmid": {"101"}}))...)
		if response.Status != tc.status || strings.Contains(string(response.Body), "top-secret") {
			t.Errorf("err %v: status=%d body=%s", tc.err, response.Status, response.Body)
		}
	}
}
