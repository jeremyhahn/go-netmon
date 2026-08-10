package metrics

import (
	"fmt"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/jeremyhahn/go-netmon/internal/mib"
)

// testObjects is the fixture the index tests build on: a scalar, a
// single-index table column, a two-index table column and the nested pair of
// scalars that exercises longest-prefix matching.
func testObjects() []*mib.OIDMetadata {
	return []*mib.OIDMetadata{
		// Scalar.
		{OID: "1.3.6.1.2.1.2.1", Name: "ifNumber", Module: "IF-MIB", IsScalar: true},

		// Single-index table: ifTable / ifEntry / ifInOctets.
		{OID: "1.3.6.1.2.1.2.2", Name: "ifTable", Module: "IF-MIB", IsTable: true},
		{OID: "1.3.6.1.2.1.2.2.1", Name: "ifEntry", Module: "IF-MIB", IsTableEntry: true, Index: []string{"ifIndex"}},
		{OID: "1.3.6.1.2.1.2.2.1.1", Name: "ifIndex", Module: "IF-MIB", IsColumn: true, Type: "Integer32"},
		{OID: "1.3.6.1.2.1.2.2.1.10", Name: "ifInOctets", Module: "IF-MIB", IsColumn: true, Type: "Counter32"},

		// Two-index table: CISCO-PROCESS-MIB cpmProcessExtRevEntry.
		{OID: "1.3.6.1.4.1.9.9.109.1.2.3.1", Name: "cpmProcessExtRevEntry", Module: "CISCO-PROCESS-MIB",
			IsTableEntry: true, Index: []string{"cpmCPUTotalIndex", "cpmProcessPID"}},
		{OID: "1.3.6.1.4.1.9.9.109.1.2.3.1.17", Name: "cpmProcessMemoryCore", Module: "CISCO-PROCESS-MIB",
			IsColumn: true, Type: "Gauge32"},

		// One object OID that is a strict prefix of another object OID.
		{OID: "1.3.6.1.4.1.99.1", Name: "outerScalar", Module: "TEST-MIB", IsScalar: true},
		{OID: "1.3.6.1.4.1.99.1.2", Name: "innerScalar", Module: "TEST-MIB", IsScalar: true},

		// Objects an agent never instantiates, plus unusable records.
		{OID: "1.3.6.1.4.1.99.9", Name: "", Module: "TEST-MIB", IsScalar: true},
		{OID: "", Name: "noOID", Module: "TEST-MIB", IsColumn: true},
		nil,
	}
}

