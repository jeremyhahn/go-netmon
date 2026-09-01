package metrics

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"

	"github.com/jeremyhahn/go-netmon/internal/mib"
	"github.com/jeremyhahn/go-netmon/internal/snmp"
)

// coverageMIB is a table with one of each kind of column the exporter treats
// differently, plus a text scalar. It is the smallest shape that exercises
// every publishing path.
func coverageMIB() []*mib.OIDMetadata {
	return []*mib.OIDMetadata{
		{
			OID: ".1.3.6.1.2.1.2.2", Name: "ifTable", Module: "IF-MIB",
			IsTable: true, ObjectClass: mib.ObjectClassTableNode,
		},
		{
			OID: ".1.3.6.1.2.1.2.2.1", Name: "ifEntry", Module: "IF-MIB", Parent: ".1.3.6.1.2.1.2.2",
			IsTableEntry: true, ObjectClass: mib.ObjectClassEntryNode,
			Index:        []string{"ifIndex"},
			IndexColumns: []mib.IndexColumn{{Name: "ifIndex", OID: ".1.3.6.1.2.1.2.2.1.1", Type: "Integer32", BaseType: "Integer32"}},
		},
		{
			OID: ".1.3.6.1.2.1.2.2.1.1", Name: "ifIndex", Module: "IF-MIB", Parent: ".1.3.6.1.2.1.2.2.1",
			Type: "Integer32", Access: "read-only", IsColumn: true,
			ObjectClass: mib.ObjectClassIndexColumn, EntryOID: ".1.3.6.1.2.1.2.2.1",
		},
		// A numeric column: published as a reading.
		{
			OID: ".1.3.6.1.2.1.2.2.1.10", Name: "ifInOctets", Module: "IF-MIB", Parent: ".1.3.6.1.2.1.2.2.1",
			Type: "Counter32", Access: "read-only", IsColumn: true, Units: "octets",
			Description: "Octets received.",
			ObjectClass: mib.ObjectClassMetricColumn, EntryOID: ".1.3.6.1.2.1.2.2.1",
		},
		// A text status column: published ONLY as an info metric.
		{
			OID: ".1.3.6.1.2.1.31.1.1.1.1", Name: "ifName", Module: "IF-MIB", Parent: ".1.3.6.1.2.1.2.2.1",
			Type: "DisplayString", Access: "read-only", IsColumn: true,
			Description: "The textual name of the interface.",
			ObjectClass: mib.ObjectClassStatusColumn, EntryOID: ".1.3.6.1.2.1.2.2.1",
		},
		// A numeric status column: published BOTH ways.
		{
			OID: ".1.3.6.1.2.1.2.2.1.8", Name: "ifOperStatus", Module: "IF-MIB", Parent: ".1.3.6.1.2.1.2.2.1",
			Type: "Integer32", Access: "read-only", IsColumn: true,
			Description: "The current operational state.",
			EnumValues:  map[int64]string{1: "up", 2: "down"},
			ObjectClass: mib.ObjectClassStatusColumn, EntryOID: ".1.3.6.1.2.1.2.2.1",
		},
		// A text scalar: published only as an info metric.
		{
			OID: ".1.3.6.1.4.1.21317.1.3.1.13.1.9.2", Name: "PSU2Status", Module: "ATEN-MIB",
			Type: "DisplayString", Access: "read-only", IsScalar: true,
			Description: "Power supply two's condition, in words.",
			ObjectClass: mib.ObjectClassScalar,
		},
	}
}

// coverageResults are the varbinds a device answers for coverageMIB.
func coverageResults() []*snmp.Result {
	when := time.Unix(1756600000, 0)
	return []*snmp.Result{
		{OID: ".1.3.6.1.2.1.2.2.1.10.1", Type: gosnmp.Counter32, Value: uint(4200), Timestamp: when},
		{OID: ".1.3.6.1.2.1.2.2.1.8.1", Type: gosnmp.Integer, Value: 1, Timestamp: when},
		{OID: ".1.3.6.1.2.1.31.1.1.1.1.1", Type: gosnmp.OctetString, Value: []byte("Ethernet1/3"), Timestamp: when},
		{OID: ".1.3.6.1.4.1.21317.1.3.1.13.1.9.2.0", Type: gosnmp.OctetString, Value: []byte("PSU2 is on"), Timestamp: when},
	}
}

