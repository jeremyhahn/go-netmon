package promname_test

import (
	"strings"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"

	"github.com/jeremyhahn/go-netmon/internal/grafana/builder"
	"github.com/jeremyhahn/go-netmon/internal/metrics"
	"github.com/jeremyhahn/go-netmon/internal/mib"
	"github.com/jeremyhahn/go-netmon/internal/mib/storage"
	"github.com/jeremyhahn/go-netmon/internal/promname"
	"github.com/jeremyhahn/go-netmon/internal/snmp"
)

// The OIDs and objects the contract is checked against: one scalar, one table
// with a two-column index, and one column that no MIB describes.
const (
	testModule = "CONTRACT-TEST-MIB"

	ifNumberOID   = ".1.3.6.1.2.1.2.1"
	ifTableOID    = ".1.3.6.1.2.1.2.2"
	ifEntryOID    = ".1.3.6.1.2.1.2.2.1"
	ifIndexOID    = ".1.3.6.1.2.1.2.2.1.1"
	ifDescrOID    = ".1.3.6.1.2.1.2.2.1.2"
	ifInOctetsOID = ".1.3.6.1.2.1.2.2.1.10"

	bgpTableOID  = ".1.3.6.1.4.1.9.9.187.1.2.5"
	bgpEntryOID  = ".1.3.6.1.4.1.9.9.187.1.2.5.1"
	bgpTypeOID   = ".1.3.6.1.4.1.9.9.187.1.2.5.1.1"
	bgpAddrOID   = ".1.3.6.1.4.1.9.9.187.1.2.5.1.3"
	bgpStateOID  = ".1.3.6.1.4.1.9.9.187.1.2.5.1.12"
	bgpPrefixOID = ".1.3.6.1.4.1.9.9.187.1.2.5.1.10"

	undescribedOID = ".1.3.6.1.4.1.9.9.999.1.2.3.4"

	// A scalar reporting its condition as text rather than a number. Vendors of
	// small appliances do this for everything worth knowing, so the naming
	// contract has to cover it as well as the status-column case.
	psuStatusOID = ".1.3.6.1.4.1.21317.1.3.1.13.1.9.2"
)

