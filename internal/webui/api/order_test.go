package api

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	"github.com/jeremyhahn/go-netmon/internal/webui/storage"
)

// GET /api/devices/{id}/mibs returned all_mibs and direct_mibs in Go's map
// iteration order: four consecutive calls gave four different orders. The web
// UI feeds that array straight into its tab builder, so the dashboard's MIB
// tabs, their panes and the tab that opens by default all moved on every page
// load. These tests state that every list endpoint answers in the same order
// each time, and that the order is one a human can scan.

// orderedFixture stores a device and a profile with MIBs attached to both, in
// an order that is not the sorted one.
func orderedFixture(t *testing.T, s *Server) (deviceID, profileID int64) {
	t.Helper()

	ctx := context.Background()
	deviceID = storeDevice(t, s, "zulu-router", "192.0.2.10")

	profile := &storage.DeviceProfile{Name: "Core Routers"}
	if err := s.storage.CreateDeviceProfile(ctx, profile); err != nil {
		t.Fatalf("create profile: %v", err)
	}
	if err := s.storage.AssignProfileToDevice(ctx, deviceID, profile.ID); err != nil {
		t.Fatalf("assign profile: %v", err)
	}

	for i, name := range []string{"UDP-MIB", "CISCO-BGP4-MIB", "IF-MIB", "SNMPv2-MIB", "TCP-MIB", "ENTITY-MIB"} {
		m := &storage.MIB{Name: name, FilePath: "/app/data/mibs/uploaded/" + name + ".txt"}
		if err := s.storage.CreateMIB(ctx, m); err != nil {
			t.Fatalf("create MIB %s: %v", name, err)
		}
		if i%2 == 0 {
			if err := s.storage.AssignMIBToDevice(ctx, deviceID, m.ID); err != nil {
				t.Fatalf("assign %s to device: %v", name, err)
			}
			continue
		}
		if err := s.storage.AssignMIBToProfile(ctx, profile.ID, m.ID); err != nil {
			t.Fatalf("assign %s to profile: %v", name, err)
		}
	}

	return deviceID, profile.ID
}

// namesFromResponse pulls the "name" out of each record under key, which every
// list endpoint returns as an array of objects.
func namesFromResponse(t *testing.T, body []byte, key string) []string {
	t.Helper()

	var payload map[string][]struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode %s: %v (body %s)", key, err, body)
	}

	records, ok := payload[key]
	if !ok {
		t.Fatalf("response has no %q: %s", key, body)
	}

	names := make([]string, len(records))
	for i, record := range records {
		names[i] = record.Name
	}
	return names
}

// assertEndpointOrder requests path twelve times and requires the named array
// to come back the same, in ascending order, every time.
func assertEndpointOrder(t *testing.T, s *Server, path, key string) {
	t.Helper()

	var first []string
	for call := 1; call <= 12; call++ {
		rec := do(t, s, http.MethodGet, path, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200 (body %q)", path, rec.Code, rec.Body.String())
		}

		got := namesFromResponse(t, rec.Body.Bytes(), key)
		if call == 1 {
			first = got
			if len(got) < 2 {
				t.Fatalf("GET %s returned %d records in %s; the fixture gives it several", path, len(got), key)
			}
			if !slices.IsSorted(got) {
				t.Errorf("GET %s returned %s as %v, which is not in name order", path, key, got)
			}
			continue
		}
		if !slices.Equal(got, first) {
			t.Fatalf("GET %s returned %s as %v on call %d, having returned %v on call 1",
				path, key, got, call, first)
		}
	}
}

func TestDeviceMIBEndpointIsDeterministic(t *testing.T) {
	s := newTestServer(t)
	deviceID, _ := orderedFixture(t, s)

	assertEndpointOrder(t, s, "/api/devices/"+itoa(deviceID)+"/mibs", "all_mibs")
	assertEndpointOrder(t, s, "/api/devices/"+itoa(deviceID)+"/mibs", "direct_mibs")
}

func TestProfileMIBEndpointIsDeterministic(t *testing.T) {
	s := newTestServer(t)
	_, profileID := orderedFixture(t, s)

	assertEndpointOrder(t, s, "/api/profiles/"+itoa(profileID)+"/mibs", "mibs")
}

func TestCatalogAndRecordEndpointsAreDeterministic(t *testing.T) {
	s := newTestServer(t)
	orderedFixture(t, s)

	// A second device and profile so the record lists have something to order.
	storeDevice(t, s, "alpha-switch", "192.0.2.11")
	if err := s.storage.CreateDeviceProfile(context.Background(),
		&storage.DeviceProfile{Name: "Access Switches"}); err != nil {
		t.Fatalf("create profile: %v", err)
	}

	assertEndpointOrder(t, s, "/api/mibs", "mibs")
	assertEndpointOrder(t, s, "/api/devices", "devices")
	assertEndpointOrder(t, s, "/api/profiles", "profiles")
}