func TestObjectNameIndexMetricNameFor(t *testing.T) {
	index := newObjectNameIndex(testObjects())

	tests := []struct {
		name        string
		instanceOID string
		want        string
		wantFound   bool
	}{
		{
			name:        "scalar instance drops the .0 suffix",
			instanceOID: "1.3.6.1.2.1.2.1.0",
			want:        "snmp_ifNumber",
			wantFound:   true,
		},
		{
			name:        "scalar instance with a leading dot",
			instanceOID: ".1.3.6.1.2.1.2.1.0",
			want:        "snmp_ifNumber",
			wantFound:   true,
		},
		{
			name:        "single-index row drops the row index",
			instanceOID: "1.3.6.1.2.1.2.2.1.10.5",
			want:        "snmp_ifInOctets",
			wantFound:   true,
		},
		{
			name:        "a different row of the same column resolves identically",
			instanceOID: "1.3.6.1.2.1.2.2.1.10.42",
			want:        "snmp_ifInOctets",
			wantFound:   true,
		},
		{
			name:        "two-part row index is dropped whole",
			instanceOID: "1.3.6.1.4.1.9.9.109.1.2.3.1.17.7.5420",
			want:        "snmp_cpmProcessMemoryCore",
			wantFound:   true,
		},
		{
			name:        "long row index is dropped whole",
			instanceOID: "1.3.6.1.2.1.2.2.1.10.1.4.203.0.113.218.1.1.2.3.4.5.6.7.8.9",
			want:        "snmp_ifInOctets",
			wantFound:   true,
		},
		{
			name:        "longest prefix wins over the shorter enclosing object",
			instanceOID: "1.3.6.1.4.1.99.1.2.0",
			want:        "snmp_innerScalar",
			wantFound:   true,
		},
		{
			name:        "shorter object still resolves when it is the longest match",
			instanceOID: "1.3.6.1.4.1.99.1.0",
			want:        "snmp_outerScalar",
			wantFound:   true,
		},
		{
			name:        "object OID itself resolves",
			instanceOID: "1.3.6.1.2.1.2.2.1.10",
			want:        "snmp_ifInOctets",
			wantFound:   true,
		},
		{
			name:        "OID from no loaded MIB is unresolved",
			instanceOID: "1.3.6.1.4.1.99999.7.1.0",
			wantFound:   false,
		},
		{
			name:        "table entry is never instantiable so its rows are unresolved",
			instanceOID: "1.3.6.1.2.1.2.2.1.99.5",
			wantFound:   false,
		},
		{
			name:        "OID shorter than any indexed object is unresolved",
			instanceOID: "1.3.6",
			wantFound:   false,
		},
		{
			name:        "single sub-identifier is unresolved",
			instanceOID: "1",
			wantFound:   false,
		},
		{
			name:        "empty OID is unresolved",
			instanceOID: "",
			wantFound:   false,
		},
		{
			name:        "bare dot is unresolved",
			instanceOID: ".",
			wantFound:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, found := index.metricNameFor(tt.instanceOID)
			if found != tt.wantFound {
				t.Fatalf("metricNameFor(%q) found = %v, want %v (got name %q)", tt.instanceOID, found, tt.wantFound, got)
			}
			if found && got != tt.want {
				t.Errorf("metricNameFor(%q) = %q, want %q", tt.instanceOID, got, tt.want)
			}
		})
	}
}

func TestObjectNameIndexIndexesOnlyInstantiableObjects(t *testing.T) {
	index := newObjectNameIndex(testObjects())

	for _, oid := range []string{
		"1.3.6.1.2.1.2.2",   // ifTable
		"1.3.6.1.2.1.2.2.1", // ifEntry
		"1.3.6.1.4.1.99.9",  // indexed object with no name
	} {
		if name, found := index.metricNames[oid]; found {
			t.Errorf("OID %s should not be indexed, got %q", oid, name)
		}
	}

	if len(index.metricNames) != 6 {
		t.Errorf("expected 6 instantiable objects indexed, got %d: %v", len(index.metricNames), index.metricNames)
	}
}

// TestObjectNameIndexMalformedOIDs feeds the index OIDs that are not really
// OIDs at all, which is what a hand-edited or partially imported MIB record
// looks like. Resolution must terminate and report "not found" rather than
// walking off the front of the string.
func TestObjectNameIndexMalformedOIDs(t *testing.T) {
	index := newObjectNameIndex([]*mib.OIDMetadata{
		{OID: "9", Name: "singleSubIDObject", Module: "TEST-MIB", IsScalar: true},
	})

	if got, found := index.metricNameFor("9.0"); !found || got != "snmp_singleSubIDObject" {
		t.Errorf("metricNameFor(%q) = %q, %v; want %q, true", "9.0", got, found, "snmp_singleSubIDObject")
	}

	for _, oid := range []string{"1", "notanoid", "."} {
		if got, found := index.metricNameFor(oid); found {
			t.Errorf("metricNameFor(%q) = %q, true; want unresolved", oid, got)
		}
	}
}

func TestObjectNameIndexFirstDefinitionWins(t *testing.T) {
	index := newObjectNameIndex([]*mib.OIDMetadata{
		{OID: "1.3.6.1.2.1.2.1", Name: "ifNumber", Module: "IF-MIB", IsScalar: true},
		{OID: ".1.3.6.1.2.1.2.1", Name: "ifNumberDuplicate", Module: "OTHER-MIB", IsScalar: true},
	})

	got, found := index.metricNameFor("1.3.6.1.2.1.2.1.0")
	if !found || got != "snmp_ifNumber" {
		t.Errorf("metricNameFor() = %q, %v; want %q, true", got, found, "snmp_ifNumber")
	}
}