// contractMIB returns the objects the exporter and every consumer are checked
// against. It is a real MIB shape: a scalar, an ifTable indexed by one integer
// column, and a Cisco BGP peer table indexed by two columns, one of them an
// address whose decoded value is what a dashboard picks a row by.
func contractMIB() []*mib.OIDMetadata {
	return []*mib.OIDMetadata{
		{
			OID: ifNumberOID, Name: "ifNumber", Module: testModule,
			Type: "Integer32", Access: "read-only", IsScalar: true, Pollable: true,
			ObjectClass: mib.ObjectClassScalar, IsChartable: true, IsDisplayable: true,
		},
		{
			OID: psuStatusOID, Name: "PSU2Status", Module: testModule,
			Type: "DisplayString", Access: "read-only", IsScalar: true, Pollable: true,
			ObjectClass: mib.ObjectClassScalar, IsDisplayable: true,
		},
		{
			OID: ifTableOID, Name: "ifTable", Module: testModule,
			Access: "not-accessible", IsTable: true, ObjectClass: mib.ObjectClassTableNode,
		},
		{
			OID: ifEntryOID, Name: "ifEntry", Module: testModule, Parent: ifTableOID,
			Access: "not-accessible", IsTableEntry: true, ObjectClass: mib.ObjectClassEntryNode,
			Index:        []string{"ifIndex"},
			IndexColumns: []mib.IndexColumn{{Name: "ifIndex", OID: ifIndexOID, Type: "InterfaceIndex", BaseType: "Integer32"}},
		},
		{
			OID: ifIndexOID, Name: "ifIndex", Module: testModule, Parent: ifEntryOID,
			Type: "InterfaceIndex", Access: "read-only", IsColumn: true, IsIndex: true,
			ObjectClass: mib.ObjectClassIndexColumn, TableOID: ifTableOID, EntryOID: ifEntryOID,
		},
		{
			OID: ifDescrOID, Name: "ifDescr", Module: testModule, Parent: ifEntryOID,
			Type: "DisplayString", Access: "read-only", IsColumn: true, Pollable: true,
			ObjectClass: mib.ObjectClassStatusColumn, TableOID: ifTableOID, EntryOID: ifEntryOID,
			IsDisplayable: true,
		},
		{
			OID: ifInOctetsOID, Name: "ifInOctets", Module: testModule, Parent: ifEntryOID,
			Type: "Counter32", Access: "read-only", IsColumn: true, Pollable: true,
			ObjectClass: mib.ObjectClassMetricColumn, TableOID: ifTableOID, EntryOID: ifEntryOID,
			IsChartable: true,
		},
		{
			OID: bgpTableOID, Name: "cbgpPeer2Table", Module: testModule,
			Access: "not-accessible", IsTable: true, ObjectClass: mib.ObjectClassTableNode,
		},
		{
			OID: bgpEntryOID, Name: "cbgpPeer2Entry", Module: testModule, Parent: bgpTableOID,
			Access: "not-accessible", IsTableEntry: true, ObjectClass: mib.ObjectClassEntryNode,
			Index: []string{"cbgpPeer2Type", "cbgpPeer2RemoteAddr"},
			IndexColumns: []mib.IndexColumn{
				{Name: "cbgpPeer2Type", OID: bgpTypeOID, Type: "InetAddressType", BaseType: "Integer32"},
				{Name: "cbgpPeer2RemoteAddr", OID: bgpAddrOID, Type: "InetAddress", BaseType: "OctetString"},
			},
		},
		{
			OID: bgpTypeOID, Name: "cbgpPeer2Type", Module: testModule, Parent: bgpEntryOID,
			Type: "InetAddressType", Access: "not-accessible", IsColumn: true, IsIndex: true,
			ObjectClass: mib.ObjectClassIndexColumn, TableOID: bgpTableOID, EntryOID: bgpEntryOID,
		},
		{
			OID: bgpAddrOID, Name: "cbgpPeer2RemoteAddr", Module: testModule, Parent: bgpEntryOID,
			Type: "InetAddress", Access: "not-accessible", IsColumn: true, IsIndex: true,
			ObjectClass: mib.ObjectClassIndexColumn, TableOID: bgpTableOID, EntryOID: bgpEntryOID,
		},
		{
			OID: bgpStateOID, Name: "cbgpPeer2State", Module: testModule, Parent: bgpEntryOID,
			Type: "INTEGER", Access: "read-only", IsColumn: true, Pollable: true,
			ObjectClass: mib.ObjectClassStatusColumn, TableOID: bgpTableOID, EntryOID: bgpEntryOID,
			EnumValues:    map[int64]string{1: "idle", 6: "established"},
			IsDisplayable: true,
		},
		{
			OID: bgpPrefixOID, Name: "cbgpPeer2FsmEstablishedTransitions", Module: testModule,
			Parent: bgpEntryOID, Type: "Counter32", Access: "read-only", IsColumn: true, Pollable: true,
			ObjectClass: mib.ObjectClassMetricColumn, TableOID: bgpTableOID, EntryOID: bgpEntryOID, IsChartable: true,
		},
	}
}

// contractStore returns a MIB store holding contractMIB.
func contractStore(t *testing.T) *storage.Storage {
	t.Helper()

	store, err := storage.New(t.TempDir())
	if err != nil {
		t.Fatalf("open MIB storage: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := store.Close(); closeErr != nil {
			t.Errorf("close MIB storage: %v", closeErr)
		}
	})

	if err := store.StoreModule(&mib.ModuleInfo{Name: testModule}); err != nil {
		t.Fatalf("store module: %v", err)
	}
	for _, object := range contractMIB() {
		if err := store.StoreOID(object); err != nil {
			t.Fatalf("store OID %s: %v", object.OID, err)
		}
	}
	return store
}

