package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Asking for a page instead of the whole fleet.
//
// The overview displays one page at a time but the endpoint serialized every
// configured device, measured at 378 bytes each: about 3MB per refresh per open
// browser at ten thousand devices, every thirty seconds. The rows a reader
// cannot see are the overwhelming majority of that.
//
// The counts behind the summary chips still have to cover the whole fleet --
// "12 unreachable" is a statement about the fleet, not about page one -- so they
// are computed server-side and travel as numbers rather than as rows.

// fleetPage requests one page and decodes the response.
func fleetPage(t *testing.T, s *Server, query string) FleetStatusResponse {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, "/api/devices/status?"+query, nil)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}

	var fleet FleetStatusResponse
	if err := json.Unmarshal(w.Body.Bytes(), &fleet); err != nil {
		t.Fatalf("decode: %v (%s)", err, w.Body.String())
	}
	return fleet
}

// storeFleet writes n devices and points the server at a poller that knows them.
func storeFleet(t *testing.T, n int) *Server {
	t.Helper()

	s, poller, devices := fleetOf(t, n, 0)
	s.pollerClient = poller
	for i := range devices {
		device := devices[i]
		if err := s.storage.CreateDevice(context.Background(), &device); err != nil {
			t.Fatalf("create device: %v", err)
		}
	}
	return s
}

// TestFleetStatusReturnsOnlyTheRequestedPage is the requirement.
func TestFleetStatusReturnsOnlyTheRequestedPage(t *testing.T) {
	t.Parallel()

	s := storeFleet(t, 500)

	fleet := fleetPage(t, s, "limit=25&offset=0")
	if got := len(fleet.Devices); got != 25 {
		t.Fatalf("returned %d rows, want the 25 asked for", got)
	}
	if fleet.Total != 500 {
		t.Errorf("total = %d, want 500: the pager cannot size itself without it", fleet.Total)
	}
	if fleet.Devices[0].Name != "device-00000" {
		t.Errorf("first row = %q, want the start of the fleet", fleet.Devices[0].Name)
	}

	// The next page continues where the last one stopped, with no overlap.
	next := fleetPage(t, s, "limit=25&offset=25")
	if next.Devices[0].Name != "device-00025" {
		t.Errorf("second page starts at %q, want device-00025", next.Devices[0].Name)
	}
}

// TestFleetSummaryCoversTheWholeFleet is what must not be lost by paging: the
// chips count every device, not the ones on screen.
func TestFleetSummaryCoversTheWholeFleet(t *testing.T) {
	t.Parallel()

	s := storeFleet(t, 500)

	fleet := fleetPage(t, s, "limit=10")
	if len(fleet.Devices) != 10 {
		t.Fatalf("returned %d rows, want 10", len(fleet.Devices))
	}
	if fleet.Configured != 500 {
		t.Errorf("configured = %d, want 500", fleet.Configured)
	}

	var counted int
	for _, n := range fleet.Summary {
		counted += n
	}
	if counted != 500 {
		t.Errorf("summary counts %d devices across %v, want all 500", counted, fleet.Summary)
	}
	if fleet.Summary[StateOK] != 500 {
		t.Errorf("summary[ok] = %d, want 500: every device is polling cleanly", fleet.Summary[StateOK])
	}
}

// TestFleetSearchIsAppliedServerSide covers the reason paging needs the server
// to search: the browser no longer holds the rows to search through.
func TestFleetSearchIsAppliedServerSide(t *testing.T) {
	t.Parallel()

	s := storeFleet(t, 500)

	fleet := fleetPage(t, s, "search=device-00123&limit=25")
	if fleet.Total != 1 {
		t.Fatalf("total = %d, want 1 match for an exact name", fleet.Total)
	}
	if len(fleet.Devices) != 1 || fleet.Devices[0].Name != "device-00123" {
		t.Fatalf("rows = %v, want just device-00123", deviceNames(fleet))
	}

	// An address matches too: an operator often knows the IP, not the name.
	byAddress := fleetPage(t, s, "search="+fleet.Devices[0].Address+"&limit=25")
	if byAddress.Total != 1 {
		t.Errorf("searching the address matched %d devices, want 1", byAddress.Total)
	}

	// And the summary still describes the fleet, not the search.
	if byAddress.Configured != 500 {
		t.Errorf("configured = %d under a search, want the whole fleet", byAddress.Configured)
	}
}

