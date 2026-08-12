package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/jeremyhahn/go-netmon/internal/webui/storage"
)

// A filter is worth nothing until the poller has it.
//
// An operator can narrow a MIB to the objects worth collecting: the web UI
// stores the patterns, shows them, and the cost estimator prices the result. The
// poller was told only the module names, so it fetched every pollable object of
// every module and the narrowing never happened. The whole feature was inert for
// the one thing it exists to do, and nothing said so -- the UI reported the
// filter as set, and the poll count did not move.
//
// This was found by setting a filter on a live stack and watching core-01
// keep polling 15,952 OIDs.

// TestAFilterReachesThePoller is the regression.
func TestAFilterReachesThePoller(t *testing.T) {
	s := newTestServer(t)
	poller := &recordingPoller{}
	s.pollerClient = poller

	ctx := context.Background()
	device := storeDevice(t, s, "core-01", "192.0.2.1")

	mib := &storage.MIB{Name: "CISCO-PROCESS-MIB", FilePath: "/mibs/CISCO-PROCESS-MIB.my"}
	if err := s.storage.CreateMIB(ctx, mib); err != nil {
		t.Fatalf("CreateMIB: %v", err)
	}
	if err := s.storage.AssignMIBToDevice(ctx, device, mib.ID); err != nil {
		t.Fatalf("AssignMIBToDevice: %v", err)
	}

	// Narrow the module to the CPU totals table, which is what an operator does
	// to stop a router reporting 761 per-process rows it never reads.
	const cpuTotals = `^1\.3\.6\.1\.4\.1\.9\.9\.109\.1\.1\.1`

	// Encoded rather than pasted: the pattern is full of backslashes, and a
	// hand-built body would be testing my JSON quoting rather than the push.
	body, err := json.Marshal(map[string]any{
		"enabled":      true,
		"include_oids": []string{cpuTotals},
		"max_oids":     40,
	})
	if err != nil {
		t.Fatalf("encode the assignment: %v", err)
	}

	rec := do(t, s, http.MethodPut,
		"/api/devices/"+itoa(device)+"/mibs/"+itoa(mib.ID)+"/assignment", string(body))
	if rec.Code != http.StatusOK {
		t.Fatalf("setting the filter answered %d: %s", rec.Code, rec.Body.String())
	}

	// The push that carries it is the last one the poller received.
	poller.mu.Lock()
	pushes := len(poller.filters)
	sent := poller.filters[pushes-1]
	modules := poller.modules[pushes-1]
	poller.mu.Unlock()

	if len(modules) != 1 || modules[0] != "CISCO-PROCESS-MIB" {
		t.Fatalf("modules pushed = %v, want [CISCO-PROCESS-MIB]", modules)
	}

	filter, carried := sent["CISCO-PROCESS-MIB"]
	if !carried {
		t.Fatal("the poller was told the module and not the filter; it will poll the module in full")
	}
	if len(filter.IncludeOIDs) != 1 || filter.IncludeOIDs[0] != cpuTotals {
		t.Errorf("IncludeOIDs = %v, want the pattern that was set", filter.IncludeOIDs)
	}
	if filter.MaxOIDs != 40 {
		t.Errorf("MaxOIDs = %d, want 40", filter.MaxOIDs)
	}
}

// TestAnUnfilteredModuleCarriesNoFilter keeps the push honest in the other
// direction: a module nobody narrowed must arrive with nothing attached, so the
// poller reads it in full rather than through an empty filter.
func TestAnUnfilteredModuleCarriesNoFilter(t *testing.T) {
	s := newTestServer(t)
	poller := &recordingPoller{}
	s.pollerClient = poller

	ctx := context.Background()
	device := storeDevice(t, s, "bastion", "192.0.2.14")

	mib := &storage.MIB{Name: "IF-MIB", FilePath: "/mibs/IF-MIB.txt"}
	if err := s.storage.CreateMIB(ctx, mib); err != nil {
		t.Fatalf("CreateMIB: %v", err)
	}

	rec := do(t, s, http.MethodPost, "/api/devices/"+itoa(device)+"/mibs",
		`{"mib_id":`+itoa(mib.ID)+`}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("assign answered %d: %s", rec.Code, rec.Body.String())
	}

	poller.mu.Lock()
	sent := poller.filters[len(poller.filters)-1]
	modules := poller.modules[len(poller.modules)-1]
	poller.mu.Unlock()

	if len(modules) != 1 || modules[0] != "IF-MIB" {
		t.Fatalf("modules pushed = %v, want [IF-MIB]", modules)
	}
	if len(sent) != 0 {
		t.Errorf("an unfiltered module arrived with filters %v", sent)
	}
}

// TestDisablingAModuleWithdrawsItEntirely covers the other half of the contract:
// a disabled assignment is not a filter, it is an absence, and the poller stops
// fetching the module because it is no longer in the list.
func TestDisablingAModuleWithdrawsItEntirely(t *testing.T) {
	s := newTestServer(t)
	poller := &recordingPoller{}
	s.pollerClient = poller

	ctx := context.Background()
	device := storeDevice(t, s, "core-01", "192.0.2.1")

	for _, name := range []string{"IF-MIB", "CISCO-PROCESS-MIB"} {
		m := &storage.MIB{Name: name, FilePath: "/mibs/" + name}
		if err := s.storage.CreateMIB(ctx, m); err != nil {
			t.Fatalf("CreateMIB %s: %v", name, err)
		}
		if err := s.storage.AssignMIBToDevice(ctx, device, m.ID); err != nil {
			t.Fatalf("assign %s: %v", name, err)
		}
		if name == "CISCO-PROCESS-MIB" {
			rec := do(t, s, http.MethodPut,
				"/api/devices/"+itoa(device)+"/mibs/"+itoa(m.ID)+"/assignment",
				`{"enabled":false}`)
			if rec.Code != http.StatusOK {
				t.Fatalf("disabling answered %d: %s", rec.Code, rec.Body.String())
			}
		}
	}

	poller.mu.Lock()
	modules := poller.modules[len(poller.modules)-1]
	poller.mu.Unlock()

	if len(modules) != 1 || modules[0] != "IF-MIB" {
		t.Errorf("modules pushed = %v, want only the enabled one", modules)
	}
}
