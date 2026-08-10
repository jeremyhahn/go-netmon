package metrics

import (
	"testing"
	"time"

	"github.com/jeremyhahn/go-netmon/internal/mib"
	"github.com/jeremyhahn/go-netmon/internal/mib/storage"
	"github.com/jeremyhahn/go-netmon/internal/snmp"
)

// The tests in this file pin one invariant: every sample a conversion produces
// for a device carries the same caller-supplied labels, whether it is an
// ordinary reading, a status info metric or snmp_system_info. Info metrics are
// what dashboards join against, so an info series missing the environment or
// site label its readings carry silently breaks every one of those joins.

// ifTableObjects is the ifTable cut down to what these tests need: an index
// column, a textual status column that becomes snmp_ifdescr_info, and a
// counter column that becomes an ordinary sample. One poll of both columns
// therefore exercises both construction paths in a single conversion.
var ifTableObjects = []*mib.OIDMetadata{
	{
		OID:          "1.3.6.1.2.1.2.2.1",
		Name:         "ifEntry",
		Module:       "IF-MIB",
		Parent:       "1.3.6.1.2.1.2.2",
		IsTableEntry: true,
		Index:        []string{"ifIndex"},
	},
	{
		OID:         "1.3.6.1.2.1.2.2.1.1",
		Name:        "ifIndex",
		Module:      "IF-MIB",
		Parent:      "1.3.6.1.2.1.2.2.1",
		IsColumn:    true,
		Type:        "Integer32",
		ObjectClass: mib.ObjectClassIndexColumn,
	},
	{
		OID:         "1.3.6.1.2.1.2.2.1.2",
		Name:        "ifDescr",
		Module:      "IF-MIB",
		Parent:      "1.3.6.1.2.1.2.2.1",
		IsColumn:    true,
		Type:        "DisplayString",
		ObjectClass: mib.ObjectClassStatusColumn,
	},
	{
		OID:         "1.3.6.1.2.1.2.2.1.10",
		Name:        "ifInOctets",
		Module:      "IF-MIB",
		Parent:      "1.3.6.1.2.1.2.2.1",
		IsColumn:    true,
		Type:        "Counter32",
		ObjectClass: mib.ObjectClassMetricColumn,
	},
}

const (
	ifDescrOID    = ".1.3.6.1.2.1.2.2.1.2.3"
	ifInOctetsOID = ".1.3.6.1.2.1.2.2.1.10.3"
	sysNameOID    = ".1.3.6.1.2.1.1.5.0"
)

