package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeremyhahn/go-netmon/pkg/client"
)

// testDeviceFixture is the device the fake API serves in most cases.
func testDeviceFixture() client.Device {
	return client.Device{
		ID:          7,
		Name:        "core-01",
		Address:     "198.51.100.252",
		SNMPVersion: "2c",
		Community:   "public",
		Port:        161,
		CreatedAt:   time.Now().Add(-24 * time.Hour),
	}
}

// testDeviceStatus is the poller's view of testDeviceFixture.
func testDeviceStatus() client.DeviceStatus {
	return client.DeviceStatus{
		Name:             "core-01",
		Address:          "198.51.100.252",
		Connected:        true,
		LastPollTime:     time.Now().Add(-20 * time.Second),
		LastPollSuccess:  true,
		MetricsCollected: 6610,
		PollInterval:     "30s",
	}
}

// instantSeries builds n distinct series for the device detail view, encoded
// the way the server really encodes them: a "metrics" envelope with Unix
// timestamps rather than a bare array of objects.
func instantSeries(n int) map[string]any {
	values := make([]map[string]any, 0, n)
	for i := range n {
		values = append(values, map[string]any{
			"metric": map[string]string{
				"__name__": "snmp_ifinoctets",
				"device":   "core-01",
				"ifindex":  fmt.Sprint(i),
			},
			"value":     float64(i),
			"timestamp": time.Now().Add(-10 * time.Second).Unix(),
		})
	}
	return map[string]any{"metrics": values}
}

// typedInstantSeries builds the same n series already decoded, for tests that
// exercise a renderer directly rather than through the API client.
func typedInstantSeries(n int) []client.InstantValue {
	values := make([]client.InstantValue, 0, n)
	for i := range n {
		values = append(values, client.InstantValue{
			Metric: map[string]string{
				"__name__": "snmp_ifinoctets",
				"device":   "core-01",
				"ifindex":  fmt.Sprint(i),
			},
			Value: float64(i),
			Time:  time.Now().Add(-10 * time.Second),
		})
	}
	return values
}

func TestMonitorFleetSummarizesEveryDevice(t *testing.T) {
	second := client.Device{ID: 9, Name: "edge-sw-02", Address: "198.51.100.10", Port: 161}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/devices", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, map[string]any{
			"devices": []client.Device{testDeviceFixture(), second},
		})
	})
	mux.HandleFunc("/api/devices/7/status", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, testDeviceStatus())
	})
	mux.HandleFunc("/api/devices/9/status", func(w http.ResponseWriter, _ *http.Request) {
		// The poller does not know this device yet; the fleet view must still
		// list it rather than failing outright.
		http.Error(w, "device not polled", http.StatusNotFound)
	})

	env, out := newFakeEnv(t, mux)
	if err := runCommand(t, newMonitorCommand(env)); err != nil {
		t.Fatalf("monitor: %v", err)
	}

	got := out.String()
	for _, want := range []string{
		"2 devices", "1 polling", "1 with errors",
		"NAME", "ADDRESS", "CONNECTED", "LAST POLL", "METRICS", "INTERVAL",
		"core-01", "edge-sw-02", "198.51.100.252", "6610",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("fleet view missing %q:\n%s", want, got)
		}
	}
}

func TestMonitorFleetEmptyAndListError(t *testing.T) {
	t.Run("no devices", func(t *testing.T) {
		mux := http.NewServeMux()
		mux.HandleFunc("/api/devices", func(w http.ResponseWriter, _ *http.Request) {
			respondJSON(t, w, map[string]any{"devices": []client.Device{}})
		})

		env, out := newFakeEnv(t, mux)
		if err := runCommand(t, newMonitorCommand(env)); err != nil {
			t.Fatalf("monitor: %v", err)
		}
		if !strings.Contains(out.String(), "No devices configured") {
			t.Errorf("expected an empty-fleet notice, got:\n%s", out)
		}
	})

	t.Run("server error", func(t *testing.T) {
		mux := http.NewServeMux()
		mux.HandleFunc("/api/devices", func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "database unavailable", http.StatusInternalServerError)
		})

		env, _ := newFakeEnv(t, mux)
		err := runCommand(t, newMonitorCommand(env))
		if err == nil {
			t.Fatal("expected an error when the device list cannot be read")
		}

		var apiErr *client.APIError
		if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusInternalServerError {
			t.Fatalf("got %v, want a 500 APIError", err)
		}
	})
}

