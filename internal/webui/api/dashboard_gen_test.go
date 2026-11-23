package api

import (
	"github.com/jeremyhahn/go-netmon/internal/promname"

	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-netmon/internal/webui/storage"
)

// dashboardServer builds a server whose MIB storage path is a fresh directory,
// so the parser the generator initializes has somewhere to open.
func dashboardServer(t *testing.T) *Server {
	t.Helper()

	s := newTestServer(t)
	s.mibStoragePath = filepath.Join(t.TempDir(), "mib-storage")
	s.promClient = (&promStub{body: `{"status":"success","data":["snmp_ifinoctets"]}`}).start(t)
	return s
}

func TestGenerateDashboardForAnExplicitMIBSet(t *testing.T) {
	s := dashboardServer(t)
	ctx := context.Background()

	device := storeDevice(t, s, "bastion", "192.0.2.14")
	mib := &storage.MIB{Name: "IF-MIB", FilePath: "/mibs/IF-MIB.mib"}
	if err := s.storage.CreateMIB(ctx, mib); err != nil {
		t.Fatalf("CreateMIB: %v", err)
	}

	body := `{"device_id":` + itoa(device) + `,"mib_ids":[` + itoa(mib.ID) + `],"title":"Aten PDU"}`
	rec := do(t, s, http.MethodPost, "/api/dashboards/generate", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Aten PDU") {
		t.Errorf("body = %q, want the requested title", rec.Body.String())
	}
}

func TestGenerateDashboardRejectsBadInput(t *testing.T) {
	s := dashboardServer(t)
	ctx := context.Background()

	device := storeDevice(t, s, "bastion", "192.0.2.14")
	mib := &storage.MIB{Name: "IF-MIB", FilePath: "/mibs/IF-MIB.mib"}
	if err := s.storage.CreateMIB(ctx, mib); err != nil {
		t.Fatalf("CreateMIB: %v", err)
	}

	tests := []struct {
		name       string
		body       string
		wantStatus int
	}{
		{"a malformed body", "{", http.StatusBadRequest},
		{"no device_id", `{"mib_ids":[1]}`, http.StatusBadRequest},
		{"no mib_ids", `{"device_id":` + itoa(device) + `}`, http.StatusBadRequest},
		{"an unknown device", `{"device_id":4242,"mib_ids":[` + itoa(mib.ID) + `]}`, http.StatusNotFound},
		{"an unknown MIB", `{"device_id":` + itoa(device) + `,"mib_ids":[4242]}`, http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(t, s, http.MethodPost, "/api/dashboards/generate", tt.body)
			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d (body %q)", rec.Code, tt.wantStatus, rec.Body.String())
			}
		})
	}
}

func TestGenerateDeviceDashboard(t *testing.T) {
	s := dashboardServer(t)
	ctx := context.Background()

	device := storeDevice(t, s, "bastion", "192.0.2.14")
	mib := &storage.MIB{Name: "IF-MIB", FilePath: "/mibs/IF-MIB.mib"}
	if err := s.storage.CreateMIB(ctx, mib); err != nil {
		t.Fatalf("CreateMIB: %v", err)
	}
	if err := s.storage.AssignMIBToDevice(ctx, device, mib.ID); err != nil {
		t.Fatalf("AssignMIBToDevice: %v", err)
	}

	rec := do(t, s, http.MethodGet, "/api/devices/"+itoa(device)+"/dashboard", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "bastion") {
		t.Errorf("body = %q, want it to name the device", rec.Body.String())
	}
}

func TestGenerateDeviceDashboardRejectsBadInput(t *testing.T) {
	s := dashboardServer(t)
	device := storeDevice(t, s, "bastion", "192.0.2.14")

	tests := []struct {
		name       string
		path       string
		wantStatus int
	}{
		{"an unknown device", "/api/devices/4242/dashboard", http.StatusNotFound},
		{"a device with no MIBs", "/api/devices/" + itoa(device) + "/dashboard", http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(t, s, http.MethodGet, tt.path, "")
			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d (body %q)", rec.Code, tt.wantStatus, rec.Body.String())
			}
		})
	}
}

// TestChartMetricNames pins the metric name a generated chart carries to the
// exporter's naming rule, including the info-metric shape a non-numeric object
// reaches Prometheus in.
func TestChartMetricNames(t *testing.T) {
	tests := []struct {
		name   string
		object string
		want   string
	}{
		{"a plain object name", "ifInOctets", "snmp_ifInOctets"},
		{"a name with separators", "cbgpPeer2AddrFamily-Prefix", "snmp_cbgpPeer2AddrFamily_Prefix"},
		{"case is preserved", "cpmCPUTotal5sec", "snmp_cpmCPUTotal5sec"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := promname.MetricName(tt.object); got != tt.want {
				t.Errorf("promname.MetricName(%q) = %q, want %q", tt.object, got, tt.want)
			}
		})
	}

	if got, want := promname.InfoMetricName("ifDescr"), "snmp_ifdescr_info"; got != want {
		t.Errorf("promname.InfoMetricName(\"ifDescr\") = %q, want %q", got, want)
	}
}

// TestTheGeneratorAsksAboutThisDeviceOnly is the contract behind every panel.
//
// A panel is offered only for a metric the device reports. The question used to
// be asked fleet-wide, so a metric any device produced offered a panel on every
// device: 75 of core-01's 408 panels named a metric it does not produce, and
// narrowing a MIB left its panels behind because the name lingers in the label
// index after the series goes stale.
func TestTheGeneratorAsksAboutThisDeviceOnly(t *testing.T) {
	s := newTestServer(t)
	s.mibStoragePath = filepath.Join(t.TempDir(), "mib-storage")
	stub := &promStub{body: `{"status":"success","data":["snmp_ifinoctets"]}`}
	s.promClient = stub.start(t)

	ctx := context.Background()
	device := storeDevice(t, s, "core-01", "192.0.2.1")
	mib := &storage.MIB{Name: "IF-MIB", FilePath: "/mibs/IF-MIB.mib"}
	if err := s.storage.CreateMIB(ctx, mib); err != nil {
		t.Fatalf("CreateMIB: %v", err)
	}
	if err := s.storage.AssignMIBToDevice(ctx, device, mib.ID); err != nil {
		t.Fatalf("AssignMIBToDevice: %v", err)
	}

	rec := do(t, s, http.MethodGet, "/api/devices/"+itoa(device)+"/dashboard", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}

	// The device has to be in what was asked of Prometheus. Without it the
	// answer describes the fleet, and the panels describe no device at all.
	//
	// Checked across ALL queries, not the last one: the generator now also
	// reads the metric catalog, and that query is deliberately fleet-wide.
	if !stub.asked("core-01") {
		t.Errorf("no query named the device; asked %q", stub.allParams)
	}

	// And the converse, which is the property that keeps the catalog cheap:
	// it carries no device dimension, so it must NOT be asked per device. A
	// device-scoped catalog query would be O(devices) at the one layer this
	// design spent three review rounds keeping flat.
	for _, q := range stub.allParams {
		if strings.Contains(q, catalogMetric) && strings.Contains(q, "core-01") {
			t.Errorf("the catalog was queried per device: %q", q)
		}
	}
}