// TestFleetStateFilterIsAppliedServerSide covers the summary chips: clicking one
// has to narrow the rows without the browser holding them all.
func TestFleetStateFilterIsAppliedServerSide(t *testing.T) {
	t.Parallel()

	s := storeFleet(t, 20)

	ok := fleetPage(t, s, "state=ok&limit=100")
	if ok.Total != 20 {
		t.Errorf("state=ok matched %d, want all 20", ok.Total)
	}

	none := fleetPage(t, s, "state=unreachable&limit=100")
	if none.Total != 0 || len(none.Devices) != 0 {
		t.Errorf("state=unreachable matched %d rows, want none", len(none.Devices))
	}
	if none.Configured != 20 {
		t.Errorf("configured = %d, want 20 even when the filter matches nothing", none.Configured)
	}
}

// TestFleetStatusWithoutPagingIsUnchanged keeps the CLI and any other existing
// caller working: no limit means the whole fleet, as before.
func TestFleetStatusWithoutPagingIsUnchanged(t *testing.T) {
	t.Parallel()

	s := storeFleet(t, 120)

	fleet := fleetPage(t, s, "")
	if got := len(fleet.Devices); got != 120 {
		t.Errorf("returned %d rows with no limit, want the whole fleet", got)
	}
	if fleet.Total != 120 {
		t.Errorf("total = %d, want 120", fleet.Total)
	}
}

// TestFleetPagingRejectsNonsense is the bad-input half: a limit that is not a
// number is a client defect and must be reported, not guessed at.
func TestFleetPagingRejectsNonsense(t *testing.T) {
	t.Parallel()

	s := storeFleet(t, 5)

	for _, query := range []string{"limit=abc", "offset=-1", "limit=-5"} {
		req := httptest.NewRequest(http.MethodGet, "/api/devices/status?"+query, nil)
		w := httptest.NewRecorder()
		s.router.ServeHTTP(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("%s answered %d, want %d", query, w.Code, http.StatusBadRequest)
		}
	}
}

// TestFleetOffsetPastTheEndIsEmptyNotAnError covers the reader who pages beyond
// a fleet that shrank under them.
func TestFleetOffsetPastTheEndIsEmptyNotAnError(t *testing.T) {
	t.Parallel()

	s := storeFleet(t, 10)

	fleet := fleetPage(t, s, "limit=25&offset=100")
	if len(fleet.Devices) != 0 {
		t.Errorf("returned %d rows past the end, want none", len(fleet.Devices))
	}
	if fleet.Total != 10 {
		t.Errorf("total = %d, want the real count so the pager can send them back", fleet.Total)
	}
}

// BenchmarkFleetStatusPayload measures what a refresh actually serializes, which
// is the cost this change exists to remove.
func BenchmarkFleetStatusPayload(b *testing.B) {
	for _, spec := range []struct {
		name  string
		query string
	}{
		{"whole-fleet", ""},
		{"one-page", "limit=25"},
	} {
		b.Run(spec.name, func(b *testing.B) {
			s, poller, devices := fleetOf(b, 10000, 0)
			s.pollerClient = poller
			for i := range devices {
				device := devices[i]
				if err := s.storage.CreateDevice(context.Background(), &device); err != nil {
					b.Fatalf("create device: %v", err)
				}
			}

			b.ResetTimer()
			var bytes int
			for i := 0; i < b.N; i++ {
				req := httptest.NewRequest(http.MethodGet, "/api/devices/status?"+spec.query, nil)
				w := httptest.NewRecorder()
				s.router.ServeHTTP(w, req)
				bytes = w.Body.Len()
			}
			b.StopTimer()
			b.ReportMetric(float64(bytes), "bytes/refresh")
			fmt.Fprintf(b.Output(), "")
		})
	}
}