// exported runs the real export path over instanceOIDs and returns the samples
// it produced, keyed by instance OID. This is the exporter's own answer -- not a
// reimplementation of it -- which is what makes it usable as the fixed point
// every consumer is compared against.
func exported(t *testing.T, store *storage.Storage, instanceOIDs ...string) map[string]*metrics.Sample {
	t.Helper()

	results := make([]*snmp.Result, 0, len(instanceOIDs))
	for _, oid := range instanceOIDs {
		results = append(results, &snmp.Result{
			OID:       oid,
			Type:      gosnmp.Counter32,
			Value:     uint(1),
			Timestamp: time.Unix(0, 0),
		})
	}

	samples := metrics.ConvertSNMPResultsWithTableIndexes(
		"contract-device", results, nil, metrics.NewTableIndexCache(store), nil)

	byOID := make(map[string]*metrics.Sample, len(samples))
	for _, sample := range samples {
		if oid := sample.Labels[promname.OIDLabel]; oid != "" {
			byOID[oid] = sample
		}
	}

	for _, oid := range instanceOIDs {
		if byOID[oid] == nil {
			t.Fatalf("exporter produced no sample for instance OID %s", oid)
		}
	}
	return byOID
}

// TestMetricNameMatchesExporter is the drift gate between the exporter and
// every consumer that has to name a series it did not produce.
//
// Each case names an object through promname -- the single derivation the
// dashboard generators, query builders and CLI all go through -- and compares
// it with the name the exporter actually gave a live sample of that object.
// Both sides have to change together or this fails, which is exactly what the
// previous arrangement could not do: every consumer asserted against its own
// local "snmp__" helper, so all of them stayed green while the exporter stopped
// emitting those names and every dashboard panel went blank.
func TestMetricNameMatchesExporter(t *testing.T) {
	store := contractStore(t)

	tests := []struct {
		name       string
		instance   string // instance OID as the poller reports it
		objectName string // the MIB object that instance instantiates
	}{
		{"scalar", ifNumberOID + ".0", "ifNumber"},
		{"table column", ifInOctetsOID + ".10", "ifInOctets"},
		{"same column, another row", ifInOctetsOID + ".20178", "ifInOctets"},
		{"mixed case object name", bgpPrefixOID + ".1.4.203.0.113.202", "cbgpPeer2FsmEstablishedTransitions"},
	}

	instances := make([]string, 0, len(tests))
	for _, tt := range tests {
		instances = append(instances, tt.instance)
	}
	samples := exported(t, store, instances...)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want := samples[tt.instance].Name

			if got := promname.MetricName(tt.objectName); got != want {
				t.Errorf("promname.MetricName(%q) = %q, exporter emitted %q",
					tt.objectName, got, want)
			}
			if !strings.HasPrefix(want, promname.Prefix) {
				t.Errorf("exporter name %q does not start with promname.Prefix %q", want, promname.Prefix)
			}
		})
	}
}