func TestMonitorDeviceShowsHeaderAndSeries(t *testing.T) {
	var query string

	mux := http.NewServeMux()
	mux.HandleFunc("/api/devices", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, map[string]any{"devices": []client.Device{testDeviceFixture()}})
	})
	mux.HandleFunc("/api/devices/7", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, testDeviceFixture())
	})
	mux.HandleFunc("/api/devices/7/status", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, testDeviceStatus())
	})
	mux.HandleFunc("/api/metrics/instant", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode instant request: %v", err)
		}
		query = body.Query
		respondJSON(t, w, instantSeries(3))
	})

	env, out := newFakeEnv(t, mux)
	if err := runCommand(t, newMonitorCommand(env), "core-01"); err != nil {
		t.Fatalf("monitor device: %v", err)
	}

	// The detail view selects by the device label the exporter emits.
	if want := `{device="core-01",__name__=~"snmp_.*"}`; query != want {
		t.Errorf("instant query = %q, want %q", query, want)
	}

	got := out.String()
	for _, want := range []string{
		"Device:", "core-01",
		"Address:", "198.51.100.252:161",
		"Connected:", "yes",
		"Metrics collected:", "6610",
		"Last poll:",
		"Current values (3 series)",
		// Nothing in the fake API's MIBs accounts for these series, so they
		// keep their full PromQL identity rather than losing their labels.
		"NAME", "VALUE", "TIME",
		`snmp_ifinoctets{device="core-01",ifindex="0"}`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("detail view missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "hidden") {
		t.Errorf("unexpected truncation notice with 3 series under --top 20:\n%s", got)
	}
}

func TestMonitorDeviceTopTruncatesAndSaysSo(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/devices/7", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, testDeviceFixture())
	})
	mux.HandleFunc("/api/devices/7/status", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, testDeviceStatus())
	})
	mux.HandleFunc("/api/metrics/instant", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, instantSeries(5))
	})

	env, out := newFakeEnv(t, mux)
	if err := runCommand(t, newMonitorCommand(env), "7", "--top", "2"); err != nil {
		t.Fatalf("monitor device: %v", err)
	}

	got := out.String()
	if !strings.Contains(got, "Current values (5 series)") {
		t.Errorf("header must report the full series count:\n%s", got)
	}
	if !strings.Contains(got, "3 more series hidden") {
		t.Errorf("expected a truncation notice for 5 series under --top 2:\n%s", got)
	}
	// Sorting happens before truncation, so the first two indexes survive.
	if !strings.Contains(got, `ifindex="1"`) || strings.Contains(got, `ifindex="4"`) {
		t.Errorf("truncation kept the wrong series:\n%s", got)
	}
}

func TestMonitorDeviceUnresolvable(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/devices", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, map[string]any{"devices": []client.Device{testDeviceFixture()}})
	})
	mux.HandleFunc("/api/metrics/instant", func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("metrics must not be queried for an unresolvable device")
	})

	env, _ := newFakeEnv(t, mux)
	err := runCommand(t, newMonitorCommand(env), "ghost")
	if err == nil {
		t.Fatal("expected an error for an unknown device")
	}

	var notFound *client.NotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("got %T, want *client.NotFoundError", err)
	}
}

func TestMonitorDeviceSurvivesMissingStatus(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/devices/7", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, testDeviceFixture())
	})
	mux.HandleFunc("/api/devices/7/status", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "device not polled", http.StatusNotFound)
	})
	mux.HandleFunc("/api/metrics/instant", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, instantSeries(1))
	})

	env, out := newFakeEnv(t, mux)
	if err := runCommand(t, newMonitorCommand(env), "7"); err != nil {
		t.Fatalf("monitor device: %v", err)
	}

	got := out.String()
	if !strings.Contains(got, "Status:") || !strings.Contains(got, "unavailable") {
		t.Errorf("expected the header to explain the missing status:\n%s", got)
	}
	if !strings.Contains(got, "Current values (1 series)") {
		t.Errorf("metrics must still render without poller status:\n%s", got)
	}
}

