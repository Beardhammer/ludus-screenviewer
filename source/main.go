// Screen Viewer shows view-only snapshots of the VM screens in every range the
// caller can access. It captures them through the caller's own Proxmox token,
// so it can never see a console that the caller could not open themselves.
package main

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"time"

	ludus "gitlab.com/badsectorlabs/ludus/ludus-plugin-sdk"
	"gitlab.com/badsectorlabs/ludus/ludus-plugin-sdk/protocol"
)

// version is set at link time from the release tag (see scripts/build.sh).
var version = "dev"

const (
	defaultScreenWidth = 640
	captureTimeout     = 30 * time.Second
)

// allowedScreenWidths bounds the work one request can ask for.
var allowedScreenWidths = map[int]bool{640: true, 1280: true, 1920: true}

type captureFunc func(ctx context.Context, creds protocol.ProxmoxCredentials, vmid, maxWidth int) (*screenCapture, error)

type server struct {
	capture captureFunc
}

// The SDK derives both plugin.json and runtime metadata from this declaration.
func declaration() *ludus.Plugin {
	return newPlugin(captureVMScreen)
}

func newPlugin(capture captureFunc) *ludus.Plugin {
	s := &server{capture: capture}
	return &ludus.Plugin{
		ID: "ludus-screenviewer", Name: "Screen Viewer", Version: version,
		Description: "View-only snapshots of the VM screens in every range you can access.",
		Author:      "screenviewer",
		// Not range-scoped: this plugin spans all of the caller's ranges.
		UI: &ludus.UI{RangeScoped: false},
		Routes: []ludus.Route{
			{Name: "Screens", Method: http.MethodGet, Pattern: "/screens", Handler: s.screens},
			{Name: "Screen", Method: http.MethodGet, Pattern: "/screen", Handler: s.screen},
		},
	}
}

type vmView struct {
	ProxmoxID int    `json:"proxmoxID"`
	Name      string `json:"name"`
	IP        string `json:"ip"`
	OS        string `json:"os"`
	IsRouter  bool   `json:"isRouter"`
}

type rangeView struct {
	RangeID string   `json:"rangeID"`
	Name    string   `json:"name"`
	VMs     []vmView `json:"vms"`
	Error   string   `json:"error,omitempty"`
}

type screensResponse struct {
	Ranges []rangeView `json:"ranges"`
}

type screenResponse struct {
	ProxmoxID  int    `json:"proxmoxID"`
	Name       string `json:"name"`
	RangeID    string `json:"rangeID"`
	State      string `json:"state"`
	Image      string `json:"image,omitempty"`
	Width      int    `json:"width,omitempty"`
	Height     int    `json:"height,omitempty"`
	Partial    bool   `json:"partial,omitempty"`
	CapturedAt string `json:"capturedAt"`
}

func requireUser(w http.ResponseWriter, c *ludus.Call) bool {
	if c == nil || c.Auth.RecordID == "" || c.User == nil || c.User.RecordID == "" {
		ludus.Error(w, http.StatusForbidden, "A Ludus user is required")
		return false
	}
	return true
}

// screens lists every accessible range and its VMs. One unreadable range does
// not hide the others.
func (s *server) screens(w http.ResponseWriter, r *http.Request) {
	c := ludus.CallFrom(r)
	if !requireUser(w, c) {
		return
	}
	ranges, err := c.Host.AccessibleRanges()
	if err != nil {
		ludus.HostError(w, err)
		return
	}
	views := make([]rangeView, 0, len(ranges))
	for _, rng := range ranges {
		if rng.RecordID == "" || rng.Number <= 0 {
			continue // The ROOT pseudo-range has no VMs of its own.
		}
		view := rangeView{RangeID: rng.RangeID, Name: rng.Name, VMs: []vmView{}}
		vms, err := c.Host.RangeVMs(rng.Number)
		if err != nil {
			ludus.Logger().Warn("list range VMs", "range", rng.RangeID, "error", err)
			view.Error = "Could not load this range's machines."
		}
		for _, vm := range vms {
			if vm.ProxmoxID <= 0 {
				continue // Recorded but not deployed yet.
			}
			view.VMs = append(view.VMs, vmView{
				ProxmoxID: vm.ProxmoxID, Name: vm.Name, IP: vm.IP, OS: vm.OS, IsRouter: vm.IsRouter,
			})
		}
		sort.SliceStable(view.VMs, func(i, j int) bool { return view.VMs[i].Name < view.VMs[j].Name })
		views = append(views, view)
	}
	sort.SliceStable(views, func(i, j int) bool { return views[i].RangeID < views[j].RangeID })
	ludus.JSON(w, http.StatusOK, screensResponse{Ranges: views})
}

// screen captures one VM. The VM ID is client input: FindVM re-checks on the
// host that it belongs to a range this caller can access.
func (s *server) screen(w http.ResponseWriter, r *http.Request) {
	c := ludus.CallFrom(r)
	if !requireUser(w, c) {
		return
	}
	query := r.URL.Query()
	vmid, err := strconv.Atoi(query.Get("vmid"))
	if err != nil || vmid <= 0 {
		ludus.Error(w, http.StatusBadRequest, "A valid VM ID is required")
		return
	}
	width := defaultScreenWidth
	if raw := query.Get("width"); raw != "" {
		width, err = strconv.Atoi(raw)
		if err != nil || !allowedScreenWidths[width] {
			ludus.Error(w, http.StatusBadRequest, "Unsupported screen width")
			return
		}
	}
	vm, rng, err := c.Host.FindVM(vmid)
	if err != nil {
		ludus.HostError(w, err)
		return
	}
	creds, err := c.Host.ProxmoxCredentials()
	if err != nil {
		ludus.HostError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), captureTimeout)
	defer cancel()
	shot, err := s.capture(ctx, creds, vmid, width)
	if err != nil {
		ludus.Logger().Warn("capture screen", "vmid", vmid, "error", err)
		switch {
		case errors.Is(err, errConsoleDenied):
			ludus.Error(w, http.StatusForbidden, "Your Proxmox account cannot open this VM's console")
		case errors.Is(err, context.DeadlineExceeded):
			ludus.Error(w, http.StatusGatewayTimeout, "Timed out capturing this screen")
		default:
			ludus.Error(w, http.StatusBadGateway, "Could not capture this screen")
		}
		return
	}
	ludus.JSON(w, http.StatusOK, screenResponse{
		ProxmoxID: vmid, Name: vm.Name, RangeID: rng.RangeID, State: shot.State,
		Image: shot.dataURI(), Width: shot.Width, Height: shot.Height, Partial: shot.Partial,
		CapturedAt: time.Now().UTC().Format(time.RFC3339),
	})
}

func main() { ludus.Serve(declaration()) }
