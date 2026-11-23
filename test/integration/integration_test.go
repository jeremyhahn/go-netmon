//go:build integration
// +build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"

	"github.com/jeremyhahn/go-netmon/internal/config"
	"github.com/jeremyhahn/go-netmon/internal/metrics"
	"github.com/jeremyhahn/go-netmon/internal/mib/loader"
	"github.com/jeremyhahn/go-netmon/internal/mib/storage"
	"github.com/jeremyhahn/go-netmon/internal/poller"
	"github.com/jeremyhahn/go-netmon/internal/snmp"
)

// Probe settings for the readiness gates. The timeouts are deliberately short:
// a gate exists to find out whether a service is answering yet, so it must ask
// often rather than block on one slow attempt.
const (
	probeCommunity  = "public"
	probeTimeout    = 2 * time.Second
	probeRetries    = 1
	probeInterval   = time.Second
	serviceDeadline = 30 * time.Second
)

// testDevice is the device label every assertion in this file keys off. It has
// to match the device name the poller is configured with, since that is what
// reaches Prometheus as a label.
const testDevice = "test-router"

// SNMPProbeError reports a single failed attempt to read an object from an SNMP
// target. It names the target and the object, because "connection refused" on
// its own does not say which of the two services under test was silent.
type SNMPProbeError struct {
	Target string
	OID    string
	Stage  string
	Err    error
}

func (e *SNMPProbeError) Error() string {
	return fmt.Sprintf("snmp probe of %s (%s) failed at %s: %v", e.Target, e.OID, e.Stage, e.Err)
}

func (e *SNMPProbeError) Unwrap() error { return e.Err }

// SNMPUnansweredError reports a target that answered without supplying a usable
// value: an agent that replies noSuchObject is reachable but is not the agent
// the test needs, and reporting that as success is the failure this gate exists
// to prevent.
type SNMPUnansweredError struct {
	Target string
	OID    string
	Type   string
	Value  any
}

func (e *SNMPUnansweredError) Error() string {
	return fmt.Sprintf("snmp target %s answered %s with an unusable varbind: type=%s value=%v",
		e.Target, e.OID, e.Type, e.Value)
}

// SNMPUnreachableError reports an SNMP target that never produced a usable
// answer before the deadline. Elapsed and Attempts distinguish a target that
// refused instantly and was retried for the full window from one that swallowed
// every request until the budget ran out.
type SNMPUnreachableError struct {
	Target   string
	OID      string
	Attempts int
	Elapsed  time.Duration
	Err      error
}

func (e *SNMPUnreachableError) Error() string {
	return fmt.Sprintf("snmp target %s never answered %s: %d attempts over %s; last failure: %v",
		e.Target, e.OID, e.Attempts, e.Elapsed.Round(time.Millisecond), e.Err)
}

func (e *SNMPUnreachableError) Unwrap() error { return e.Err }

// PrometheusNotReadyError reports a Prometheus that never reported itself
// healthy before the deadline.
type PrometheusNotReadyError struct {
	URL      string
	Attempts int
	Elapsed  time.Duration
	Err      error
}

func (e *PrometheusNotReadyError) Error() string {
	return fmt.Sprintf("prometheus at %s never became healthy: %d attempts over %s; last failure: %v",
		e.URL, e.Attempts, e.Elapsed.Round(time.Millisecond), e.Err)
}

func (e *PrometheusNotReadyError) Unwrap() error { return e.Err }

// PrometheusQueryError reports a query that could not be carried out at all,
// as opposed to one that ran and returned nothing.
type PrometheusQueryError struct {
	Query string
	Stage string
	Err   error
}

func (e *PrometheusQueryError) Error() string {
	return fmt.Sprintf("prometheus query %q failed at %s: %v", e.Query, e.Stage, e.Err)
}

func (e *PrometheusQueryError) Unwrap() error { return e.Err }

// PrometheusStatusError reports a query answered with a non-200 status.
type PrometheusStatusError struct {
	Query      string
	StatusCode int
	Body       string
}