// TestDeviceMIBEndpointRejectsAMalformedID is the bad-input case for the
// endpoint the ordering tests exercise.
func TestDeviceMIBEndpointRejectsAMalformedID(t *testing.T) {
	s := newTestServer(t)

	// The route only matches digits, so a non-numeric ID is not this handler's
	// resource at all; an ID that is numeric but absent must still answer with
	// an empty list rather than another device's MIBs.
	if rec := do(t, s, http.MethodGet, "/api/devices/not-a-number/mibs", ""); rec.Code == http.StatusOK {
		t.Errorf("GET with a non-numeric device ID = %d, want a failure", rec.Code)
	}

	rec := do(t, s, http.MethodGet, "/api/devices/4242/mibs", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET for an unknown device = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if got := namesFromResponse(t, rec.Body.Bytes(), "all_mibs"); len(got) != 0 {
		t.Errorf("an unknown device reports MIBs %v", got)
	}
}

// TestCompareInstanceOrdersNumericallyThenByText pins the order of the instance
// picker: SNMP table indexes are numbers, and 10 belongs after 2.
func TestCompareInstanceOrdersNumericallyThenByText(t *testing.T) {
	instances := []string{"10", "2", "GigabitEthernet0/0/1", "1", "Loopback0", "10"}
	slices.SortFunc(instances, compareInstance)

	want := []string{"1", "2", "10", "10", "GigabitEthernet0/0/1", "Loopback0"}
	if !slices.Equal(instances, want) {
		t.Errorf("sorted to %v, want %v", instances, want)
	}
}

// TestCompareInstanceIsATotalOrder covers the degenerate inputs: equal values,
// and text that cannot be a number at all. A comparison that is not consistent
// makes the sort's output depend on the input order, which is the very thing
// being fixed.
func TestCompareInstanceIsATotalOrder(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1", "1", 0},
		{"", "", 0},
		{"eth0", "eth0", 0},
		{"2", "eth0", -1},
		{"eth0", "2", 1},
		{"", "0", 1},
		{"eth0", "eth1", -1},
		{"eth1", "eth0", 1},
	}

	for _, tc := range cases {
		if got := compareInstance(tc.a, tc.b); got != tc.want {
			t.Errorf("compareInstance(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
		if got, reversed := compareInstance(tc.a, tc.b), compareInstance(tc.b, tc.a); got != -reversed {
			t.Errorf("compareInstance(%q, %q) = %d but the reverse is %d", tc.a, tc.b, got, reversed)
		}
	}
}

// TestLabelSetKeyDoesNotDependOnMapOrder is the grouping bug behind the charts:
// the key a series is grouped under was built by ranging over the label map, so
// two samples with identical labels could land under two different keys and one
// series was silently drawn as two.
func TestLabelSetKeyDoesNotDependOnMapOrder(t *testing.T) {
	labels := map[string]string{
		"device": "core-01", "ifname": "Gi0/0/1", "instance": "1",
		"job": "snmp", "module": "IF-MIB", "unit": "octets",
	}

	first := labelsToStringFromMap(labels, "ifHCInOctets")
	for call := 2; call <= 24; call++ {
		if got := labelsToStringFromMap(labels, "ifHCInOctets"); got != first {
			t.Fatalf("call %d produced %q, call 1 produced %q", call, got, first)
		}
	}

	// A different label set must still key differently, or grouping would merge
	// series that are not the same.
	other := map[string]string{"device": "core-01", "ifname": "Gi0/0/2"}
	if labelsToStringFromMap(other, "ifHCInOctets") == first {
		t.Error("two different label sets produced the same grouping key")
	}
}

// TestConvertToTimeSeriesIsDeterministic covers the series order a chart is
// drawn in: in map order the legend and the colors change on every identical
// query.
func TestConvertToTimeSeriesIsDeterministic(t *testing.T) {
	metrics := make([]interface{}, 0, 4)
	for _, iface := range []string{"Gi0/0/3", "Gi0/0/1", "Gi0/0/4", "Gi0/0/2"} {
		metrics = append(metrics, map[string]interface{}{
			"metric": map[string]interface{}{
				"__name__": "ifHCInOctets",
				"device":   "core-01",
				"ifname":   iface,
			},
			"values": []interface{}{
				[]interface{}{float64(1), "17"},
			},
		})
	}

	first := seriesNamesOf(t, convertToTimeSeries(metrics, nil))
	if len(first) != 4 {
		t.Fatalf("grouped %d series from four distinct label sets", len(first))
	}
	if !slices.IsSorted(first) {
		t.Errorf("series came back as %v, which is not a settled order", first)
	}

	for call := 2; call <= 12; call++ {
		if got := seriesNamesOf(t, convertToTimeSeries(metrics, nil)); !slices.Equal(got, first) {
			t.Fatalf("call %d returned %v, call 1 returned %v", call, got, first)
		}
	}
}

// TestConvertToTimeSeriesWithNothingToConvert is the bad-input case: entries
// that are not metrics at all must be skipped rather than panic.
func TestConvertToTimeSeriesWithNothingToConvert(t *testing.T) {
	if got := convertToTimeSeries(nil, nil); len(got) != 0 {
		t.Errorf("no metrics produced %d series", len(got))
	}
	if got := convertToTimeSeries([]interface{}{"not a metric", 42}, nil); len(got) != 0 {
		t.Errorf("unusable entries produced %d series", len(got))
	}
}

// seriesLabels renders each series as the interface it belongs to, which is
// what the legend shows.
func seriesNamesOf(t *testing.T, series []TimeSeriesData) []string {
	t.Helper()

	names := make([]string, len(series))
	for i, s := range series {
		names[i] = s.Labels["ifname"]
	}
	return names
}
