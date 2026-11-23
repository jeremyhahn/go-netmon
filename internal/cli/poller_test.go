package cli

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-netmon/pkg/client"
)

// testPollerConfig is the configuration document the fake API serves.
func testPollerConfig() client.PollerConfig {
	return client.PollerConfig{
		Version: 1786204013,
		MIBModules: []client.PollerMIB{
			{Name: "IF-MIB", Enabled: true},
			{Name: "CISCO-BGP4-MIB", Enabled: true},
			{Name: "SNMPv2-MIB", Enabled: true},
		},
		Devices: []client.PollerDevice{
			{
				Name:         "core-01",
				Address:      "198.51.100.252",
				SNMPVersion:  "2c",
				Community:    "public",
				PollInterval: 30,
				Profile:      "router",
				Modules:      []string{"IF-MIB", "SNMPv2-MIB"},
				Enabled:      true,
			},
			{
				Name:        "edge-sw-02",
				Address:     "198.51.100.10",
				SNMPVersion: "3",
				Enabled:     false,
			},
		},
	}
}

func TestPollerConfigRendersDevicesAndModules(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/poller/config", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, testPollerConfig())
	})

	env, out := newFakeEnv(t, mux)
	if err := runCommand(t, newPollerCommand(env), "config"); err != nil {
		t.Fatalf("poller config: %v", err)
	}

	got := out.String()
	for _, want := range []string{
		"Version:", "1786204013",
		"MIB modules:", "IF-MIB", "CISCO-BGP4-MIB",
		"NAME", "ADDRESS", "VERSION", "INTERVAL", "PROFILE", "MODULES", "ENABLED",
		"core-01", "198.51.100.252", "30s", "router",
		"edge-sw-02", "no",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("config output missing %q:\n%s", want, got)
		}
	}

	// MIB modules are listed alphabetically, not in server order.
	if strings.Index(got, "CISCO-BGP4-MIB") > strings.Index(got, "IF-MIB") {
		t.Errorf("MIB modules are not sorted:\n%s", got)
	}
	// A device without a poll interval or profile shows a placeholder rather
	// than a blank column.
	if !strings.Contains(got, "-") {
		t.Errorf("expected placeholders for the unset columns:\n%s", got)
	}
}

func TestPollerConfigEmptyAndServerError(t *testing.T) {
	t.Run("nothing configured", func(t *testing.T) {
		mux := http.NewServeMux()
		mux.HandleFunc("/api/poller/config", func(w http.ResponseWriter, _ *http.Request) {
			respondJSON(t, w, client.PollerConfig{})
		})

		env, out := newFakeEnv(t, mux)
		if err := runCommand(t, newPollerCommand(env), "config"); err != nil {
			t.Fatalf("poller config: %v", err)
		}
		if strings.Count(out.String(), "(none)") != 2 {
			t.Errorf("expected empty module and device sections:\n%s", out)
		}
	})

	t.Run("server error", func(t *testing.T) {
		mux := http.NewServeMux()
		mux.HandleFunc("/api/poller/config", func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "config unavailable", http.StatusInternalServerError)
		})

		env, _ := newFakeEnv(t, mux)
		err := runCommand(t, newPollerCommand(env), "config")
		if err == nil {
			t.Fatal("expected an error from a 500 response")
		}

		var apiErr *client.APIError
		if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusInternalServerError {
			t.Fatalf("got %v, want a 500 APIError", err)
		}
	})
}

func TestPollerConfigJSON(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/poller/config", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, testPollerConfig())
	})

	env, out := newFakeEnv(t, mux)
	env.Opts.JSON = true
	if err := runCommand(t, newPollerCommand(env), "config"); err != nil {
		t.Fatalf("poller config: %v", err)
	}

	var decoded client.PollerConfig
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("output is not a poller config: %v\n%s", err, out)
	}
	// JSON keeps the server's module order for scripts that depend on it.
	if len(decoded.MIBModules) != 3 || decoded.MIBModules[0].Name != "IF-MIB" {
		t.Errorf("modules = %v", decoded.MIBModules)
	}
}

// learnedTablesDocument is the envelope the OID learner returns through the
// management API's proxy.
func learnedTablesDocument() map[string]any {
	lastSeen := time.Now().Add(-45 * time.Second).Format(time.RFC3339Nano)
	return map[string]any{
		"tables": map[string]any{
			"1.3.6.1.2.1.2.2.1": map[string]any{
				"entry_oid":    "1.3.6.1.2.1.2.2.1",
				"table_name":   "ifTable",
				"index_names":  []string{"ifIndex"},
				"column_oids":  []string{"1.3.6.1.2.1.2.2.1.10", "1.3.6.1.2.1.2.2.1.16"},
				"sample_count": 96,
				"last_seen":    lastSeen,
				"source":       "mib",
			},
			"1.3.6.1.4.1.9.9.187.1.2.5.1": map[string]any{
				"entry_oid":    "1.3.6.1.4.1.9.9.187.1.2.5.1",
				"column_oids":  []string{"1.3.6.1.4.1.9.9.187.1.2.5.1.3"},
				"sample_count": 12,
				"last_seen":    lastSeen,
				"source":       "learned",
			},
		},
		"stats": map[string]any{
			"total_tables": 2,
			"learned_only": 1,
		},
	}
}