func (e *PrometheusStatusError) Error() string {
	return fmt.Sprintf("prometheus query %q returned HTTP %d: %s", e.Query, e.StatusCode, e.Body)
}

// PrometheusResponseError reports a query answered with HTTP 200 but an API
// status other than "success". Prometheus reports query errors this way, so a
// caller that only checks the HTTP status treats them as an empty result.
type PrometheusResponseError struct {
	Query  string
	Status string
	Body   string
}

func (e *PrometheusResponseError) Error() string {
	return fmt.Sprintf("prometheus query %q returned API status %q: %s", e.Query, e.Status, e.Body)
}

// MetricsNotFoundError reports a query that ran but matched no series. This is
// the outcome that means the pipeline under test produced nothing, so it is
// kept distinct from every transport failure above.
type MetricsNotFoundError struct {
	Query    string
	Attempts int
	Status   string
}

func (e *MetricsNotFoundError) Error() string {
	return fmt.Sprintf("prometheus query %q matched no series after %d attempts (API status %q)",
		e.Query, e.Attempts, e.Status)
}

// MetricShapeError reports a series that came back malformed: no name, the
// wrong device, or a value that is not a number. A series that cannot be read
// is not evidence that the exporter worked.
type MetricShapeError struct {
	Query  string
	Metric map[string]string
	Reason string
}

func (e *MetricShapeError) Error() string {
	return fmt.Sprintf("prometheus query %q returned an unusable series %v: %s", e.Query, e.Metric, e.Reason)
}