// TestQueryBuilderNamesMatchExporter pins the Grafana query builder's output to
// the exporter. A panel whose selector names a metric the exporter never emits
// draws nothing, and nothing else in the suite notices.
func TestQueryBuilderNamesMatchExporter(t *testing.T) {
	store := contractStore(t)
	samples := exported(t, store, ifNumberOID+".0", ifInOctetsOID+".10")

	qb := builder.NewQueryBuilder("")

	tests := []struct {
		name     string
		object   *mib.OIDMetadata
		instance string
	}{
		{
			name:     "scalar",
			object:   &mib.OIDMetadata{OID: ifNumberOID, Name: "ifNumber", Type: "Integer32", IsScalar: true},
			instance: ifNumberOID + ".0",
		},
		{
			name:     "counter column",
			object:   &mib.OIDMetadata{OID: ifInOctetsOID, Name: "ifInOctets", Type: "Counter32", IsColumn: true},
			instance: ifInOctetsOID + ".10",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want := samples[tt.instance].Name

			for label, query := range map[string]string{
				"BuildQuery":            qb.BuildQuery(tt.object),
				"BuildTableColumnQuery": qb.BuildTableColumnQuery(tt.object),
				"BuildRateQuery":        qb.BuildRateQuery(tt.object, "5m"),
				"BuildColumnQuery":      qb.BuildColumnQuery(tt.object, true, true).Expr,
			} {
				if !strings.Contains(query, want+"{") {
					t.Errorf("%s = %q, does not select the exporter's metric %q", label, query, want)
				}
				if strings.Contains(query, "__name__") {
					t.Errorf("%s = %q, still selects on __name__; the row index is a label now", label, query)
				}
			}
		})
	}
}

// The web UI's half of this gate moved, because the drift it guarded can no
// longer happen here.
//
// It used to generate a dashboard specification from this MIB store and check
// that every widget named a metric the exporter emits. A panel's metric name is
// no longer derived from a MIB object at all: it is a name Prometheus told the
// server it is holding for that device, so a panel naming a metric nothing
// publishes cannot be constructed.
//
// What CAN still drift is the identity join a panel query is built around --
// the catalog declares which index labels to match on, and if those disagree
// with the labels the exporter attaches, the join matches nothing and every
// line on the panel loses its name. That gate is
// TestCatalogIndexLabelsMatchTheLabelsTheExporterAttaches in internal/metrics,
// which is where the catalog is built and so where the two sides can be
// compared without a second implementation of either.

// TestIndexLabelsMatchExporter pins the label names a consumer selects a table
// row by to the labels the exporter actually attaches.
//
// This is the other half of the contract: naming the metric right and then
// filtering on a label that does not exist selects nothing, which is
// indistinguishable from the device not reporting the object at all.
func TestIndexLabelsMatchExporter(t *testing.T) {
	store := contractStore(t)
	samples := exported(t, store, ifInOctetsOID+".10", bgpPrefixOID+".1.4.203.0.113.202")

	tests := []struct {
		name        string
		instance    string
		indexColumn string
		wantValue   string
	}{
		{"integer index", ifInOctetsOID + ".10", "ifIndex", "10"},
		{"address index", bgpPrefixOID + ".1.4.203.0.113.202", "cbgpPeer2RemoteAddr", "203.0.113.202"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			label := promname.IndexLabel(tt.indexColumn)

			value, found := samples[tt.instance].Labels[label]
			if !found {
				t.Fatalf("exporter attached no %q label to a sample of %s; it emitted %v",
					label, tt.instance, samples[tt.instance].Labels)
			}
			if value != tt.wantValue {
				t.Errorf("label %s = %q, want %q", label, value, tt.wantValue)
			}
		})
	}
}

// TestRawOIDNameMatchesExporterFallback pins the one case that still carries an
// OID in its metric name: an instance no loaded MIB describes. The CLI decodes
// these back to an OID, so the two have to agree.
func TestRawOIDNameMatchesExporterFallback(t *testing.T) {
	store := contractStore(t)
	instance := undescribedOID + ".7"
	samples := exported(t, store, instance)

	want := samples[instance].Name
	if got := promname.RawOIDMetricName(instance); got != want {
		t.Errorf("promname.RawOIDMetricName(%q) = %q, exporter emitted %q", instance, got, want)
	}
	if !strings.HasPrefix(want, promname.Prefix+"_") {
		t.Errorf("exporter fallback name %q is not the raw-OID form", want)
	}
}