func TestMonitorDeviceMetricsError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/devices/7", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, testDeviceFixture())
	})
	mux.HandleFunc("/api/devices/7/status", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, testDeviceStatus())
	})
	mux.HandleFunc("/api/metrics/instant", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "prometheus unreachable", http.StatusInternalServerError)
	})

	env, _ := newFakeEnv(t, mux)
	err := runCommand(t, newMonitorCommand(env), "7")
	if err == nil {
		t.Fatal("expected an error when the metric query fails")
	}
	if !strings.Contains(err.Error(), "prometheus unreachable") {
		t.Errorf("error lost the server's explanation: %v", err)
	}
}

func TestMonitorDeviceJSON(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/devices/7", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, testDeviceFixture())
	})
	mux.HandleFunc("/api/devices/7/status", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, testDeviceStatus())
	})
	mux.HandleFunc("/api/metrics/instant", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, instantSeries(2))
	})

	env, out := newFakeEnv(t, mux)
	env.Opts.JSON = true
	if err := runCommand(t, newMonitorCommand(env), "7"); err != nil {
		t.Fatalf("monitor device: %v", err)
	}

	var decoded struct {
		Device client.Device         `json:"device"`
		Status client.DeviceStatus   `json:"status"`
		Series []client.InstantValue `json:"series"`
	}
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("output is not the detail document: %v\n%s", err, out)
	}
	if decoded.Device.Name != "core-01" || len(decoded.Series) != 2 {
		t.Errorf("decoded %+v", decoded)
	}
}

func TestDeviceSeriesQueryEscapesNames(t *testing.T) {
	if got, want := deviceSeriesQuery("core-01"),
		`{device="core-01",__name__=~"snmp_.*"}`; got != want {
		t.Errorf("deviceSeriesQuery = %q, want %q", got, want)
	}
	// A name carrying a quote must not break out of the label matcher.
	if got, want := deviceSeriesQuery(`bad"name`),
		`{device="bad\"name",__name__=~"snmp_.*"}`; got != want {
		t.Errorf("deviceSeriesQuery = %q, want %q", got, want)
	}
}

// TestDeviceSeriesQueryExcludesPollerTelemetry documents why the name matcher
// is there: go-netmon's own metrics carry a device label too, and without the
// prefix filter they crowd out the device's actual SNMP data.
func TestDeviceSeriesQueryExcludesPollerTelemetry(t *testing.T) {
	query := deviceSeriesQuery("core-01")
	if !strings.Contains(query, `__name__=~"snmp_.*"`) {
		t.Errorf("query %q must restrict to SNMP-derived metrics", query)
	}
}

func TestRenderMonitorSeriesEdgeCases(t *testing.T) {
	t.Run("no samples yet", func(t *testing.T) {
		out := &bytes.Buffer{}
		if err := renderMonitorSeries(out, nil, defaultMonitorTop, nil); err != nil {
			t.Fatalf("renderMonitorSeries: %v", err)
		}
		if !strings.Contains(out.String(), "may not have completed a poll") {
			t.Errorf("expected a no-samples explanation, got:\n%s", out)
		}
	})

	t.Run("top zero shows everything", func(t *testing.T) {
		out := &bytes.Buffer{}
		if err := renderMonitorSeries(out, typedInstantSeries(3), 0, nil); err != nil {
			t.Fatalf("renderMonitorSeries: %v", err)
		}
		if strings.Contains(out.String(), "hidden") {
			t.Errorf("--top 0 must not truncate:\n%s", out)
		}
	})
}

// ifMIBDetail is the module the fake API serves for the naming tests. It holds
// the IF-MIB objects whose samples the instant query returns.
func ifMIBDetail() client.MIBDetail {
	return client.MIBDetail{ID: 3, Name: "IF-MIB", Objects: ifMIBObjects()}
}

// mibFetches counts the requests the naming index costs, so a test can tell
// whether the index was built, skipped or reused.
type mibFetches struct {
	list   atomic.Int64
	detail atomic.Int64
}

// exportedSample is one series as the exporter writes it: named after the MIB
// object it instantiates, with the instance in the "oid" label.
type exportedSample struct {
	metric string
	oid    string
	value  float64
}