func TestEndToEndIntegration(t *testing.T) {
	prometheusURL := prometheusTarget()
	snmpHost, snmpPort := snmpTarget()

	t.Logf("Testing with Prometheus: %s, SNMP: %s", prometheusURL, net.JoinHostPort(snmpHost, snmpPort))

	// Wait for services to be ready
	if err := waitForPrometheus(t.Context(), prometheusURL, serviceDeadline); err != nil {
		t.Fatalf("Prometheus not ready: %v", err)
	}
	t.Log("Prometheus is ready")

	probe, err := waitForSNMP(snmpHost, snmpPort, serviceDeadline)
	if err != nil {
		t.Fatalf("SNMP simulator not ready: %v", err)
	}
	t.Logf("SNMP simulator is ready: %s = %v (%s)", probe.OID, probe.Value, probe.TypeString())

	// Create temporary directory for MIB storage
	tmpDir := t.TempDir()
	storagePath := filepath.Join(tmpDir, "mib-storage")

	// Create MIB configuration
	mibCfg := &config.MIBConfig{
		Paths:       []string{"../../mibs/standard"},
		StoragePath: storagePath,
		AutoLoad: []string{
			"SNMPv2-MIB",
			"IF-MIB",
		},
	}

	// Initialize MIB loader
	t.Log("Initializing MIB loader...")
	mibLoader, err := loader.New(mibCfg)
	if err != nil {
		t.Fatalf("Failed to create MIB loader: %v", err)
	}
	defer mibLoader.Close()

	// Load MIBs
	t.Log("Loading MIBs...")
	if loadErr := mibLoader.Load(); loadErr != nil {
		t.Fatalf("Failed to load MIBs: %v", loadErr)
	}

	// Verify profiles loaded
	profiles := mibLoader.GetProfileManager().ListProfiles()
	if len(profiles) == 0 {
		t.Fatal("Expected built-in profiles to be loaded")
	}
	t.Logf("Loaded %d profiles", len(profiles))

	// Create Prometheus exporter
	t.Log("Creating Prometheus exporter...")
	mibStorageInterface := mibLoader.GetStorage()
	mibStorage, ok := mibStorageInterface.(*storage.Storage)
	if !ok {
		t.Fatal("Failed to get MIB storage")
	}
	exporter, err := metrics.NewPrometheusExporter(
		prometheusURL+"/api/v1/write",
		100,           // smaller batch for faster testing
		2*time.Second, // faster flush for testing
		mibStorage,    // pass MIB storage for OID learning
	)
	if err != nil {
		t.Fatalf("Failed to create exporter: %v", err)
	}
	defer exporter.Close()

	// Create test configuration
	cfg := &config.Config{
		Global: config.GlobalConfig{
			DefaultPollInterval: 5, // 5 second poll interval for testing
			MaxWorkers:          10,
			SNMPTimeout:         5,
			SNMPRetries:         2,
		},
		Devices: []config.Device{
			{
				Name:         testDevice,
				Address:      net.JoinHostPort(snmpHost, snmpPort),
				SNMPVersion:  "2c",
				Community:    probeCommunity,
				Profile:      "default",
				PollInterval: 5,
				Enabled:      true,
				Tags: map[string]string{
					"environment": "test",
					"location":    "integration-test",
				},
			},
		},
		Logging: config.LoggingConfig{
			Level:  "info",
			Format: "text",
			Output: "stdout",
		},
	}

	// Create poller with exporter
	t.Log("Creating poller...")
	p := poller.NewWithExporter(cfg, mibLoader, exporter)

	// Start polling
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	t.Log("Starting poller...")
	if err := p.Start(ctx); err != nil {
		t.Fatalf("Failed to start poller: %v", err)
	}

	// Collect results for a few poll cycles. A cycle that reports an error is a
	// failure of the thing under test, not a diagnostic: the agent is known to
	// be answering, because the readiness gate above read a varbind out of it.
	t.Log("Collecting poll results...")
	const minResults = 2 // Wait for at least 2 poll cycles
	resultsReceived := 0
	timeout := time.After(15 * time.Second)

resultLoop:
	for resultsReceived < minResults {
		select {
		case result := <-p.Results():
			if result == nil {
				continue
			}
			if result.Error != nil {
				t.Errorf("Poll of %s failed: %v", result.DeviceName, result.Error)
				continue
			}
			if len(result.Results) == 0 {
				t.Errorf("Poll of %s returned no varbinds", result.DeviceName)
				continue
			}

			usable := usableVarbinds(result.Results)
			t.Logf("Poll result for %s: %d varbinds, %d carrying data",
				result.DeviceName, len(result.Results), len(usable))

			if len(usable) == 0 {
				t.Errorf("Poll of %s returned %d varbinds, none carrying data: %v",
					result.DeviceName, len(result.Results), varbindOIDs(result.Results))
				continue
			}

			assertSysUpTime(t, result.DeviceName, result.Results)
			assertInterfaceTableWalked(t, result.DeviceName, usable)
			resultsReceived++
		case <-timeout:
			break resultLoop
		}
	}

	if resultsReceived < minResults {
		t.Fatalf("Received %d successful poll results, want at least %d", resultsReceived, minResults)
	}

	t.Logf("Received %d poll results", resultsReceived)

	// Stop poller
	t.Log("Stopping poller...")
	if err := p.Stop(); err != nil {
		t.Errorf("Failed to stop poller: %v", err)
	}

	// Close exporter to flush remaining metrics
	t.Log("Closing exporter to flush metrics...")
	if err := exporter.Close(); err != nil {
		t.Errorf("Failed to close exporter: %v", err)
	}

	// Wait a bit for metrics to be written to Prometheus
	t.Log("Waiting for Prometheus to process metrics...")
	time.Sleep(3 * time.Second)

	// Verify metrics in Prometheus
	t.Log("Verifying metrics in Prometheus...")
	if err := verifyPrometheusMetrics(t.Context(), prometheusURL); err != nil {
		t.Errorf("Failed to verify Prometheus metrics: %v", err)
	} else {
		t.Log("Successfully verified metrics in Prometheus")
	}
}

// prometheusTarget is the Prometheus base URL under test.
func prometheusTarget() string {
	if target := os.Getenv("PROMETHEUS_URL"); target != "" {
		return target
	}

	return "http://localhost:9090"
}

