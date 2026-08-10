package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"
)

// promStub serves canned Prometheus API responses and records the last query
// string it was asked for, so a test can check what the client sent as well as
// what it made of the answer.
type promStub struct {
	body       string
	status     int
	lastPath   string
	lastParams string
}

func (p *promStub) start(t *testing.T) *PrometheusClient {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.lastPath = r.URL.Path
		p.lastParams = r.URL.RawQuery
		if p.status != 0 {
			w.WriteHeader(p.status)
		}
		_, _ = w.Write([]byte(p.body))
	}))
	t.Cleanup(srv.Close)

	return NewPrometheusClient(srv.URL)
}

func TestPrometheusQuery(t *testing.T) {
	stub := &promStub{body: `{
		"status":"success",
		"data":{"resultType":"vector","result":[
			{"metric":{"__name__":"snmp_if_in","ifindex":"1"},"value":[1700000000,"42.5"]},
			{"metric":{"__name__":"snmp_if_in","ifindex":"2"},"value":[1700000000,"7"]}
		]}
	}`}
	client := stub.start(t)

	metrics, err := client.Query(context.Background(), `snmp_if_in{device="bastion"}`)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(metrics) != 2 {
		t.Fatalf("metrics = %d, want 2", len(metrics))
	}
	if metrics[0].Value != 42.5 || metrics[0].Timestamp != 1700000000 {
		t.Errorf("first metric = %+v, want value 42.5 at 1700000000", metrics[0])
	}
	if metrics[0].Metric["ifindex"] != "1" {
		t.Errorf("labels = %v, want ifindex preserved", metrics[0].Metric)
	}
	if stub.lastPath != "/api/v1/query" {
		t.Errorf("path = %q, want /api/v1/query", stub.lastPath)
	}
	if !strings.Contains(stub.lastParams, "snmp_if_in") {
		t.Errorf("params = %q, want the query", stub.lastParams)
	}
}

// TestPrometheusQuerySkipsUnusableSamples keeps one malformed sample from
// discarding the whole response.
func TestPrometheusQuerySkipsUnusableSamples(t *testing.T) {
	stub := &promStub{body: `{
		"status":"success",
		"data":{"resultType":"vector","result":[
			{"metric":{"a":"1"},"value":[1700000000]},
			{"metric":{"a":"2"},"value":["not-a-timestamp","1"]},
			{"metric":{"a":"3"},"value":[1700000000,5]},
			{"metric":{"a":"4"},"value":[1700000000,"not-a-number"]},
			{"metric":{"a":"5"},"value":[1700000000,"9"]}
		]}
	}`}
	client := stub.start(t)

	metrics, err := client.Query(context.Background(), "x")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(metrics) != 1 || metrics[0].Value != 9 {
		t.Errorf("metrics = %+v, want only the one usable sample", metrics)
	}
}

func TestPrometheusQueryRange(t *testing.T) {
	stub := &promStub{body: `{
		"status":"success",
		"data":{"resultType":"matrix","result":[
			{"metric":{"__name__":"snmp_if_in"},"values":[[1700000000,"1"],[1700000030,"2"]]},
			{"metric":{"__name__":"snmp_if_out"},"value":[1700000000,"3"]}
		]}
	}`}
	client := stub.start(t)

	end := time.Unix(1700000060, 0)
	results, err := client.QueryRange(context.Background(), "snmp_if_in", end.Add(-time.Minute), end, 30*time.Second)
	if err != nil {
		t.Fatalf("QueryRange: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %d, want 2", len(results))
	}

	first, ok := results[0].(map[string]interface{})
	if !ok {
		t.Fatalf("result 0 is %T, want a map", results[0])
	}
	if _, has := first["values"]; !has {
		t.Errorf("range result = %v, want a values series", first)
	}

	second, ok := results[1].(map[string]interface{})
	if !ok {
		t.Fatalf("result 1 is %T, want a map", results[1])
	}
	if _, has := second["value"]; !has {
		t.Errorf("instant result = %v, want a single value", second)
	}

	if !strings.Contains(stub.lastParams, "step=30s") {
		t.Errorf("params = %q, want step=30s", stub.lastParams)
	}
}