// newIfTableCache returns an index cache over ifTableObjects held in a real
// MIB store, so the tests run against the same sweep the exporter does.
func newIfTableCache(t *testing.T) *TableIndexCache {
	t.Helper()

	store, err := storage.New(t.TempDir())
	if err != nil {
		t.Fatalf("create storage: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	for _, object := range ifTableObjects {
		if err := store.StoreOID(object); err != nil {
			t.Fatalf("store %s: %v", object.Name, err)
		}
	}

	return NewTableIndexCache(store)
}

// ifTableResults returns one reading of each column of interface 3 plus
// sysName, which is what a conversion needs to emit an ordinary sample, a
// status info metric and snmp_system_info at once.
func ifTableResults(timestamp time.Time) []*snmp.Result {
	return []*snmp.Result{
		{OID: ifInOctetsOID, Value: uint(4096), Timestamp: timestamp},
		{OID: ifDescrOID, Value: []byte("GigabitEthernet0/0/0"), Timestamp: timestamp},
		{OID: sysNameOID, Value: []byte("router1"), Timestamp: timestamp},
	}
}

// sampleNamed returns the single sample called name, failing the test when the
// conversion produced anything but exactly one.
func sampleNamed(t *testing.T, samples []*Sample, name string) *Sample {
	t.Helper()

	var found *Sample
	for _, sample := range samples {
		if sample.Name != name {
			continue
		}
		if found != nil {
			t.Fatalf("expected exactly one %s sample, got more than one", name)
		}
		found = sample
	}
	if found == nil {
		names := make([]string, 0, len(samples))
		for _, sample := range samples {
			names = append(names, sample.Name)
		}
		t.Fatalf("no %s sample in conversion output; got %v", name, names)
	}
	return found
}

// assertLabel fails when labels[name] is not want.
func assertLabel(t *testing.T, sample *Sample, name, want string) {
	t.Helper()

	if got := sample.Labels[name]; got != want {
		t.Errorf("%s label %q = %q, want %q", sample.Name, name, got, want)
	}
}

// TestInfoMetricCarriesExtraLabels is the regression test for the defect: a
// status info metric was built from the device, index and value labels alone,
// so a device exported with environment="prod" produced
// snmp_ifInOctets{environment="prod"} but a bare snmp_ifdescr_info.
func TestInfoMetricCarriesExtraLabels(t *testing.T) {
	cache := newIfTableCache(t)
	timestamp := time.Unix(1700000000, 0)
	extra := map[string]string{"environment": "prod", "site": "dc1"}

	infoMetrics := ConvertStatusColumnsToInfoMetrics("router1", ifTableResults(timestamp), extra, cache, timestamp)

	info := sampleNamed(t, infoMetrics, "snmp_ifdescr_info")
	assertLabel(t, info, "environment", "prod")
	assertLabel(t, info, "site", "dc1")

	// The structural labels must survive the merge intact.
	assertLabel(t, info, "device", "router1")
	assertLabel(t, info, "ifindex", "3")
	assertLabel(t, info, "value", "GigabitEthernet0/0/0")
	if info.Value != 1.0 {
		t.Errorf("info metric value = %v, want 1", info.Value)
	}
}

// TestConversionAppliesExtraLabelsUniformly pins the invariant that broke: an
// ordinary sample, a status info metric and snmp_system_info from one
// conversion all carry the caller's labels identically. Asserting them
// together is the point — asserting each in isolation is what let them drift.
func TestConversionAppliesExtraLabelsUniformly(t *testing.T) {
	cache := newIfTableCache(t)
	timestamp := time.Unix(1700000000, 0)
	extra := map[string]string{"environment": "prod", "site": "dc1", "role": "core"}

	samples := ConvertSNMPResultsWithTableIndexes("router1", ifTableResults(timestamp), extra, cache, nil)

	pinned := []*Sample{
		sampleNamed(t, samples, "snmp_ifInOctets"),
		sampleNamed(t, samples, "snmp_ifdescr_info"),
		sampleNamed(t, samples, "snmp_system_info"),
	}

	for _, sample := range pinned {
		for name, want := range extra {
			assertLabel(t, sample, name, want)
		}
		assertLabel(t, sample, "device", "router1")
	}
}

// TestConversionWithoutExtraLabels covers the empty-input case for every
// construction site: a nil or empty extras map must still produce fully
// labeled samples rather than tripping over the merge.
func TestConversionWithoutExtraLabels(t *testing.T) {
	timestamp := time.Unix(1700000000, 0)

	for _, tt := range []struct {
		name  string
		extra map[string]string
	}{
		{name: "nil extras", extra: nil},
		{name: "empty extras", extra: map[string]string{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cache := newIfTableCache(t)
			samples := ConvertSNMPResultsWithTableIndexes("router1", ifTableResults(timestamp), tt.extra, cache, nil)

			ordinary := sampleNamed(t, samples, "snmp_ifInOctets")
			assertLabel(t, ordinary, "device", "router1")
			assertLabel(t, ordinary, "oid", ifInOctetsOID)
			assertLabel(t, ordinary, "ifindex", "3")

			info := sampleNamed(t, samples, "snmp_ifdescr_info")
			assertLabel(t, info, "device", "router1")
			assertLabel(t, info, "ifindex", "3")
			assertLabel(t, info, "value", "GigabitEthernet0/0/0")
			if len(info.Labels) != 3 {
				t.Errorf("info metric labels = %v, want device, ifindex and value only", info.Labels)
			}

			system := sampleNamed(t, samples, "snmp_system_info")
			assertLabel(t, system, "device", "router1")
			assertLabel(t, system, "sys_name", "router1")
		})
	}
}

// TestExtraLabelsNeverOverwriteStructuralLabels asserts the precedence rule
// for the bad-input case: a caller whose tags collide with device, oid, an
// index label or an info metric's value label loses the tag. The alternative
// would let a device tag rename another device's series, or point an info
// metric's value at something it never measured.
func TestExtraLabelsNeverOverwriteStructuralLabels(t *testing.T) {
	cache := newIfTableCache(t)
	timestamp := time.Unix(1700000000, 0)
	hostile := map[string]string{
		"device":   "impostor",
		"oid":      ".9.9.9",
		"ifindex":  "999",
		"value":    "hijacked",
		"sys_name": "hijacked",
		"safe":     "kept",
	}

	samples := ConvertSNMPResultsWithTableIndexes("router1", ifTableResults(timestamp), hostile, cache, nil)

	ordinary := sampleNamed(t, samples, "snmp_ifInOctets")
	assertLabel(t, ordinary, "device", "router1")
	assertLabel(t, ordinary, "oid", ifInOctetsOID)
	assertLabel(t, ordinary, "ifindex", "3")
	assertLabel(t, ordinary, "safe", "kept")

	info := sampleNamed(t, samples, "snmp_ifdescr_info")
	assertLabel(t, info, "device", "router1")
	assertLabel(t, info, "ifindex", "3")
	assertLabel(t, info, "value", "GigabitEthernet0/0/0")
	assertLabel(t, info, "safe", "kept")

	system := sampleNamed(t, samples, "snmp_system_info")
	assertLabel(t, system, "device", "router1")
	assertLabel(t, system, "sys_name", "router1")
	assertLabel(t, system, "safe", "kept")
}

// TestExtraLabelPrecedenceIsDeterministic runs the same hostile merge many
// times over. Map iteration order is randomized, so a merge that resolved
// collisions by write order rather than by rule would disagree with itself
// across runs.
func TestExtraLabelPrecedenceIsDeterministic(t *testing.T) {
	cache := newIfTableCache(t)
	timestamp := time.Unix(1700000000, 0)
	hostile := map[string]string{
		"device":  "impostor",
		"ifindex": "999",
		"value":   "hijacked",
	}
	results := ifTableResults(timestamp)

	for i := 0; i < 100; i++ {
		infoMetrics := ConvertStatusColumnsToInfoMetrics("router1", results, hostile, cache, timestamp)
		info := sampleNamed(t, infoMetrics, "snmp_ifdescr_info")

		if info.Labels["device"] != "router1" || info.Labels["ifindex"] != "3" || info.Labels["value"] != "GigabitEthernet0/0/0" {
			t.Fatalf("iteration %d resolved a collision differently: %v", i, info.Labels)
		}
	}
}

// TestConvertSNMPResultsToSamplesLabelPrecedence covers the MIB-less
// converter, whose samples are built by the same merge.
func TestConvertSNMPResultsToSamplesLabelPrecedence(t *testing.T) {
	timestamp := time.Unix(1700000000, 0)
	results := ifTableResults(timestamp)

	t.Run("extras applied", func(t *testing.T) {
		samples := ConvertSNMPResultsToSamples("router1", results, map[string]string{"environment": "prod"})

		for _, sample := range samples {
			assertLabel(t, sample, "environment", "prod")
			assertLabel(t, sample, "device", "router1")
		}
		assertLabel(t, sampleNamed(t, samples, "snmp_system_info"), "sys_name", "router1")
	})

	t.Run("collisions rejected", func(t *testing.T) {
		samples := ConvertSNMPResultsToSamples("router1", results, map[string]string{
			"device":   "impostor",
			"oid":      ".9.9.9",
			"sys_name": "hijacked",
		})

		for _, sample := range samples {
			assertLabel(t, sample, "device", "router1")
		}
		assertLabel(t, sampleNamed(t, samples, "snmp_system_info"), "sys_name", "router1")
	})
}

// TestConvertSNMPResultsWithInterfaceNamesLabelPrecedence covers the
// interface-name converter, including its ifname and ifindex labels, which are
// structural for the same reason a table index label is.
func TestConvertSNMPResultsWithInterfaceNamesLabelPrecedence(t *testing.T) {
	timestamp := time.Unix(1700000000, 0)
	results := ifTableResults(timestamp)

	ifNames := NewInterfaceNameCache()
	ifNames.UpdateFromSNMPResults("router1", results)

	t.Run("extras applied", func(t *testing.T) {
		samples := ConvertSNMPResultsWithInterfaceNames("router1", results, map[string]string{"environment": "prod"}, ifNames)

		for _, sample := range samples {
			assertLabel(t, sample, "environment", "prod")
			assertLabel(t, sample, "device", "router1")
		}
		assertLabel(t, sampleNamed(t, samples, "snmp_system_info"), "sys_name", "router1")
	})

	t.Run("collisions rejected", func(t *testing.T) {
		samples := ConvertSNMPResultsWithInterfaceNames("router1", results, map[string]string{
			"device":  "impostor",
			"oid":     ".9.9.9",
			"ifname":  "hijacked",
			"ifindex": "999",
		}, ifNames)

		counter := sampleNamed(t, samples, "snmp__1_3_6_1_2_1_2_2_1_10_3")
		assertLabel(t, counter, "device", "router1")
		assertLabel(t, counter, "oid", ifInOctetsOID)
		assertLabel(t, counter, "ifname", "GigabitEthernet0/0/0")
		assertLabel(t, counter, "ifindex", "3")
	})
}

// TestMergeExtraLabels exercises the merge directly, including the inputs the
// converters can hand it when a device has no tags at all.
func TestMergeExtraLabels(t *testing.T) {
	for _, tt := range []struct {
		name   string
		labels map[string]string
		extra  map[string]string
		want   map[string]string
	}{
		{
			name:   "disjoint labels are all kept",
			labels: map[string]string{labelDevice: "router1"},
			extra:  map[string]string{"environment": "prod", "site": "dc1"},
			want:   map[string]string{labelDevice: "router1", "environment": "prod", "site": "dc1"},
		},
		{
			name:   "nil extras leave the labels untouched",
			labels: map[string]string{labelDevice: "router1"},
			extra:  nil,
			want:   map[string]string{labelDevice: "router1"},
		},
		{
			name:   "empty extras leave the labels untouched",
			labels: map[string]string{labelDevice: "router1"},
			extra:  map[string]string{},
			want:   map[string]string{labelDevice: "router1"},
		},
		{
			name:   "colliding extras are dropped",
			labels: map[string]string{labelDevice: "router1", "ifindex": "3"},
			extra:  map[string]string{labelDevice: "impostor", "ifindex": "999", "site": "dc1"},
			want:   map[string]string{labelDevice: "router1", "ifindex": "3", "site": "dc1"},
		},
		{
			name:   "an empty extra value is a value like any other",
			labels: map[string]string{labelDevice: "router1"},
			extra:  map[string]string{"site": ""},
			want:   map[string]string{labelDevice: "router1", "site": ""},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			mergeExtraLabels("router1", tt.labels, tt.extra)

			if len(tt.labels) != len(tt.want) {
				t.Fatalf("merged labels = %v, want %v", tt.labels, tt.want)
			}
			for name, want := range tt.want {
				if got := tt.labels[name]; got != want {
					t.Errorf("label %q = %q, want %q", name, got, want)
				}
			}
		})
	}
}