// snmpTarget is the SNMP agent under test.
func snmpTarget() (host, port string) {
	host = os.Getenv("SNMP_HOST")
	if host == "" {
		host = "localhost"
	}

	port = os.Getenv("SNMP_PORT")
	if port == "" {
		port = "1161"
	}

	return host, port
}

func waitForPrometheus(ctx context.Context, url string, timeout time.Duration) error {
	start := time.Now()
	deadline := start.Add(timeout)
	client := &http.Client{Timeout: 5 * time.Second}

	attempts := 0
	var lastErr error
	for {
		attempts++
		lastErr = probePrometheus(ctx, client, url)
		if lastErr == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return &PrometheusNotReadyError{
				URL:      url,
				Attempts: attempts,
				Elapsed:  time.Since(start),
				Err:      lastErr,
			}
		}
		time.Sleep(probeInterval)
	}
}

// probePrometheus performs one health check.
func probePrometheus(ctx context.Context, client *http.Client, url string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"/-/healthy", nil)
	if err != nil {
		return &PrometheusQueryError{Query: "/-/healthy", Stage: "build request", Err: err}
	}

	resp, err := client.Do(req)
	if err != nil {
		return &PrometheusQueryError{Query: "/-/healthy", Stage: "request", Err: err}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return &PrometheusQueryError{Query: "/-/healthy", Stage: "read body", Err: err}
	}

	if resp.StatusCode != http.StatusOK {
		return &PrometheusStatusError{Query: "/-/healthy", StatusCode: resp.StatusCode, Body: string(body)}
	}

	return nil
}

// waitForSNMP blocks until the SNMP agent at host:port answers a GET of
// sysUpTime.0 with a usable value, or until the timeout expires.
//
// It uses the production SNMP client over a real UDP socket, so what it proves
// is exactly what the poller needs to be true. Sleeping for a fixed interval
// and returning nil, which is what this used to do, makes every later failure
// look like a fault in the code under test rather than an environment that
// never came up.
func waitForSNMP(host, port string, timeout time.Duration) (*snmp.Result, error) {
	target := net.JoinHostPort(host, port)
	device := &config.Device{
		Name:        "snmp-readiness-probe",
		Address:     target,
		SNMPVersion: "2c",
		Community:   probeCommunity,
		Enabled:     true,
	}

	start := time.Now()
	deadline := start.Add(timeout)

	attempts := 0
	var lastErr error
	for {
		attempts++
		result, err := probeSNMP(device, target)
		if err == nil {
			return result, nil
		}
		lastErr = err
		if time.Now().After(deadline) {
			return nil, &SNMPUnreachableError{
				Target:   target,
				OID:      sysUpTimeOID,
				Attempts: attempts,
				Elapsed:  time.Since(start),
				Err:      lastErr,
			}
		}
		time.Sleep(probeInterval)
	}
}

// probeSNMP performs one connect-and-GET against the target, returning the
// varbind only when it carries a value the poller could actually export.
func probeSNMP(device *config.Device, target string) (*snmp.Result, error) {
	client, newErr := snmp.NewClient(device, probeTimeout, probeRetries)
	if newErr != nil {
		return nil, &SNMPProbeError{Target: target, OID: sysUpTimeOID, Stage: "new client", Err: newErr}
	}
	defer client.Close()

	if connectErr := client.Connect(); connectErr != nil {
		return nil, &SNMPProbeError{Target: target, OID: sysUpTimeOID, Stage: "connect", Err: connectErr}
	}

	result, getErr := client.Get(sysUpTimeOID)
	if getErr != nil {
		return nil, &SNMPProbeError{Target: target, OID: sysUpTimeOID, Stage: "get", Err: getErr}
	}

	if !usableVarbind(result) {
		return nil, &SNMPUnansweredError{
			Target: target,
			OID:    sysUpTimeOID,
			Type:   result.TypeString(),
			Value:  result.Value,
		}
	}

	return result, nil
}