func TestPrometheusQueryLabelValues(t *testing.T) {
	stub := &promStub{body: `{"status":"success","data":["1","2","3"]}`}
	client := stub.start(t)

	values, err := client.QueryLabelValues(context.Background(), "ifindex", map[string]string{"device": "bastion"})
	if err != nil {
		t.Fatalf("QueryLabelValues: %v", err)
	}
	if len(values) != 3 || values[0] != "1" {
		t.Errorf("values = %v, want [1 2 3]", values)
	}
	if stub.lastPath != "/api/v1/label/ifindex/values" {
		t.Errorf("path = %q, want the label values endpoint", stub.lastPath)
	}
	if !strings.Contains(stub.lastParams, "match") {
		t.Errorf("params = %q, want the matcher", stub.lastParams)
	}

	// With no matchers the endpoint is asked for everything.
	if _, err := client.QueryLabelValues(context.Background(), "ifindex", nil); err != nil {
		t.Fatalf("QueryLabelValues with no matchers: %v", err)
	}
	if stub.lastParams != "" {
		t.Errorf("params = %q, want none when no matchers were given", stub.lastParams)
	}
}

func TestPrometheusQuerySeries(t *testing.T) {
	stub := &promStub{body: `{
		"status":"success",
		"data":[{"metric":{"__name__":"snmp_ifname_info","ifindex":"1"}}]
	}`}
	client := stub.start(t)

	end := time.Unix(1700000060, 0)

	series, err := client.QuerySeries(context.Background(), map[string]string{"device": "bastion"}, end.Add(-time.Hour), end)
	if err != nil {
		t.Fatalf("QuerySeries: %v", err)
	}
	if len(series) != 1 || series[0]["ifindex"] != "1" {
		t.Errorf("series = %v, want the one info metric", series)
	}
	if !strings.Contains(stub.lastParams, "device") {
		t.Errorf("params = %q, want the matcher", stub.lastParams)
	}

	// Without matchers the client still scopes the query to info metrics,
	// which the series endpoint requires.
	if _, err := client.QuerySeries(context.Background(), nil, end.Add(-time.Hour), end); err != nil {
		t.Fatalf("QuerySeries with no matchers: %v", err)
	}
	if !strings.Contains(stub.lastParams, "_info") {
		t.Errorf("params = %q, want the default info-metric selector", stub.lastParams)
	}
}

func TestPrometheusClientRejectsBadResponses(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"a reported query error", `{"status":"error","error":"parse error","errorType":"bad_data"}`, "parse error"},
		{"a body that is not JSON", `<html>oops</html>`, "unmarshaling"},
	}

	end := time.Unix(1700000060, 0)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := (&promStub{body: tt.body}).start(t)
			ctx := context.Background()

			calls := map[string]func() error{
				"Query":            func() error { _, err := client.Query(ctx, "x"); return err },
				"QueryRange":       func() error { _, err := client.QueryRange(ctx, "x", end, end, time.Second); return err },
				"QueryLabelValues": func() error { _, err := client.QueryLabelValues(ctx, "l", nil); return err },
				"QuerySeries":      func() error { _, err := client.QuerySeries(ctx, nil, end, end); return err },
			}

			for name, call := range calls {
				err := call()
				if err == nil {
					t.Errorf("%s accepted a bad response", name)
					continue
				}
				if !strings.Contains(err.Error(), tt.want) {
					t.Errorf("%s error = %q, want it to mention %q", name, err, tt.want)
				}
			}
		})
	}
}

