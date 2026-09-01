package metrics

import (
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-netmon/internal/mib"
)

// TestClassifyKind pins the rule a renderer keys on.
//
// kind is the one catalog field that must be reliable: unit is declared by
// fewer than a fifth of MIB objects, so a renderer that depended on it would
// be blank most of the time, while kind is derivable for every object whose
// type resolved.
func TestClassifyKind(t *testing.T) {
	tests := []struct {
		name   string
		object *mib.OIDMetadata
		want   MetricKind
	}{
		{"Counter32", &mib.OIDMetadata{Type: "Counter32"}, MetricKindCounter},
		{"Counter64", &mib.OIDMetadata{Type: "Counter64"}, MetricKindCounter},
		{"TimeTicks is counter-like", &mib.OIDMetadata{Type: "TimeTicks"}, MetricKindCounter},
		{"Gauge32", &mib.OIDMetadata{Type: "Gauge32"}, MetricKindGauge},
		{"Unsigned32", &mib.OIDMetadata{Type: "Unsigned32"}, MetricKindGauge},
		{"Integer32", &mib.OIDMetadata{Type: "Integer32"}, MetricKindGauge},
		{"DisplayString", &mib.OIDMetadata{Type: "DisplayString"}, MetricKindString},
		{"OctetString", &mib.OIDMetadata{Type: "OctetString"}, MetricKindString},
		{"SnmpAdminString", &mib.OIDMetadata{Type: "SnmpAdminString"}, MetricKindString},
		{"IpAddress", &mib.OIDMetadata{Type: "IpAddress"}, MetricKindString},
		{
			name: "an enumeration, whatever its integer type",
			object: &mib.OIDMetadata{
				Type:       "Integer32",
				EnumValues: map[int64]string{1: "up", 2: "down"},
			},
			want: MetricKindEnum,
		},
		{
			name: "TruthValue with its enumeration",
			object: &mib.OIDMetadata{
				Type:       "TruthValue",
				EnumValues: map[int64]string{1: "true", 2: "false"},
			},
			want: MetricKindEnum,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyKind(tt.object); got != tt.want {
				t.Errorf("classifyKind() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestClassifyKindReportsUnresolvedTypesAsUnknown is the diagnostic case.
//
// An object whose module parsed with unresolved imports comes back with no
// type at all, and loses its units, display hint and enumeration in the same
// stroke. Measured at 730 of 9,638 pollable objects across 198 modules. The
// catalog reports that rather than guessing, because a guess would put a line
// chart on something that might be a hostname.
func TestClassifyKindReportsUnresolvedTypesAsUnknown(t *testing.T) {
	tests := []struct {
		name   string
		object *mib.OIDMetadata
	}{
		{"no type resolved", &mib.OIDMetadata{Name: "atmVplOperStatus"}},
		{"nil object", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyKind(tt.object); got != MetricKindUnknown {
				t.Errorf("classifyKind() = %q, want %q", got, MetricKindUnknown)
			}
		})
	}
}

// TestClassifyKindPrefersUnknownOverEverything checks the ordering: an object
// with no resolved type must not be classified from whatever else survived,
// because nothing about it is trustworthy.
func TestClassifyKindPrefersUnknownOverEverything(t *testing.T) {
	object := &mib.OIDMetadata{
		Name:       "atmVplOperStatus",
		EnumValues: map[int64]string{1: "up"},
		Units:      "seconds",
	}

	if got := classifyKind(object); got != MetricKindUnknown {
		t.Errorf("classifyKind() = %q for an object with no resolved type, want %q",
			got, MetricKindUnknown)
	}
}

// TestNewCatalogIndexKeysOnThePublishedName checks the primary key.
func TestNewCatalogIndexKeysOnThePublishedName(t *testing.T) {
	index := newCatalogIndex([]*mib.OIDMetadata{
		{
			OID: ".1.3.6.1.2.1.2.2.1.10", Name: "ifInOctets", Module: "IF-MIB",
			Type: "Counter32", Units: "octets", Description: "Octets received.",
			IsColumn: true, EntryOID: ".1.3.6.1.2.1.2.2.1",
		},
	}, newTableSet(), nil)

	entry, found := index.entryFor("snmp_ifInOctets")
	if !found {
		t.Fatalf("no entry under the published name; index holds %d entries", len(index.entries))
	}
	if entry.Object != "ifInOctets" || entry.Module != "IF-MIB" {
		t.Errorf("entry = %+v, want the object's provenance", entry)
	}
	if entry.Kind != MetricKindCounter {
		t.Errorf("Kind = %q, want counter", entry.Kind)
	}
	if entry.Unit != "octets" {
		t.Errorf("Unit = %q, want octets", entry.Unit)
	}
}

// TestNewCatalogIndexIsFirstWinsOnCollision checks that the catalog resolves a
// duplicated metric name the same way the metric name index does.
//
// 436 of 9,638 objects collide across 198 modules, so this is routine. Both
// OIDs are resolved through the name index rather than only the first, because
// the point is not that the catalog has some entry -- it is that both
// colliding objects publish under the name the catalog describes. Asserting
// only the first would check the catalog against itself.
func TestNewCatalogIndexIsFirstWinsOnCollision(t *testing.T) {
	objects := []*mib.OIDMetadata{
		{OID: ".1.3.6.1.2.1.1.9", Name: "sysCounter", Module: "FIRST-MIB", Type: "Counter32", IsScalar: true},
		{OID: ".1.3.6.1.4.1.9.1.9", Name: "sysCounter", Module: "SECOND-MIB", Type: "Counter32", IsScalar: true},
	}

	index := newCatalogIndex(objects, newTableSet(), nil)

	entry, found := index.entryFor("snmp_sysCounter")
	if !found {
		t.Fatal("no entry for the colliding name")
	}
	if entry.Module != "FIRST-MIB" {
		t.Errorf("Module = %q, want the first definition to win", entry.Module)
	}
	if index.collisions != 1 {
		t.Errorf("collisions = %d, want 1", index.collisions)
	}

	// Both objects publish under the name the catalog describes, which is
	// what makes one description for two objects the right question rather
	// than a bookkeeping detail.
	names := newObjectNameIndex(objects)
	for _, instance := range []string{".1.3.6.1.2.1.1.9.0", ".1.3.6.1.4.1.9.1.9.0"} {
		got, resolved := names.metricNameFor(instance)
		if !resolved {
			t.Fatalf("the name index does not resolve %s", instance)
		}
		if got != entry.Metric {
			t.Errorf("%s is published as %q but the catalog describes %q", instance, got, entry.Metric)
		}
	}
}

// TestCatalogAndNameIndexDescribeTheSameObjects checks that the two indexes
// are built from one filtered set.
//
// They answer two halves of one question -- what a sample is called, and what
// that name means -- so a sample named by one and described by the other is
// worse than either being wrong alone.
func TestCatalogAndNameIndexDescribeTheSameObjects(t *testing.T) {
	objects := []*mib.OIDMetadata{
		{OID: ".1.3.6.1.2.1.2.2.1.10", Name: "ifInOctets", Module: "IF-MIB", Type: "Counter32", IsColumn: true},
		{OID: ".1.3.6.1.2.1.1.3", Name: "sysUpTime", Module: "SNMPv2-MIB", Type: "TimeTicks", IsScalar: true},
		// Never instantiated, so neither index may hold it.
		{OID: ".1.3.6.1.2.1.2.2", Name: "ifTable", Module: "IF-MIB", IsTable: true},
		{OID: ".1.3.6.1.2.1.2.2.1", Name: "ifEntry", Module: "IF-MIB", IsTableEntry: true},
	}

	catalog := newCatalogIndex(objects, newTableSet(), nil)
	names := newObjectNameIndex(objects)

	for _, tt := range []struct{ instance, metric string }{
		{".1.3.6.1.2.1.2.2.1.10.1", "snmp_ifInOctets"},
		{".1.3.6.1.2.1.1.3.0", "snmp_sysUpTime"},
	} {
		got, resolved := names.metricNameFor(tt.instance)
		if !resolved || got != tt.metric {
			t.Fatalf("name index resolved %s to %q (%v), want %q", tt.instance, got, resolved, tt.metric)
		}
		if _, described := catalog.entryFor(got); !described {
			t.Errorf("%s is published as %q and the catalog cannot describe it", tt.instance, got)
		}
	}

	for _, notInstantiated := range []string{"snmp_ifTable", "snmp_ifEntry"} {
		if _, described := catalog.entryFor(notInstantiated); described {
			t.Errorf("the catalog describes %q, which is never published", notInstantiated)
		}
	}
}

// TestNewCatalogIndexSkipsWhatIsNeverPublished checks that the catalog holds
// rows only for things a device can answer. A table node or a conceptual row
// is never instantiated, so a row describing one documents nothing.
func TestNewCatalogIndexSkipsWhatIsNeverPublished(t *testing.T) {
	index := newCatalogIndex([]*mib.OIDMetadata{
		{OID: ".1.3.6.1.2.1.2.2", Name: "ifTable", Module: "IF-MIB", IsTable: true},
		{OID: ".1.3.6.1.2.1.2.2.1", Name: "ifEntry", Module: "IF-MIB", IsTableEntry: true},
		{Name: "noOID", Module: "IF-MIB", IsScalar: true},
		{OID: ".1.3.6.1.2.1.2.1", Module: "IF-MIB", IsScalar: true},
	}, newTableSet(), nil)

	if len(index.entries) != 0 {
		t.Errorf("catalog holds %d entries for objects that are never published: %+v",
			len(index.entries), index.entries)
	}
}

// TestCatalogEntryCarriesTheRowsIndexAndIdentity checks the two fields that
// tell a renderer how to label the lines of a panel.
func TestCatalogEntryCarriesTheRowsIndexAndIdentity(t *testing.T) {
	tables := newTableSet()
	tables.add(&TableIndexInfo{
		EntryOID:   "1.3.6.1.2.1.2.2.1",
		IndexNames: []string{"ifIndex"},
	}, nil)

	// Keyed the way the sweep keys it: normalized, with the leading dot.
	statusColumns := map[string][]statusColumnInfo{
		".1.3.6.1.2.1.2.2.1": {{Name: "ifDescr"}, {Name: "ifName"}, {Name: "ifAlias"}},
	}

	index := newCatalogIndex([]*mib.OIDMetadata{
		{
			OID: ".1.3.6.1.2.1.2.2.1.10", Name: "ifInOctets", Module: "IF-MIB",
			Type: "Counter32", IsColumn: true, EntryOID: "1.3.6.1.2.1.2.2.1",
		},
	}, tables, statusColumns)

	entry, found := index.entryFor("snmp_ifInOctets")
	if !found {
		t.Fatal("no entry")
	}
	if entry.IndexLabelsValue() != "ifindex" {
		t.Errorf("index labels = %q, want ifindex", entry.IndexLabelsValue())
	}

	// The name is not on the series -- an ordinary column carries only its
	// index -- so the catalog has to say which info metric to join.
	if entry.IdentityInfoMetric != "snmp_ifname_info" {
		t.Errorf("identity info metric = %q, want snmp_ifname_info", entry.IdentityInfoMetric)
	}
	if entry.IdentityLabel != "" {
		t.Errorf("identity label = %q, but an ordinary column carries no name label", entry.IdentityLabel)
	}
}

// TestCatalogEntryScalarHasNoRowToIdentify checks that a scalar is not given a
// row identity it does not have: it is one value for the device, and the
// device label already says which.
func TestCatalogEntryScalarHasNoRowToIdentify(t *testing.T) {
	index := newCatalogIndex([]*mib.OIDMetadata{
		{OID: ".1.3.6.1.2.1.1.3", Name: "sysUpTime", Module: "SNMPv2-MIB", Type: "TimeTicks", IsScalar: true},
	}, newTableSet(), nil)

	entry, _ := index.entryFor("snmp_sysUpTime")
	if entry.IndexLabelsValue() != "" || entry.IdentityInfoMetric != "" {
		t.Errorf("scalar entry = %+v, want no index or identity", entry)
	}
}

// TestDerivedCatalogEntriesDescribeWhatNoMIBCan is the correction that makes
// the catalog useful for the reading this collector exists to produce.
//
// Entity sensor readings are published under a name derived from their unit,
// so no sweep of the MIB store can describe them: there is no MIB object
// called entity_sensor_dbm. A catalog keyed on object name would document
// every interface counter and none of the optical power.
func TestDerivedCatalogEntriesDescribeWhatNoMIBCan(t *testing.T) {
	byMetric := map[string]*CatalogEntry{}
	for _, entry := range derivedCatalogEntries() {
		byMetric[entry.Metric] = entry
	}

	dbm, found := byMetric["snmp_entity_sensor_dbm"]
	if !found {
		t.Fatalf("no entry for snmp_entity_sensor_dbm; have %d entries", len(byMetric))
	}

	if dbm.Unit != "dBm" {
		t.Errorf("Unit = %q, want dBm", dbm.Unit)
	}
	if dbm.Kind != MetricKindGauge {
		t.Errorf("Kind = %q, want gauge", dbm.Kind)
	}
	// Its name IS on the series, unlike an ordinary column's.
	if dbm.IdentityLabel != labelEntityName {
		t.Errorf("IdentityLabel = %q, want %q", dbm.IdentityLabel, labelEntityName)
	}
	if dbm.IndexLabelsValue() != labelEntityIndex {
		t.Errorf("index labels = %q, want %q", dbm.IndexLabelsValue(), labelEntityIndex)
	}
	// Provenance is deliberately empty: two MIB objects on two tables publish
	// it, and naming either would be wrong on half the devices.
	if dbm.Object != "" || dbm.Module != "" {
		t.Errorf("entry claims provenance %q/%q, but two modules produce it", dbm.Module, dbm.Object)
	}
}

// TestDerivedCatalogEntriesCoverEveryPublishedSensorMetric checks that no
// sensor family is published without a catalog row to describe it.
func TestDerivedCatalogEntriesCoverEveryPublishedSensorMetric(t *testing.T) {
	described := map[string]bool{}
	for _, entry := range derivedCatalogEntries() {
		described[entry.Metric] = true
	}

	for sensorType := range sensorTypeNames {
		if metric := sensorType.MetricName(); !described[metric] {
			t.Errorf("%s is published for %s but has no catalog entry", metric, sensorType)
		}
	}
	// ...including the dimensionless family every undeclared type lands in.
	if !described[SensorType(9999).MetricName()] {
		t.Errorf("%s has no catalog entry", SensorType(9999).MetricName())
	}
}

// TestCatalogSamplesRenderOneRowPerMetric pins the wire shape.
func TestCatalogSamplesRenderOneRowPerMetric(t *testing.T) {
	when := time.Unix(1756600000, 0)
	samples := catalogSamples([]*CatalogEntry{
		{
			Metric: "snmp_ifOperStatus", Module: "IF-MIB", Object: "ifOperStatus",
			OID: ".1.3.6.1.2.1.2.2.1.8", Kind: MetricKindEnum,
			Description: "The current operational state.",
			IndexLabels: []string{"ifindex"},
			EnumValues:  map[int64]string{2: "down", 1: "up"},
		},
	}, when)

	if len(samples) != 3 {
		t.Fatalf("rendered %d samples, want one catalog row and two enum rows", len(samples))
	}

	row := samples[0]
	if row.Name != catalogMetricName {
		t.Errorf("name = %q, want %q", row.Name, catalogMetricName)
	}
	if row.Value != infoMetricValue {
		t.Errorf("value = %v, want %v; an info metric says nothing with its value", row.Value, infoMetricValue)
	}
	if !row.Timestamp.Equal(when) {
		t.Errorf("timestamp = %v, want the caller's", row.Timestamp)
	}

	want := map[string]string{
		labelCatalogMetric:      "snmp_ifOperStatus",
		labelCatalogModule:      "IF-MIB",
		labelCatalogObject:      "ifOperStatus",
		labelOID:                ".1.3.6.1.2.1.2.2.1.8",
		labelCatalogKind:        "enum",
		labelCatalogDescription: "The current operational state.",
		labelCatalogIndexLabels: "ifindex",
	}
	for name, value := range want {
		if row.Labels[name] != value {
			t.Errorf("label %s = %q, want %q", name, row.Labels[name], value)
		}
	}
	if len(row.Labels) != len(want) {
		t.Errorf("labels = %v, want exactly %v", row.Labels, want)
	}

	// The enumeration is rendered in ascending numeric order, so two
	// collectors given the same MIBs publish identical series.
	if samples[1].Labels[labelCatalogEnumValue] != "1" || samples[1].Labels[labelCatalogEnumLabel] != "up" {
		t.Errorf("first enum row = %v, want value 1 / up", samples[1].Labels)
	}
	if samples[2].Labels[labelCatalogEnumValue] != "2" {
		t.Errorf("second enum row = %v, want value 2", samples[2].Labels)
	}
	if samples[1].Name != catalogEnumMetricName {
		t.Errorf("enum row name = %q, want %q", samples[1].Name, catalogEnumMetricName)
	}
}

// TestCatalogSamplesOmitEmptyLabels checks that a missing unit or description
// is absent rather than empty. An empty Prometheus label cannot be told from
// an absent one, so writing it widens the series for nothing -- and unit is
// empty for four objects in five.
func TestCatalogSamplesOmitEmptyLabels(t *testing.T) {
	samples := catalogSamples([]*CatalogEntry{
		{Metric: "snmp_bare", Kind: MetricKindGauge},
	}, time.Unix(0, 0))

	if len(samples) != 1 {
		t.Fatalf("rendered %d samples, want one", len(samples))
	}
	for _, absent := range []string{
		labelCatalogModule, labelCatalogObject, labelOID, labelCatalogUnit,
		labelCatalogDescription, labelCatalogIdentity, labelCatalogIdentityInfo,
		labelCatalogIndexLabels,
	} {
		if _, present := samples[0].Labels[absent]; present {
			t.Errorf("label %s present but empty", absent)
		}
	}
	if samples[0].Labels[labelCatalogKind] != "gauge" {
		t.Errorf("kind = %q, want gauge; kind is never omitted", samples[0].Labels[labelCatalogKind])
	}
}

// TestCatalogCarriesNoDeviceLabel is the scaling invariant, and the reason the
// catalog is affordable at all.
//
// A device dimension would multiply 20,390 series by the fleet, so at a
// hundred thousand devices the description of a metric would cost more than
// the metric. Nothing here may vary per device.
func TestCatalogCarriesNoDeviceLabel(t *testing.T) {
	entries := append(derivedCatalogEntries(), &CatalogEntry{
		Metric: "snmp_ifInOctets", Module: "IF-MIB", Object: "ifInOctets",
		Kind: MetricKindCounter, IndexLabels: []string{"ifindex"},
		EnumValues: map[int64]string{1: "up"},
	})

	for _, sample := range catalogSamples(entries, time.Unix(0, 0)) {
		for name := range sample.Labels {
			if name == labelDevice {
				t.Fatalf("%s carries a device label: %v", sample.Name, sample.Labels)
			}
			if strings.Contains(name, "device") {
				t.Errorf("%s carries a device-ish label %q", sample.Name, name)
			}
		}
	}
}