// usableVarbind reports whether a varbind carries a value. An agent answers a
// GET for an object it does not implement with noSuchObject or noSuchInstance
// rather than an error, so the Go error alone does not distinguish an agent
// serving data from one serving nothing.
func usableVarbind(result *snmp.Result) bool {
	if result == nil || result.Value == nil {
		return false
	}

	switch result.Type {
	case gosnmp.NoSuchObject, gosnmp.NoSuchInstance, gosnmp.EndOfMibView, gosnmp.Null:
		return false
	default:
		return true
	}
}

// usableVarbinds returns the varbinds in a poll result that carry a value.
//
// A poll result is not required to be entirely usable: the profile selects
// objects from the whole MIB and the simulator implements only some of them, so
// noSuchInstance for an object the fixture omits is the agent behaving
// correctly. What matters is how many varbinds carry data and which ones.
func usableVarbinds(results []*snmp.Result) []*snmp.Result {
	usable := make([]*snmp.Result, 0, len(results))
	for _, result := range results {
		if usableVarbind(result) {
			usable = append(usable, result)
		}
	}

	return usable
}

// findVarbind returns the varbind for an OID, if the result set has one.
func findVarbind(results []*snmp.Result, oid string) (*snmp.Result, bool) {
	for _, result := range results {
		if canonicalOID(result.OID) == canonicalOID(oid) {
			return result, true
		}
	}

	return nil, false
}

// assertSysUpTime fails the test unless the result set carries sysUpTime.0 as a
// TimeTicks value that has actually advanced. Every SNMP agent implements it,
// so its absence means the poll reached nothing, and counting varbinds alone
// would pass on a result made entirely of noSuchInstance.
func assertSysUpTime(t *testing.T, device string, results []*snmp.Result) {
	t.Helper()

	uptime, found := findVarbind(results, sysUpTimeOID)
	if !found {
		t.Errorf("device %s returned %d varbinds but none for sysUpTime.0 (%s): %v",
			device, len(results), sysUpTimeOID, varbindOIDs(results))
		return
	}

	if !usableVarbind(uptime) {
		t.Errorf("device %s returned sysUpTime.0 as %s with value %v, which carries no data",
			device, uptime.TypeString(), uptime.Value)
		return
	}

	if uptime.Type != gosnmp.TimeTicks {
		t.Errorf("device %s returned sysUpTime.0 as %s, want TimeTicks", device, uptime.TypeString())
		return
	}

	ticks, err := uptime.GetUint64Value()
	if err != nil {
		t.Errorf("device %s returned an unreadable sysUpTime.0 value %v: %v", device, uptime.Value, err)
		return
	}
	if ticks == 0 {
		t.Errorf("device %s reported sysUpTime.0 of 0 ticks; the agent has never been up", device)
	}
}

// assertInterfaceTableWalked fails the test unless the poll brought back at
// least one ifTable column value. The scalars alone would pass on a poll that
// never walked a table, which is the larger half of what the profile selects.
func assertInterfaceTableWalked(t *testing.T, device string, usable []*snmp.Result) {
	t.Helper()

	for _, result := range usable {
		if strings.HasPrefix(canonicalOID(result.OID), canonicalOID(ifEntryOID)+".") {
			return
		}
	}

	t.Errorf("device %s returned no ifTable (%s) values among %d varbinds carrying data: %v",
		device, ifEntryOID, len(usable), varbindOIDs(usable))
}

// varbindOIDs lists the OIDs in a result set, for failure messages.
func varbindOIDs(results []*snmp.Result) []string {
	oids := make([]string, 0, len(results))
	for _, result := range results {
		oids = append(oids, result.OID)
	}

	return oids
}

// canonicalOID strips the leading dot so OIDs written either way compare equal.
func canonicalOID(oid string) string {
	return strings.TrimPrefix(oid, ".")
}

type prometheusResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string `json:"metric"`
			Value  []interface{}     `json:"value"`
		} `json:"result"`
	} `json:"data"`
}

func verifyPrometheusMetrics(ctx context.Context, prometheusURL string) error {
	client := &http.Client{Timeout: 10 * time.Second}

	// Query for metrics with device label
	query := fmt.Sprintf(`{device=%q}`, testDevice)

	// Retry a few times since metrics might take time to be queryable
	const attempts = 5
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		promResp, err := queryPrometheus(ctx, client, prometheusURL, query)
		if err != nil {
			lastErr = err
			time.Sleep(probeInterval)
			continue
		}

		if len(promResp.Data.Result) == 0 {
			lastErr = &MetricsNotFoundError{
				Query:    query,
				Attempts: attempt,
				Status:   promResp.Status,
			}
			time.Sleep(2 * probeInterval)
			continue
		}

		return checkSeries(query, promResp)
	}

	return lastErr
}

// queryPrometheus runs one instant query and decodes the response.
func queryPrometheus(ctx context.Context, client *http.Client, prometheusURL, query string) (*prometheusResponse, error) {
	endpoint := prometheusURL + "/api/v1/query?" + url.Values{"query": {query}}.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, &PrometheusQueryError{Query: query, Stage: "build request", Err: err}
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, &PrometheusQueryError{Query: query, Stage: "request", Err: err}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &PrometheusQueryError{Query: query, Stage: "read body", Err: err}
	}

	if resp.StatusCode != http.StatusOK {
		return nil, &PrometheusStatusError{Query: query, StatusCode: resp.StatusCode, Body: string(body)}
	}

	var promResp prometheusResponse
	if err := json.Unmarshal(body, &promResp); err != nil {
		return nil, &PrometheusQueryError{Query: query, Stage: "decode " + string(body), Err: err}
	}

	if promResp.Status != "success" {
		return nil, &PrometheusResponseError{Query: query, Status: promResp.Status, Body: string(body)}
	}

	return &promResp, nil
}

// checkSeries verifies the series Prometheus returned are actually readable:
// named, attributed to the device under test, and carrying a numeric sample.
// The count alone says nothing about whether the exporter wrote anything a
// dashboard could plot.
func checkSeries(query string, promResp *prometheusResponse) error {
	for _, series := range promResp.Data.Result {
		if series.Metric["__name__"] == "" {
			return &MetricShapeError{Query: query, Metric: series.Metric, Reason: "no metric name"}
		}
		if series.Metric["device"] != testDevice {
			return &MetricShapeError{
				Query:  query,
				Metric: series.Metric,
				Reason: "device label is " + series.Metric["device"] + ", want " + testDevice,
			}
		}
		if len(series.Value) != 2 {
			return &MetricShapeError{Query: query, Metric: series.Metric, Reason: "sample is not a [timestamp, value] pair"}
		}

		raw, ok := series.Value[1].(string)
		if !ok {
			return &MetricShapeError{Query: query, Metric: series.Metric, Reason: "sample value is not a string"}
		}
		if _, err := strconv.ParseFloat(raw, 64); err != nil {
			return &MetricShapeError{Query: query, Metric: series.Metric, Reason: "sample value " + raw + " is not a number"}
		}
	}

	return nil
}

