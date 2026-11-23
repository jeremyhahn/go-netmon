package mib

import (
	"strings"
	"testing"
	"time"
)

// TestOIDMetadataIsPollable covers the gate that decides what the poller asks a
// device for. Getting it wrong in either direction is expensive: a false
// negative silently drops a metric, and a false positive puts a not-accessible
// or obsolete object into every request, which is how a batch grows past what
// an agent will answer.
func TestOIDMetadataIsPollable(t *testing.T) {
	tests := []struct {
		name string
		meta OIDMetadata
		want bool
	}{
		{
			name: "read-only current column",
			meta: OIDMetadata{
				OID:    "1.3.6.1.2.1.2.2.1.10",
				Access: "read-only", Status: "current", IsColumn: true,
			},
			want: true,
		},
		{
			name: "read-write current scalar",
			meta: OIDMetadata{
				OID:    "1.3.6.1.2.1.1.4",
				Access: "read-write", Status: "current", IsScalar: true,
			},
			want: true,
		},
		{
			name: "not-accessible",
			meta: OIDMetadata{Access: "not-accessible", Status: "current"},
			want: false,
		},
		{
			name: "accessible-for-notify",
			meta: OIDMetadata{Access: "accessible-for-notify", Status: "current"},
			want: false,
		},
		{
			name: "deprecated",
			meta: OIDMetadata{Access: "read-only", Status: "deprecated"},
			want: false,
		},
		{
			name: "obsolete",
			meta: OIDMetadata{Access: "read-only", Status: "obsolete"},
			want: false,
		},
		{
			name: "table node itself",
			meta: OIDMetadata{Access: "read-only", Status: "current", IsTable: true},
			want: false,
		},
		{
			name: "entry node itself",
			meta: OIDMetadata{Access: "read-only", Status: "current", IsTableEntry: true},
			want: false,
		},
		{
			name: "zero value carries no access or status",
			meta: OIDMetadata{},
			want: false,
		},
		{
			// A module whose IMPORTS did not resolve parses into objects with
			// the right access and status and no OID at all. Counting them as
			// pollable told the series estimate, the object plan and the poller
			// itself that there was something to ask for when there was not.
			name: "readable and current but unresolved to any OID",
			meta: OIDMetadata{Access: "read-only", Status: "current", IsScalar: true},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.meta.IsPollable(); got != tt.want {
				t.Errorf("IsPollable() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestOIDMetadataTypePredicates pins the type classification the metric layer
// uses to choose between a counter, a gauge and a label. A counter mistaken for
// a gauge is graphed as a raw total instead of a rate.
func TestOIDMetadataTypePredicates(t *testing.T) {
	tests := []struct {
		typeName string
		counter  bool
		gauge    bool
		str      bool
	}{
		{typeName: "Counter32", counter: true},
		{typeName: "Counter64", counter: true},
		{typeName: "Counter", counter: true},
		{typeName: "TimeTicks", counter: true},
		{typeName: "Gauge32", gauge: true},
		{typeName: "Gauge", gauge: true},
		{typeName: "Integer32", gauge: true},
		{typeName: "Integer", gauge: true},
		{typeName: "Unsigned32", gauge: true},
		{typeName: "OctetString", str: true},
		{typeName: "DisplayString", str: true},
		{typeName: "ObjectIdentifier"},
		{typeName: ""},
		// The predicates compare exact MIB type names; a lowercase spelling is
		// not a MIB type and must not be classified as one.
		{typeName: "counter32"},
	}

	for _, tt := range tests {
		t.Run(tt.typeName, func(t *testing.T) {
			meta := &OIDMetadata{Type: tt.typeName}
			if got := meta.IsCounter(); got != tt.counter {
				t.Errorf("IsCounter() = %v, want %v", got, tt.counter)
			}
			if got := meta.IsGauge(); got != tt.gauge {
				t.Errorf("IsGauge() = %v, want %v", got, tt.gauge)
			}
			if got := meta.IsString(); got != tt.str {
				t.Errorf("IsString() = %v, want %v", got, tt.str)
			}
		})
	}
}

// TestOIDMetadataTypePredicatesAreMutuallyExclusive checks that no type is
// classified two ways at once, which would make the metric layer's choice
// depend on the order it happens to test them in.
func TestOIDMetadataTypePredicatesAreMutuallyExclusive(t *testing.T) {
	for _, typeName := range []string{
		"Counter32", "Counter64", "TimeTicks", "Gauge32",
		"Integer32", "Unsigned32", "OctetString", "DisplayString",
	} {
		meta := &OIDMetadata{Type: typeName}
		matches := 0
		for _, matched := range []bool{meta.IsCounter(), meta.IsGauge(), meta.IsString()} {
			if matched {
				matches++
			}
		}
		if matches != 1 {
			t.Errorf("%s matches %d classifications, want exactly 1", typeName, matches)
		}
	}
}

func TestOIDMetadataString(t *testing.T) {
	meta := &OIDMetadata{
		OID:    "1.3.6.1.2.1.2.2.1.10",
		Name:   "ifInOctets",
		Type:   "Counter32",
		Access: "read-only",
	}

	got := meta.String()
	for _, want := range []string{"ifInOctets", "1.3.6.1.2.1.2.2.1.10", "Counter32", "read-only"} {
		if !strings.Contains(got, want) {
			t.Errorf("String() = %q, want it to contain %q", got, want)
		}
	}
}

// TestOIDMetadataStringOnZeroValue makes sure the description an operator sees
// for an unpopulated record is still a well-formed line rather than a panic.
func TestOIDMetadataStringOnZeroValue(t *testing.T) {
	if got := (&OIDMetadata{}).String(); got == "" {
		t.Error("String() on a zero-value OIDMetadata is empty")
	}
}

func TestModuleInfoString(t *testing.T) {
	info := &ModuleInfo{
		Name:     "IF-MIB",
		OIDCount: 293,
		LoadedAt: time.Date(2024, time.March, 1, 9, 30, 0, 0, time.UTC),
	}

	got := info.String()
	for _, want := range []string{"IF-MIB", "293", "2024-03-01"} {
		if !strings.Contains(got, want) {
			t.Errorf("String() = %q, want it to contain %q", got, want)
		}
	}
}

func TestModuleInfoStringOnZeroValue(t *testing.T) {
	got := (&ModuleInfo{}).String()
	if !strings.Contains(got, "0 OIDs") {
		t.Errorf("String() = %q, want it to report an empty module as such", got)
	}
}

func TestTableInfoString(t *testing.T) {
	table := &TableInfo{
		OID:     "1.3.6.1.2.1.2.2",
		Name:    "ifTable",
		Columns: []string{"1.3.6.1.2.1.2.2.1.1", "1.3.6.1.2.1.2.2.1.2"},
	}

	got := table.String()
	for _, want := range []string{"ifTable", "1.3.6.1.2.1.2.2", "2 columns"} {
		if !strings.Contains(got, want) {
			t.Errorf("String() = %q, want it to contain %q", got, want)
		}
	}
}

func TestTableInfoStringWithNoColumns(t *testing.T) {
	got := (&TableInfo{Name: "emptyTable"}).String()
	if !strings.Contains(got, "0 columns") {
		t.Errorf("String() = %q, want it to report no columns", got)
	}
}