// TestInfoMetricNameMatchesExporter pins the other name the exporter emits: a
// status column's value is not a number, so it is published as a label on a
// constant-1 series named after the column.
func TestInfoMetricNameMatchesExporter(t *testing.T) {
	store := contractStore(t)

	samples := metrics.ConvertSNMPResultsWithTableIndexes(
		"contract-device",
		[]*snmp.Result{{
			OID:       bgpStateOID + ".1.4.203.0.113.202",
			Type:      gosnmp.Integer,
			Value:     6,
			Timestamp: time.Unix(0, 0),
		}},
		nil, metrics.NewTableIndexCache(store), nil)

	var info *metrics.Sample
	for _, sample := range samples {
		if strings.HasSuffix(sample.Name, "_info") {
			info = sample
			break
		}
	}
	if info == nil {
		t.Fatalf("exporter emitted no info metric for a status column; it emitted %v", samples)
	}

	if got := promname.InfoMetricName("cbgpPeer2State"); got != info.Name {
		t.Errorf("promname.InfoMetricName(\"cbgpPeer2State\") = %q, exporter emitted %q", got, info.Name)
	}
	if got, want := info.Labels["value"], "established"; got != want {
		t.Errorf("info metric value label = %q, want %q", got, want)
	}
}

// TestInfoMetricNameMatchesExporterForATextScalar extends the contract to the
// other kind of info metric.
//
// A scalar whose reading is text used to be dropped by the exporter entirely, so
// there was nothing for a consumer's name to match and nothing to notice: a
// missing metric and an empty one look the same from a query. Now that the
// exporter emits one, the two sides have to keep agreeing on its name.
func TestInfoMetricNameMatchesExporterForATextScalar(t *testing.T) {
	store := contractStore(t)

	samples := metrics.ConvertSNMPResultsWithTableIndexes(
		"contract-device",
		[]*snmp.Result{{
			OID:       psuStatusOID + ".0",
			Type:      gosnmp.OctetString,
			Value:     []byte("PSU2 is abnormal, please check the device"),
			Timestamp: time.Unix(0, 0),
		}},
		nil, metrics.NewTableIndexCache(store), nil)

	var info *metrics.Sample
	for _, sample := range samples {
		if strings.HasSuffix(sample.Name, "_info") && sample.Labels["value"] != "" {
			info = sample
			break
		}
	}
	if info == nil {
		t.Fatalf("the exporter dropped a text scalar; it emitted %v", samples)
	}

	if got := promname.InfoMetricName("PSU2Status"); got != info.Name {
		t.Errorf("promname.InfoMetricName(\"PSU2Status\") = %q, exporter emitted %q", got, info.Name)
	}
	if got, want := info.Labels["value"], "PSU2 is abnormal, please check the device"; got != want {
		t.Errorf("value label = %q, want the device's own words %q", got, want)
	}
}

// Entity sensor OIDs used by the sensor half of the contract: one Nexus
// transceiver, named through ENTITY-MIB.
//
// The encoding is the one walked from dist-01 on 2026-08-31 -- dBm at scale
// milli(8) with precision 0, so -7.328 dBm crosses the wire as -7328 -- so no
// fixture in the tree asserts against an encoding no device produces.
const (
	sensorValueOID     = ".1.3.6.1.4.1.9.9.91.1.1.1.1.4.3001"
	sensorTypeOID      = ".1.3.6.1.4.1.9.9.91.1.1.1.1.1.3001"
	sensorScaleOID     = ".1.3.6.1.4.1.9.9.91.1.1.1.1.2.3001"
	sensorPrecisionOID = ".1.3.6.1.4.1.9.9.91.1.1.1.1.3.3001"
	sensorStatusOID    = ".1.3.6.1.4.1.9.9.91.1.1.1.1.5.3001"
	physicalNameOID    = ".1.3.6.1.2.1.47.1.1.1.1.7.3001"

	entitySensorType = 14 // dBm
)