// oidInstantSeries encodes samples the way the exporter really emits them. The
// metric name identifies the object and nothing else, so every row of a column
// shares it and only the "oid" label says which row a sample is.
func oidInstantSeries(samples []exportedSample) map[string]any {
	values := make([]map[string]any, 0, len(samples))
	for _, sample := range samples {
		values = append(values, map[string]any{
			"metric": map[string]string{
				"__name__": sample.metric,
				"device":   "core-01",
				"oid":      sample.oid,
			},
			"value":     sample.value,
			"timestamp": time.Now().Add(-10 * time.Second).Unix(),
		})
	}
	return map[string]any{"metrics": values}
}

// namedSeriesMux serves a device whose MIBs are loadable, so the detail view
// can name its OID metrics, and counts what naming them costs.
func namedSeriesMux(t *testing.T, mibs client.DeviceMIBs) (*http.ServeMux, *mibFetches) {
	t.Helper()

	fetches := &mibFetches{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/devices/7", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, testDeviceFixture())
	})
	mux.HandleFunc("GET /api/devices/7/status", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, testDeviceStatus())
	})
	mux.HandleFunc("GET /api/devices/7/mibs", func(w http.ResponseWriter, _ *http.Request) {
		fetches.list.Add(1)
		respondJSON(t, w, mibs)
	})
	mux.HandleFunc("GET /api/mibs/3", func(w http.ResponseWriter, _ *http.Request) {
		fetches.detail.Add(1)
		respondJSON(t, w, ifMIBDetail())
	})
	mux.HandleFunc("POST /api/metrics/instant", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, oidInstantSeries([]exportedSample{
			{metric: "snmp_ifInOctets", oid: ".1.3.6.1.2.1.2.2.1.10.10", value: 1107275976},
			{metric: "snmp_ifOperStatus", oid: ".1.3.6.1.2.1.2.2.1.8.10", value: 1},
			{metric: "snmp_sysUpTime", oid: ".1.3.6.1.2.1.1.3.0", value: 8640000},
			// No loaded MIB describes this one, so the exporter falls back to
			// naming it after the instance OID.
			{metric: "snmp__1_3_6_1_4_1_9_2_1_58_0", oid: ".1.3.6.1.4.1.9.2.1.58.0", value: 42},
		}))
	})
	return mux, fetches
}

// deviceIFMIBs is the MIB assignment behind the naming tests.
func deviceIFMIBs() client.DeviceMIBs {
	return client.DeviceMIBs{
		DirectMIBs: []client.AssignedMIB{enabledMIB(3, "IF-MIB", 0)},
		AllMIBs:    []client.AssignedMIB{enabledMIB(3, "IF-MIB", 0)},
	}
}

func TestMonitorDeviceNamesSeriesThroughMIBs(t *testing.T) {
	mux, _ := namedSeriesMux(t, deviceIFMIBs())

	env, out := newFakeEnv(t, mux)
	if err := runCommand(t, newMonitorCommand(env), "7"); err != nil {
		t.Fatalf("monitor device: %v", err)
	}

	got := out.String()
	for _, want := range []string{
		"NAME", "VALUE", "TIME",
		// A column is named with its decoded row index, and its value carries
		// the UNITS clause the MIB declares.
		"ifInOctets[10]", "1107275976 octets",
		// An enumeration renders as its label.
		"ifOperStatus[10]", "up(1)",
		// A scalar's sole instance is not worth showing, and TimeTicks read as
		// a duration rather than a count of hundredths.
		"sysUpTime", "1d 0h 0m 0s",
		// Nothing loaded defines this OID, so the series keeps its full
		// identity instead of being renamed to something misleading.
		`snmp__1_3_6_1_4_1_9_2_1_58_0{device="core-01",oid=".1.3.6.1.4.1.9.2.1.58.0"}`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("named detail view missing %q:\n%s", want, got)
		}
	}

	// The exporter's metric name is gone from the rows it could be resolved for.
	if strings.Contains(got, "snmp_ifInOctets") {
		t.Errorf("a resolved series must not also print its raw name:\n%s", got)
	}
	// Rows are ordered by the name the reader sees.
	if in, op := strings.Index(got, "ifInOctets[10]"), strings.Index(got, "ifOperStatus[10]"); in > op {
		t.Errorf("rows are not sorted by resolved name:\n%s", got)
	}
}