// TestSNMPConnectivity proves the SNMP agent under test is reachable and
// serving data, twice over: once directly through the production SNMP client,
// and once through the poller that the rest of the suite depends on.
func TestSNMPConnectivity(t *testing.T) {
	snmpHost, snmpPort := snmpTarget()
	target := net.JoinHostPort(snmpHost, snmpPort)

	probe, err := waitForSNMP(snmpHost, snmpPort, serviceDeadline)
	if err != nil {
		t.Fatalf("SNMP agent not reachable: %v", err)
	}

	if got, want := canonicalOID(probe.OID), canonicalOID(sysUpTimeOID); got != want {
		t.Errorf("probe of %s answered for OID %s, want %s", target, got, want)
	}
	if probe.Type != gosnmp.TimeTicks {
		t.Errorf("probe of %s returned sysUpTime.0 as %s, want TimeTicks", target, probe.TypeString())
	}
	t.Logf("Direct SNMP probe of %s: %s = %v (%s)", target, probe.OID, probe.Value, probe.TypeString())

	cfg := &config.Config{
		Global: config.GlobalConfig{
			DefaultPollInterval: 30,
			SNMPTimeout:         5,
			SNMPRetries:         2,
		},
		Devices: []config.Device{
			{
				Name:         testDevice,
				Address:      target,
				SNMPVersion:  "2c",
				Community:    probeCommunity,
				Profile:      "default",
				PollInterval: 30,
				Enabled:      true,
			},
		},
	}

	p := poller.New(cfg, nil)

	ctx, cancel := context.WithTimeout(t.Context(), serviceDeadline)
	defer cancel()

	if err := p.Start(ctx); err != nil {
		t.Fatalf("failed to start poller for %s: %v", target, err)
	}
	defer func() {
		if err := p.Stop(); err != nil {
			t.Errorf("failed to stop poller: %v", err)
		}
	}()

	// The first poll runs as soon as the worker starts, so one interval plus the
	// SNMP timeout budget is ample.
	result := awaitPollResult(t, p, serviceDeadline)
	if result.Error != nil {
		t.Fatalf("poll of %s failed: %v", target, result.Error)
	}
	if len(result.Results) == 0 {
		t.Fatalf("poll of %s returned no varbinds", target)
	}

	// With no MIB loader the poller falls back to sysDescr.0, sysUpTime.0 and
	// sysName.0, all three of which the fixture serves, so every varbind of this
	// poll is expected to carry data.
	if usable := usableVarbinds(result.Results); len(usable) != len(result.Results) {
		t.Errorf("poll of %s returned %d varbinds but only %d carry data: %v",
			target, len(result.Results), len(usable), varbindOIDs(result.Results))
	}
	assertSysUpTime(t, result.DeviceName, result.Results)

	t.Logf("Poller collected %d varbinds from %s: %v", len(result.Results), target, varbindOIDs(result.Results))
}

// awaitPollResult returns the first poll result the poller publishes, failing
// the test if none arrives before the timeout.
func awaitPollResult(t *testing.T, p *poller.Poller, timeout time.Duration) *poller.PollResult {
	t.Helper()

	deadline := time.After(timeout)
	for {
		select {
		case result := <-p.Results():
			if result == nil {
				continue
			}
			return result
		case <-deadline:
			t.Fatalf("no poll result published within %s", timeout)
			return nil
		}
	}
}

// TestPrometheusConnectivity proves Prometheus is both healthy and able to
// answer queries. The health endpoint alone does not cover the query API that
// every metric assertion in this suite goes through.
func TestPrometheusConnectivity(t *testing.T) {
	prometheusURL := prometheusTarget()

	if err := waitForPrometheus(t.Context(), prometheusURL, serviceDeadline); err != nil {
		t.Fatalf("Prometheus not healthy: %v", err)
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := queryPrometheus(t.Context(), client, prometheusURL, "vector(1)")
	if err != nil {
		t.Fatalf("Prometheus query API unusable: %v", err)
	}

	if len(resp.Data.Result) != 1 {
		t.Fatalf("query vector(1) returned %d series, want 1", len(resp.Data.Result))
	}
	if got := resp.Data.ResultType; got != "vector" {
		t.Errorf("query vector(1) returned result type %q, want %q", got, "vector")
	}

	raw, ok := resp.Data.Result[0].Value[1].(string)
	if !ok {
		t.Fatalf("query vector(1) returned a non-string sample value %v", resp.Data.Result[0].Value[1])
	}
	if raw != "1" {
		t.Errorf("query vector(1) returned %q, want %q", raw, "1")
	}

	t.Logf("Successfully queried Prometheus at %s", prometheusURL)
}