func TestObjectNameIndexSanitizesObjectNames(t *testing.T) {
	index := newObjectNameIndex([]*mib.OIDMetadata{
		{OID: "1.3.6.1.4.1.99.5", Name: "weird-object.name", Module: "TEST-MIB", IsScalar: true},
	})

	got, found := index.metricNameFor("1.3.6.1.4.1.99.5.0")
	if !found || got != "snmp_weird_object_name" {
		t.Errorf("metricNameFor() = %q, %v; want %q, true", got, found, "snmp_weird_object_name")
	}
}

func TestObjectNameIndexEmptyAndNil(t *testing.T) {
	tests := []struct {
		name  string
		index *objectNameIndex
	}{
		{name: "nil index", index: nil},
		{name: "index built from no OIDs", index: newObjectNameIndex(nil)},
		{name: "index built from only non-instantiable OIDs", index: newObjectNameIndex([]*mib.OIDMetadata{
			{OID: "1.3.6.1.2.1.2.2.1", Name: "ifEntry", IsTableEntry: true},
		})},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, found := tt.index.metricNameFor("1.3.6.1.2.1.2.1.0"); found {
				t.Errorf("metricNameFor() = %q, true; want unresolved", got)
			}
		})
	}
}

func TestResolveMetricName(t *testing.T) {
	index := newObjectNameIndex(testObjects())

	tests := []struct {
		name         string
		index        *objectNameIndex
		oid          string
		want         string
		wantResolved bool
	}{
		{
			name:         "resolved from the MIB object name",
			index:        index,
			oid:          "1.3.6.1.2.1.2.2.1.10.5",
			want:         "snmp_ifInOctets",
			wantResolved: true,
		},
		{
			name:         "unknown OID keeps its raw-OID name",
			index:        index,
			oid:          "1.3.6.1.4.1.99999.7.1",
			want:         "snmp_1_3_6_1_4_1_99999_7_1",
			wantResolved: false,
		},
		{
			name:         "unknown OID with a leading dot keeps its raw-OID name",
			index:        index,
			oid:          ".1.3.6.1.4.1.99999.7.1",
			want:         "snmp__1_3_6_1_4_1_99999_7_1",
			wantResolved: false,
		},
		{
			name:         "nil index falls back for everything",
			index:        nil,
			oid:          "1.3.6.1.2.1.2.2.1.10.5",
			want:         "snmp_1_3_6_1_2_1_2_2_1_10_5",
			wantResolved: false,
		},
		{
			name:         "empty OID falls back",
			index:        index,
			oid:          "",
			want:         "snmp_",
			wantResolved: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, resolved := resolveMetricName(tt.index, tt.oid)
			if got != tt.want || resolved != tt.wantResolved {
				t.Errorf("resolveMetricName(%q) = %q, %v; want %q, %v", tt.oid, got, resolved, tt.want, tt.wantResolved)
			}
		})
	}
}

func TestCountSubIDs(t *testing.T) {
	tests := []struct {
		oid  string
		want int
	}{
		{oid: "", want: 0},
		{oid: "1", want: 1},
		{oid: "1.3.6", want: 3},
		{oid: "1.3.6.1.2.1.2.2.1.10.5", want: 11},
	}

	for _, tt := range tests {
		t.Run(tt.oid, func(t *testing.T) {
			if got := countSubIDs(tt.oid); got != tt.want {
				t.Errorf("countSubIDs(%q) = %d, want %d", tt.oid, got, tt.want)
			}
		})
	}
}

// TestObjectNameIndexLookupDoesNotAllocate guards the poll path: name
// resolution runs once per sample for tens of thousands of samples per poll,
// so a per-lookup allocation would show up directly as GC pressure.
func TestObjectNameIndexLookupDoesNotAllocate(t *testing.T) {
	index := newObjectNameIndex(testObjects())

	allocs := testing.AllocsPerRun(100, func() {
		if _, found := index.metricNameFor(".1.3.6.1.4.1.9.9.109.1.2.3.1.17.7.5420"); !found {
			t.Fatal("expected the fixture OID to resolve")
		}
		if _, found := index.metricNameFor(".1.3.6.1.4.1.99999.7.1.0"); found {
			t.Fatal("expected the unknown OID not to resolve")
		}
	})

	if allocs != 0 {
		t.Errorf("metricNameFor allocated %v times per run, want 0", allocs)
	}
}