// TestCatalogDescribesEveryPublishedFamily is the assertion that would have
// caught the catalog missing an entire category of metric.
//
// It converts real SNMP results of every shape the exporter treats
// differently -- a numeric column, a numeric status column, a text status
// column and a text scalar -- records what was actually emitted, and requires
// exactly one catalog row per emitted family.
//
// The catalog used to be keyed on snmp_<Name> only. Text scalars and status
// columns are published as snmp_<name>_info, so every one of them entered the
// published set, matched nothing and was silently counted as undescribed --
// while entries sat under names nothing publishes. The catalog looked complete
// and was systematically missing a whole category, which is precisely the
// failure this work exists to eliminate.
func TestCatalogDescribesEveryPublishedFamily(t *testing.T) {
	objects := coverageMIB()

	cache := &TableIndexCache{friendlyNameCache: make(map[string]string)}
	tables := newTableSet()
	tables.add(&TableIndexInfo{
		TableOID: ".1.3.6.1.2.1.2.2", EntryOID: ".1.3.6.1.2.1.2.2.1",
		IndexNames: []string{"ifIndex"},
		IndexColumns: []mib.IndexColumn{
			{Name: "ifIndex", OID: ".1.3.6.1.2.1.2.2.1.1", Type: "Integer32", BaseType: "Integer32"},
		},
	}, []string{".1.3.6.1.2.1.2.2.1.10", ".1.3.6.1.2.1.2.2.1.8", ".1.3.6.1.2.1.31.1.1.1.1"})

	statusColumns := map[string][]statusColumnInfo{
		".1.3.6.1.2.1.2.2.1": {
			{Name: "ifName", OID: ".1.3.6.1.2.1.31.1.1.1.1", ColumnID: "1", LabelName: "ifname"},
			{Name: "ifOperStatus", OID: ".1.3.6.1.2.1.2.2.1.8", ColumnID: "8", LabelName: "ifoperstatus"},
		},
	}

	cache.publishIndex(&mibSweep{
		names:         newObjectNameIndex(objects),
		tables:        tables,
		statusColumns: statusColumns,
		catalog:       newCatalogIndex(objects, tables, statusColumns),
	})

	// What the exporter actually publishes for those varbinds.
	samples := ConvertSNMPResultsWithTableIndexes("dist-01", coverageResults(), nil, cache, nil)
	if len(samples) == 0 {
		t.Fatal("the conversion produced no samples; the fixture is not exercising anything")
	}

	published := newPublishedMetrics()
	published.record(samples)

	emitted := published.snapshot()
	if len(emitted) < 4 {
		t.Fatalf("only %d families emitted (%v); the fixture should produce a reading, "+
			"an info metric for each status column and one for the text scalar", len(emitted), emitted)
	}

	// Every emitted family must have exactly one catalog row.
	exporter := &captureExporter{}
	emitter := newCatalogEmitter(exporter, cache, published)
	rows, stats := emitter.render()

	if stats.undescribed != 0 {
		t.Errorf("%d of the %d emitted families have no catalog row: %v",
			stats.undescribed, len(emitted), emitted)
	}

	described := map[string]int{}
	for _, row := range rows {
		if row.Name == catalogMetricName {
			described[row.Labels[labelCatalogMetric]]++
		}
	}
	for _, name := range emitted {
		switch described[name] {
		case 1:
		case 0:
			t.Errorf("%s is published and has no catalog row", name)
		default:
			t.Errorf("%s has %d catalog rows, want exactly one", name, described[name])
		}
	}
	// NOTE: the converse is deliberately NOT asserted. The catalog describes
	// every family the loaded MIBs can describe, including ones this process
	// has never published, because a designated emitter writes the fleet's
	// catalog and the fleet's other shards poll devices this one does not. A
	// row for an absent family costs a row; a missing row for a present family
	// costs a blank dashboard, and nothing reports it.
	_ = exporter
}

// TestCatalogKindMatchesHowTheFamilyIsPublished checks that an info family is
// described as a string whatever its object's underlying type.
//
// snmp_ifoperstatus_info is a constant 1 carrying "up" in a label. Describing
// it with the object's enum kind would have a renderer plot a line through the
// constant, and its unit and enumeration belong to the numeric family that
// carries the actual number.
func TestCatalogKindMatchesHowTheFamilyIsPublished(t *testing.T) {
	objects := coverageMIB()
	index := newCatalogIndex(objects, newTableSet(), nil)

	tests := []struct {
		metric   string
		wantKind MetricKind
		wantUnit string
	}{
		{"snmp_ifInOctets", MetricKindCounter, "octets"},
		{"snmp_ifOperStatus", MetricKindEnum, ""},
		{"snmp_ifoperstatus_info", MetricKindString, ""},
		{"snmp_ifname_info", MetricKindString, ""},
		{"snmp_psu2status_info", MetricKindString, ""},
	}

	for _, tt := range tests {
		t.Run(tt.metric, func(t *testing.T) {
			entry, found := index.entryFor(tt.metric)
			if !found {
				t.Fatalf("no catalog entry for %s", tt.metric)
			}
			if entry.Kind != tt.wantKind {
				t.Errorf("Kind = %q, want %q", entry.Kind, tt.wantKind)
			}
			if entry.Unit != tt.wantUnit {
				t.Errorf("Unit = %q, want %q", entry.Unit, tt.wantUnit)
			}
			if entry.Kind == MetricKindString && len(entry.EnumValues) > 0 {
				t.Errorf("an info family carries %d enum rows; the enumeration belongs to the number",
					len(entry.EnumValues))
			}
		})
	}

	// A text object never reaches the numeric path, so its numeric name is a
	// name nothing writes and the catalog must not claim it.
	for _, neverPublished := range []string{"snmp_ifName", "snmp_PSU2Status"} {
		if _, found := index.entryFor(neverPublished); found {
			t.Errorf("the catalog describes %s, which the exporter never publishes", neverPublished)
		}
	}
}