// TestNewSystemInfoSample covers the shared snmp_system_info builder, which is
// the one place all three converters now go through.
func TestNewSystemInfoSample(t *testing.T) {
	timestamp := time.Unix(1700000000, 0)

	t.Run("carries device, system fields and extras", func(t *testing.T) {
		sample := newSystemInfoSample("router1",
			map[string]string{"sys_name": "router1", "sys_location": "dc1"},
			map[string]string{"environment": "prod"},
			timestamp)

		if sample.Name != "snmp_system_info" {
			t.Errorf("name = %q, want snmp_system_info", sample.Name)
		}
		if sample.Value != 1.0 {
			t.Errorf("value = %v, want 1", sample.Value)
		}
		if !sample.Timestamp.Equal(timestamp) {
			t.Errorf("timestamp = %v, want %v", sample.Timestamp, timestamp)
		}
		assertLabel(t, sample, "device", "router1")
		assertLabel(t, sample, "sys_name", "router1")
		assertLabel(t, sample, "sys_location", "dc1")
		assertLabel(t, sample, "environment", "prod")
	})

	t.Run("system fields outrank colliding extras", func(t *testing.T) {
		sample := newSystemInfoSample("router1",
			map[string]string{"sys_name": "router1"},
			map[string]string{"sys_name": "hijacked", "device": "impostor"},
			timestamp)

		assertLabel(t, sample, "sys_name", "router1")
		assertLabel(t, sample, "device", "router1")
	})
}

// TestConvertStatusColumnsToInfoMetricsNilCache covers the bad-input path: a
// caller with no index cache has no MIB metadata to build info metrics from,
// and must get nothing back rather than a panic.
func TestConvertStatusColumnsToInfoMetricsNilCache(t *testing.T) {
	timestamp := time.Unix(1700000000, 0)

	infoMetrics := ConvertStatusColumnsToInfoMetrics("router1", ifTableResults(timestamp),
		map[string]string{"environment": "prod"}, nil, timestamp)

	if len(infoMetrics) != 0 {
		t.Errorf("info metrics from a nil cache = %v, want none", infoMetrics)
	}
}