// unresolvedSamplesFor reads the unresolved-name counter for one device.
func unresolvedSamplesFor(t *testing.T, device string) float64 {
	t.Helper()
	return testutil.ToFloat64(metricNameUnresolvedSamples.WithLabelValues(device))
}

// benchmarkIndex builds an index of the size a couple of fully imported
// vendor MIB trees produce, so the probe bounds are exercised realistically.
func benchmarkIndex() *objectNameIndex {
	const (
		tables          = 400
		columnsPerTable = 40
		scalars         = 2000
	)

	oids := make([]*mib.OIDMetadata, 0, tables*columnsPerTable+scalars)
	for table := 0; table < tables; table++ {
		for column := 1; column <= columnsPerTable; column++ {
			oids = append(oids, &mib.OIDMetadata{
				OID:      fmt.Sprintf("1.3.6.1.4.1.9.9.%d.1.1.1.%d", table, column),
				Name:     fmt.Sprintf("vendorColumn%d_%d", table, column),
				Module:   "VENDOR-MIB",
				IsColumn: true,
			})
		}
	}
	for scalar := 0; scalar < scalars; scalar++ {
		oids = append(oids, &mib.OIDMetadata{
			OID:      fmt.Sprintf("1.3.6.1.2.1.%d.%d", scalar/64, scalar%64),
			Name:     fmt.Sprintf("vendorScalar%d", scalar),
			Module:   "VENDOR-MIB",
			IsScalar: true,
		})
	}
	return newObjectNameIndex(oids)
}

// BenchmarkObjectNameIndexMetricNameFor measures the per-sample cost of
// naming a metric, which the exporter pays once for every sample of every
// poll of every device.
func BenchmarkObjectNameIndexMetricNameFor(b *testing.B) {
	index := benchmarkIndex()

	cases := []struct {
		name string
		oid  string
	}{
		{name: "scalar", oid: ".1.3.6.1.2.1.7.13.0"},
		{name: "single_index_row", oid: ".1.3.6.1.4.1.9.9.100.1.1.1.10.5420"},
		{name: "two_index_row", oid: ".1.3.6.1.4.1.9.9.100.1.1.1.17.7.5420"},
		{name: "long_index_row", oid: ".1.3.6.1.4.1.9.9.100.1.1.1.17.1.4.203.0.113.218.1.1"},
		{name: "unresolvable", oid: ".1.3.6.1.4.1.99999.7.1.0"},
	}

	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, _ = index.metricNameFor(tc.oid)
			}
		})
	}
}

// BenchmarkResolveMetricName measures naming including the raw-OID fallback
// path taken by OIDs no loaded MIB describes.
func BenchmarkResolveMetricName(b *testing.B) {
	index := benchmarkIndex()

	b.Run("resolved", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_, _ = resolveMetricName(index, ".1.3.6.1.4.1.9.9.100.1.1.1.17.7.5420")
		}
	})

	b.Run("unresolved", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_, _ = resolveMetricName(index, ".1.3.6.1.4.1.99999.7.1.0")
		}
	})
}

// BenchmarkNewObjectNameIndex measures the one-off build cost paid whenever
// MIB metadata is loaded.
func BenchmarkNewObjectNameIndex(b *testing.B) {
	oids := make([]*mib.OIDMetadata, 0, 20000)
	for i := 0; i < 20000; i++ {
		oids = append(oids, &mib.OIDMetadata{
			OID:      fmt.Sprintf("1.3.6.1.4.1.9.9.%d.1.1.1.%d", i/64, i%64),
			Name:     fmt.Sprintf("vendorColumn%d", i),
			IsColumn: true,
		})
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = newObjectNameIndex(oids)
	}
}