// TestPrometheusClientReportsAnUnreachableServer keeps a down Prometheus from
// looking like an empty result set.
func TestPrometheusClientReportsAnUnreachableServer(t *testing.T) {
	// A server that is closed immediately, so the port refuses connections.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()

	client := NewPrometheusClient(url)
	if _, err := client.Query(context.Background(), "x"); err == nil {
		t.Error("Query reported success against a server that is not listening")
	}
}

func TestJoinStrings(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		sep  string
		want string
	}{
		{"several parts", []string{"a", "b", "c"}, ",", "a,b,c"},
		{"one part carries no separator", []string{"a"}, ",", "a"},
		{"nothing to join", nil, ",", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := joinStrings(tt.in, tt.sep); got != tt.want {
				t.Errorf("joinStrings(%v) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestFormatSNMPValue(t *testing.T) {
	tests := []struct {
		name string
		pdu  gosnmp.SnmpPDU
		want string
	}{
		{"octet string", gosnmp.SnmpPDU{Type: gosnmp.OctetString, Value: []byte("bastion")}, "bastion"},
		{"integer", gosnmp.SnmpPDU{Type: gosnmp.Integer, Value: -3}, "-3"},
		{"counter32", gosnmp.SnmpPDU{Type: gosnmp.Counter32, Value: uint(7)}, "7"},
		{"counter64", gosnmp.SnmpPDU{Type: gosnmp.Counter64, Value: uint64(1 << 40)}, "1099511627776"},
		{"gauge32", gosnmp.SnmpPDU{Type: gosnmp.Gauge32, Value: uint(11)}, "11"},
		{"timeticks", gosnmp.SnmpPDU{Type: gosnmp.TimeTicks, Value: uint32(8640000 + 360000 + 6000 + 100)}, "1 days, 01:01:01"},
		{"object identifier", gosnmp.SnmpPDU{Type: gosnmp.ObjectIdentifier, Value: "1.3.6.1"}, "1.3.6.1"},
		{"ip address", gosnmp.SnmpPDU{Type: gosnmp.IPAddress, Value: "192.0.2.1"}, "192.0.2.1"},
		{"no such object", gosnmp.SnmpPDU{Type: gosnmp.NoSuchObject}, "No such object/instance"},
		{"no such instance", gosnmp.SnmpPDU{Type: gosnmp.NoSuchInstance}, "No such object/instance"},
		{"null", gosnmp.SnmpPDU{Type: gosnmp.Null}, "null"},
		{"a type with no special handling", gosnmp.SnmpPDU{Type: gosnmp.Boolean, Value: true}, "true"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatSNMPValue(tt.pdu); got != tt.want {
				t.Errorf("formatSNMPValue(%v) = %q, want %q", tt.pdu.Type, got, tt.want)
			}
		})
	}
}

func TestGetSNMPTypeName(t *testing.T) {
	tests := []struct {
		in   gosnmp.Asn1BER
		want string
	}{
		{gosnmp.OctetString, "OctetString"},
		{gosnmp.Integer, "Integer"},
		{gosnmp.Counter32, "Counter32"},
		{gosnmp.Counter64, "Counter64"},
		{gosnmp.Gauge32, "Gauge32"},
		{gosnmp.TimeTicks, "TimeTicks"},
		{gosnmp.ObjectIdentifier, "OID"},
		{gosnmp.IPAddress, "IPAddress"},
		{gosnmp.NoSuchObject, "NoSuchObject"},
		{gosnmp.NoSuchInstance, "NoSuchInstance"},
		{gosnmp.Null, "Null"},
	}

	for _, tt := range tests {
		if got := getSNMPTypeName(tt.in); got != tt.want {
			t.Errorf("getSNMPTypeName(%v) = %q, want %q", tt.in, got, tt.want)
		}
	}

	// An unmapped type still names itself rather than reporting nothing.
	if got := getSNMPTypeName(gosnmp.Boolean); !strings.HasPrefix(got, "Type(") {
		t.Errorf("getSNMPTypeName(Boolean) = %q, want a Type(n) fallback", got)
	}
}