func TestMonitorDeviceRawKeepsOIDsReachable(t *testing.T) {
	mux, fetches := namedSeriesMux(t, deviceIFMIBs())

	env, out := newFakeEnv(t, mux)
	if err := runCommand(t, newMonitorCommand(env), "7", "--raw"); err != nil {
		t.Fatalf("monitor device: %v", err)
	}

	got := out.String()
	if !strings.Contains(got, "LABELS") {
		t.Errorf("--raw must keep the label column:\n%s", got)
	}
	if !strings.Contains(got, `snmp_ifInOctets{device="core-01",oid=".1.3.6.1.2.1.2.2.1.10.10"}`) {
		t.Errorf("--raw must print the stored metric name and labels:\n%s", got)
	}
	if strings.Contains(got, "ifInOctets[10]") {
		t.Errorf("--raw must not resolve names:\n%s", got)
	}
	if !strings.Contains(got, "1107275976") || strings.Contains(got, "octets") {
		t.Errorf("--raw must print the stored value:\n%s", got)
	}
	// Skipping resolution must also skip what resolution costs.
	if fetches.list.Load() != 0 || fetches.detail.Load() != 0 {
		t.Errorf("--raw loaded MIBs: %d list, %d detail requests",
			fetches.list.Load(), fetches.detail.Load())
	}
}

// TestMonitorDeviceSurvivesUnloadableMIBs covers the partial index: one module
// fails to load and the view must still name what the others define.
func TestMonitorDeviceSurvivesUnloadableMIBs(t *testing.T) {
	mux, fetches := namedSeriesMux(t, client.DeviceMIBs{
		AllMIBs: []client.AssignedMIB{
			enabledMIB(4, "CISCO-PROCESS-MIB", 0),
			enabledMIB(3, "IF-MIB", 0),
		},
	})
	mux.HandleFunc("GET /api/mibs/4", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "mib file missing", http.StatusInternalServerError)
	})

	env, out := newFakeEnv(t, mux)
	if err := runCommand(t, newMonitorCommand(env), "7"); err != nil {
		t.Fatalf("a module that will not load must not fail the command: %v", err)
	}

	got := out.String()
	if !strings.Contains(got, "ifInOctets[10]") {
		t.Errorf("the loadable module must still name its objects:\n%s", got)
	}
	if !strings.Contains(got, "Current values (4 series)") {
		t.Errorf("every series must still be listed:\n%s", got)
	}
	if fetches.detail.Load() != 1 {
		t.Errorf("the loadable module was fetched %d times, want 1", fetches.detail.Load())
	}
}

// TestMonitorDeviceWithNoMIBsAssigned covers the other end of the same path:
// there is nothing to name the series with, and the view falls back to their
// raw identities rather than showing bare OIDs with no labels.
func TestMonitorDeviceWithNoMIBsAssigned(t *testing.T) {
	mux, _ := namedSeriesMux(t, client.DeviceMIBs{})

	env, out := newFakeEnv(t, mux)
	if err := runCommand(t, newMonitorCommand(env), "7"); err != nil {
		t.Fatalf("a device with no MIBs must still render: %v", err)
	}

	got := out.String()
	if !strings.Contains(got, `snmp_ifInOctets{device="core-01",oid=".1.3.6.1.2.1.2.2.1.10.10"}`) {
		t.Errorf("without MIBs the series must keep their full identity:\n%s", got)
	}
	if !strings.Contains(got, "Current values (4 series)") {
		t.Errorf("every series must still be listed:\n%s", got)
	}
}

// TestMonitorDeviceCachesTheMIBIndex documents why --refresh is affordable:
// MIB definitions do not change, so the index is built once per device rather
// than once per frame.
func TestMonitorDeviceCachesTheMIBIndex(t *testing.T) {
	mux, fetches := namedSeriesMux(t, deviceIFMIBs())

	env, _ := newFakeEnv(t, mux)
	cmd := newMonitorCommand(env)
	for range 3 {
		if err := runCommand(t, cmd, "7"); err != nil {
			t.Fatalf("monitor device: %v", err)
		}
	}

	if got := fetches.list.Load(); got != 1 {
		t.Errorf("MIB assignments fetched %d times over 3 frames, want 1", got)
	}
	if got := fetches.detail.Load(); got != 1 {
		t.Errorf("MIB definitions fetched %d times over 3 frames, want 1", got)
	}
	if len(env.oidIndexes) != 1 {
		t.Errorf("index cache holds %d devices, want 1", len(env.oidIndexes))
	}
}