// exportedSensor runs the real export path over one complete entity sensor row
// and returns the scaled sample it produced.
//
// It goes through ConvertSNMPResultsWithTableIndexes for the same reason
// exported does: the contract has to be pinned to what the exporter actually
// emits, not to a second implementation of the naming that could drift with it.
func exportedSensor(t *testing.T) *metrics.Sample {
	t.Helper()

	integer := func(oid string, value int) *snmp.Result {
		return &snmp.Result{OID: oid, Type: gosnmp.Integer, Value: value, Timestamp: time.Unix(0, 0)}
	}

	results := []*snmp.Result{
		integer(sensorTypeOID, entitySensorType),
		integer(sensorScaleOID, 8), // milli
		integer(sensorPrecisionOID, 0),
		integer(sensorValueOID, -7328), // -7.328 dBm
		integer(sensorStatusOID, 1),    // ok
		{OID: physicalNameOID, Type: gosnmp.OctetString,
			Value: []byte("Ethernet1/3 Lane 1 Transceiver Receive Power Sensor"), Timestamp: time.Unix(0, 0)},
	}

	samples := metrics.ConvertSNMPResultsWithTableIndexes(
		"contract-device", results, nil, metrics.NewTableIndexCache(nil), nil)

	for _, sample := range samples {
		if sample.Labels[promname.OIDLabel] == sensorValueOID {
			return sample
		}
	}

	t.Fatalf("exporter produced no sample for the sensor value column %s", sensorValueOID)
	return nil
}

// TestEntitySensorMetricNameMatchesExporter is the drift gate for the one
// metric family whose name is not derived from a MIB object name.
//
// A consumer cannot reach an entity sensor reading through MetricName: the
// exporter names it after the unit it scaled the reading into, not after
// entSensorValue. If these two derivations drift, every optical power panel
// goes blank in exactly the way the rest of this contract exists to prevent.
func TestEntitySensorMetricNameMatchesExporter(t *testing.T) {
	sample := exportedSensor(t)

	if got := promname.EntitySensorMetricName(entitySensorType); got != sample.Name {
		t.Errorf("EntitySensorMetricName(dBm) = %q, exporter emitted %q", got, sample.Name)
	}
}

// TestEntitySensorLabelsMatchExporter pins the labels a consumer has to filter
// and join an entity sensor reading by.
func TestEntitySensorLabelsMatchExporter(t *testing.T) {
	sample := exportedSensor(t)

	tests := []struct {
		name  string
		label string
		want  string
	}{
		{"device", promname.DeviceLabel, "contract-device"},
		{"instance OID", promname.OIDLabel, sensorValueOID},
		{"entity index", promname.EntityIndexLabel, "3001"},
		{"entity name", promname.EntityNameLabel, "Ethernet1/3 Lane 1 Transceiver Receive Power Sensor"},
		{"sensor type", promname.SensorTypeLabel, "dBm"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sample.Labels[tt.label]; got != tt.want {
				t.Errorf("label %s = %q, want %q", tt.label, got, tt.want)
			}
		})
	}
}

// TestEntitySensorIndexLabelsFollowTheIndexConvention checks that the two
// entity labels are what IndexLabel derives from their MIB objects, so a
// consumer that reaches them either way arrives at the same string.
func TestEntitySensorIndexLabelsFollowTheIndexConvention(t *testing.T) {
	if got := promname.IndexLabel("entPhysicalIndex"); got != promname.EntityIndexLabel {
		t.Errorf("IndexLabel(entPhysicalIndex) = %q, EntityIndexLabel = %q", got, promname.EntityIndexLabel)
	}
	if got := promname.IndexLabel("entPhysicalName"); got != promname.EntityNameLabel {
		t.Errorf("IndexLabel(entPhysicalName) = %q, EntityNameLabel = %q", got, promname.EntityNameLabel)
	}
}