// TestCatalogDescribesTheIdentityMetricItPointsAt checks that
// identity_info_metric names a family the catalog can also describe.
//
// A renderer told to join snmp_ifname_info, only to find the catalog says
// nothing about it, is being sent somewhere it cannot follow.
func TestCatalogDescribesTheIdentityMetricItPointsAt(t *testing.T) {
	objects := coverageMIB()
	statusColumns := map[string][]statusColumnInfo{
		".1.3.6.1.2.1.2.2.1": {{Name: "ifName"}, {Name: "ifOperStatus"}},
	}
	index := newCatalogIndex(objects, newTableSet(), statusColumns)

	entry, found := index.entryFor("snmp_ifInOctets")
	if !found {
		t.Fatal("no entry for the numeric column")
	}
	if entry.IdentityInfoMetric == "" {
		t.Fatal("the column has status columns but no identity metric")
	}
	if _, described := index.entryFor(entry.IdentityInfoMetric); !described {
		t.Errorf("identity_info_metric points at %s, which the catalog cannot describe",
			entry.IdentityInfoMetric)
	}
}

var _ = strings.ToLower
var _ = context.Background

// TestCatalogAfterReloadWritesOnlyTheCurrentRow is the #9 case: a MIB reload
// that changes an object's metadata must not leave two live descriptions of
// one metric.
//
// Module, description, unit and identity are all labels, so changing any of
// them makes a different series. The superseded row stays queryable for as
// long as the consumer's lookback, which is how a consumer could see two rows
// with the same metric= and conflicting contracts.
//
// The producer's half of the contract is what this asserts: after a reload the
// emitter writes the new row and stops writing the old one, so the current row
// is always the one with the newest sample. The consumer's half -- resolve
// duplicates per metric= by newest sample -- is documented in
// docs/metrics/metric-catalog.md, and both halves are needed together: the
// lookback that buys resilience against a missed emission is the same lookback
// that keeps a superseded row visible.
func TestCatalogAfterReloadWritesOnlyTheCurrentRow(t *testing.T) {
	before := []*mib.OIDMetadata{{
		OID: ".1.3.6.1.2.1.2.2.1.10", Name: "ifInOctets", Module: "IF-MIB",
		Type: "Counter32", IsColumn: true, Units: "octets",
		Description: "Octets received.",
	}}
	after := []*mib.OIDMetadata{{
		OID: ".1.3.6.1.2.1.2.2.1.10", Name: "ifInOctets", Module: "IF-MIB-REVISED",
		Type: "Counter32", IsColumn: true, Units: "octets",
		Description: "The total number of octets received on the interface.",
	}}

	cache := &TableIndexCache{friendlyNameCache: make(map[string]string)}
	publish := func(objects []*mib.OIDMetadata) {
		cache.publishIndex(&mibSweep{
			names:         newObjectNameIndex(objects),
			tables:        newTableSet(),
			statusColumns: make(map[string][]statusColumnInfo),
			catalog:       newCatalogIndex(objects, newTableSet(), nil),
		})
	}

	published := newPublishedMetrics()
	published.record([]*Sample{{Name: "snmp_ifInOctets"}})

	exporter := &captureExporter{}
	emitter := newCatalogEmitter(exporter, cache, published)

	publish(before)
	emitter.emit(context.Background())

	publish(after)
	emitter.emit(context.Background())

	// The second emission must describe the metric once, with the new
	// metadata, and must not restate the old row.
	second := exporter.batches[len(exporter.batches)-1]
	rows := 0
	for _, sample := range second {
		if sample.Name != catalogMetricName || sample.Labels[labelCatalogMetric] != "snmp_ifInOctets" {
			continue
		}
		rows++
		if got := sample.Labels[labelCatalogModule]; got != "IF-MIB-REVISED" {
			t.Errorf("module = %q after the reload, want the new metadata", got)
		}
		if got := sample.Labels[labelCatalogDescription]; got != after[0].Description {
			t.Errorf("description = %q, want the new one", got)
		}
	}
	if rows != 1 {
		t.Fatalf("the emission carries %d rows for snmp_ifInOctets, want exactly 1", rows)
	}

	// And the superseded label set is genuinely no longer written, which is
	// what makes "newest sample wins" resolve to the current row.
	for _, sample := range second {
		if sample.Labels[labelCatalogModule] == "IF-MIB" {
			t.Error("the emission still writes the superseded row; newest-wins would not converge")
		}
	}
}