func TestPollerTablesRendersLearnedTables(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/learned-tables", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, learnedTablesDocument())
	})

	env, out := newFakeEnv(t, mux)
	if err := runCommand(t, newPollerCommand(env), "tables"); err != nil {
		t.Fatalf("poller tables: %v", err)
	}

	got := out.String()
	for _, want := range []string{
		"TABLE", "ENTRY OID", "INDEXES", "COLUMNS", "SAMPLES", "SOURCE", "LAST SEEN",
		"ifTable", "ifIndex", "1.3.6.1.2.1.2.2.1", "96", "mib", "ago",
		// A table the learner could not name falls back to its entry OID.
		"1.3.6.1.4.1.9.9.187.1.2.5.1", "learned",
		"Stats:", "total_tables", "2",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("tables output missing %q:\n%s", want, got)
		}
	}
}

func TestPollerTablesEmpty(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/learned-tables", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, map[string]any{
			"tables": map[string]any{},
			"stats":  map[string]any{"message": "OID learner not available"},
		})
	})

	env, out := newFakeEnv(t, mux)
	if err := runCommand(t, newPollerCommand(env), "tables"); err != nil {
		t.Fatalf("poller tables: %v", err)
	}

	got := out.String()
	if !strings.Contains(got, "No learned tables") {
		t.Errorf("expected an empty-result notice:\n%s", got)
	}
	if !strings.Contains(got, "OID learner not available") {
		t.Errorf("stats explaining the emptiness were dropped:\n%s", got)
	}
}

func TestPollerTablesUnexpectedShape(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/learned-tables", func(w http.ResponseWriter, _ *http.Request) {
		// A poller that answers with a different document must produce a clear
		// error rather than a panic or an empty table.
		respondJSON(t, w, map[string]any{"tables": "not-a-table-map"})
	})

	env, _ := newFakeEnv(t, mux)
	err := runCommand(t, newPollerCommand(env), "tables")
	if err == nil {
		t.Fatal("expected an error for an unreadable learned-tables document")
	}

	var tablesErr *LearnedTablesError
	if !errors.As(err, &tablesErr) {
		t.Fatalf("got %T, want *LearnedTablesError", err)
	}
	if !strings.Contains(err.Error(), "learned-tables") {
		t.Errorf("error does not name the payload: %v", err)
	}
}

func TestPollerTablesServerError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/learned-tables", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "poller unreachable", http.StatusBadGateway)
	})

	env, _ := newFakeEnv(t, mux)
	err := runCommand(t, newPollerCommand(env), "tables")
	if err == nil {
		t.Fatal("expected an error from a 502 response")
	}

	var apiErr *client.APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusBadGateway {
		t.Fatalf("got %v, want a 502 APIError", err)
	}
}

// TestPollerTablesJSONCarriesCounts checks the --json document: the learner's
// tables as a searchable list with the counts describing it, and the stats it
// reports alongside them.
func TestPollerTablesJSONCarriesCounts(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/learned-tables", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, learnedTablesDocument())
	})

	env, out := newFakeEnv(t, mux)
	env.Opts.JSON = true
	if err := runCommand(t, newPollerCommand(env), "tables"); err != nil {
		t.Fatalf("poller tables: %v", err)
	}

	var report learnedTablesReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("output is not a learned-tables document: %v\n%s", err, out)
	}
	if len(report.Items) == 0 {
		t.Errorf("document lost its tables: %s", out)
	}
	if report.Total != len(report.Items) || report.Truncated {
		t.Errorf("counts = %+v, want a complete listing", report.listCounts)
	}
	if len(report.Stats) == 0 {
		t.Errorf("document must keep the learner's stats: %s", out)
	}
}

func TestFormatInterval(t *testing.T) {
	cases := []struct {
		seconds int
		want    string
	}{
		{seconds: 30, want: "30s"},
		{seconds: 300, want: "5m0s"},
		{seconds: 0, want: "-"},
		{seconds: -1, want: "-"},
	}

	for _, c := range cases {
		if got := formatInterval(c.seconds); got != c.want {
			t.Errorf("formatInterval(%d) = %q, want %q", c.seconds, got, c.want)
		}
	}
}

func TestDecodeLearnedTablesRejectsBadPayload(t *testing.T) {
	decoded, err := decodeLearnedTables(learnedTablesDocument())
	if err != nil {
		t.Fatalf("decodeLearnedTables: %v", err)
	}
	if len(decoded.Tables) != 2 || decoded.Tables["1.3.6.1.2.1.2.2.1"].TableName != "ifTable" {
		t.Errorf("decoded %+v", decoded.Tables)
	}

	if _, err := decodeLearnedTables(map[string]any{"tables": 42}); err == nil {
		t.Error("expected an error for a numeric tables field")
	}
}