// TestEntitySensorMetricNameCoversEveryType checks that every sensor type a
// consumer might ask about resolves to a name the exporter could emit, and
// that an undeclared type does not produce an unqueryable name.
func TestEntitySensorMetricNameCoversEveryType(t *testing.T) {
	tests := []struct {
		name       string
		sensorType int32
		want       string
	}{
		{"dBm", 14, "snmp_entity_sensor_dbm"},
		{"celsius", 8, "snmp_entity_sensor_celsius"},
		{"voltsDC", 4, "snmp_entity_sensor_volts"},
		{"amperes", 5, "snmp_entity_sensor_amperes"},
		{"undeclared", 99, "snmp_entity_sensor_value"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := promname.EntitySensorMetricName(tt.sensorType); got != tt.want {
				t.Errorf("EntitySensorMetricName(%d) = %q, want %q", tt.sensorType, got, tt.want)
			}
			if !strings.HasPrefix(tt.want, promname.Prefix) {
				t.Errorf("%q does not carry the exporter's prefix %q", tt.want, promname.Prefix)
			}
		})
	}
}

// TestIdentityLabelMatchesTheLabelTheExporterAttaches pins the derivation a
// panel names its lines by.
//
// The catalog says an interface row's identity lives on snmp_ifdescr_info. A
// consumer holding the reading does not have to go and get it: the exporter
// promotes a table's status columns onto its own readings, so the same value is
// already there as ifdescr. promname.IdentityLabel is the step from the one
// name to the other, and it is exact rather than a guess only because both are
// the column name lowercased.
//
// If it drifts, every line of every table panel silently falls back to being
// named after the panel -- forty interfaces all called "IF-MIB - ifInOctets".
func TestIdentityLabelMatchesTheLabelTheExporterAttaches(t *testing.T) {
	store := contractStore(t)

	// The status column has to be in the same conversion as the reading: the
	// exporter promotes a column's value onto its siblings from what it just
	// polled, so asking for the reading alone produces no name to promote.
	samples := metrics.ConvertSNMPResultsWithTableIndexes(
		"contract-device",
		[]*snmp.Result{
			{OID: ifDescrOID + ".10", Type: gosnmp.OctetString,
				Value: []byte("GigabitEthernet0/0"), Timestamp: time.Unix(0, 0)},
			{OID: ifInOctetsOID + ".10", Type: gosnmp.Counter32,
				Value: uint(4200), Timestamp: time.Unix(0, 0)},
		},
		nil, metrics.NewTableIndexCache(store), nil)

	var reading *metrics.Sample
	for _, sample := range samples {
		if sample.Name == promname.MetricName("ifInOctets") {
			reading = sample
			break
		}
	}
	if reading == nil {
		t.Fatalf("the exporter emitted no reading for ifInOctets; it emitted %v", samples)
	}

	label := promname.IdentityLabel(promname.InfoMetricName("ifDescr"))

	// The derivation has to land on the label naming convention, not near it.
	if want := promname.IndexLabel("ifDescr"); label != want {
		t.Errorf("IdentityLabel(InfoMetricName(\"ifDescr\")) = %q, want %q", label, want)
	}
	// And that label has to be one the exporter really attached.
	if got, want := reading.Labels[label], "GigabitEthernet0/0"; got != want {
		t.Errorf("the reading's %q label = %q, want %q; it carries %v",
			label, got, want, reading.Labels)
	}
}

// TestIdentityLabelRefusesANameItCannotDerive covers the input that is not an
// info metric at all.
//
// A catalog row with no identity metric leaves the field empty, and a renderer
// must not then be handed "" as a label to read -- every series carries no
// label called "", so it would match nothing while looking like an answer.
func TestIdentityLabelRefusesANameItCannotDerive(t *testing.T) {
	for _, input := range []string{"", "snmp_ifInOctets", "ifdescr_info", "not a metric"} {
		if got := promname.IdentityLabel(input); got != "" {
			t.Errorf("IdentityLabel(%q) = %q, want empty: it is not an info metric name",
				input, got)
		}
	}
}
