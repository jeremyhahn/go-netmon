package metrics

import (
	"testing"
	"time"

	"github.com/jeremyhahn/go-netmon/internal/mib"
	"github.com/jeremyhahn/go-netmon/internal/mib/storage"
	"github.com/jeremyhahn/go-netmon/internal/snmp"
)

// bgpPeerTable is the cbgpPeer2Table cut down to the columns these tests need:
// an index, a textual status column, an enumerated one, and cbgpPeer2LastError,
// whose SYNTAX is OCTET STRING (SIZE (2)) holding a BGP error code and subcode.
var bgpPeerTable = []*mib.OIDMetadata{
	{
		OID:          "1.3.6.1.4.1.9.9.187.1.2.5.1",
		Name:         "cbgpPeer2Entry",
		Module:       "CISCO-BGP4-MIB",
		Parent:       "1.3.6.1.4.1.9.9.187.1.2.5",
		IsTableEntry: true,
		Index:        []string{"cbgpPeer2RemoteAddr"},
	},
	{
		OID:         "1.3.6.1.4.1.9.9.187.1.2.5.1.1",
		Name:        "cbgpPeer2RemoteAddr",
		Module:      "CISCO-BGP4-MIB",
		Parent:      "1.3.6.1.4.1.9.9.187.1.2.5.1",
		IsColumn:    true,
		Type:        "Integer32",
		ObjectClass: mib.ObjectClassIndexColumn,
	},
	{
		OID:         "1.3.6.1.4.1.9.9.187.1.2.5.1.28",
		Name:        "cbgpPeer2LastError",
		Module:      "CISCO-BGP4-MIB",
		Parent:      "1.3.6.1.4.1.9.9.187.1.2.5.1",
		IsColumn:    true,
		Type:        "OCTET STRING",
		ObjectClass: mib.ObjectClassStatusColumn,
	},
	{
		OID:         "1.3.6.1.4.1.9.9.187.1.2.5.1.3",
		Name:        "cbgpPeer2State",
		Module:      "CISCO-BGP4-MIB",
		Parent:      "1.3.6.1.4.1.9.9.187.1.2.5.1",
		IsColumn:    true,
		Type:        "INTEGER",
		ObjectClass: mib.ObjectClassStatusColumn,
		EnumValues:  map[int64]string{1: "idle", 6: "established"},
	},
	{
		OID:         "1.3.6.1.4.1.9.9.187.1.2.5.1.29",
		Name:        "cbgpPeer2Description",
		Module:      "CISCO-BGP4-MIB",
		Parent:      "1.3.6.1.4.1.9.9.187.1.2.5.1",
		IsColumn:    true,
		Type:        "OCTET STRING",
		ObjectClass: mib.ObjectClassStatusColumn,
	},
	{
		// A hinted binary column. Its octets are printable, so the content
		// rule alone would call them text: only the hint says otherwise,
		// which is what makes it prove the hint is still consulted first.
		OID:         "1.3.6.1.4.1.9.9.187.1.2.5.1.30",
		Name:        "cbgpPeer2LocalIdentifier",
		Module:      "CISCO-BGP4-MIB",
		Parent:      "1.3.6.1.4.1.9.9.187.1.2.5.1",
		IsColumn:    true,
		Type:        "OCTET STRING",
		DisplayHint: "1x:",
		ObjectClass: mib.ObjectClassStatusColumn,
	},
}

const (
	lastErrorOID   = ".1.3.6.1.4.1.9.9.187.1.2.5.1.28.1"
	peerStateOID   = ".1.3.6.1.4.1.9.9.187.1.2.5.1.3.1"
	descriptionOID = ".1.3.6.1.4.1.9.9.187.1.2.5.1.29.1"
	identifierOID  = ".1.3.6.1.4.1.9.9.187.1.2.5.1.30.1"
)

// newBGPCache builds an index cache over bgpPeerTable held in a real MIB store,
// so the tests exercise the same sweep the exporter runs against.
func newBGPCache(t *testing.T) *TableIndexCache {
	t.Helper()

	store, err := storage.New(t.TempDir())
	if err != nil {
		t.Fatalf("create storage: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	for _, object := range bgpPeerTable {
		if err := store.StoreOID(object); err != nil {
			t.Fatalf("store %s: %v", object.Name, err)
		}
	}

	return NewTableIndexCache(store)
}

// infoLabel returns the "value" label of the single info metric produced for
// oid, failing the test when the exporter produced anything else.
func infoLabel(t *testing.T, cache *TableIndexCache, oid string, value any) string {
	t.Helper()

	results := []*snmp.Result{{OID: oid, Value: value, Timestamp: time.Now()}}
	samples := ConvertStatusColumnsToInfoMetrics("router1", results, nil, cache, time.Now())
	if len(samples) != 1 {
		t.Fatalf("expected 1 info metric for %s, got %d", oid, len(samples))
	}
	return samples[0].Labels["value"]
}

// TestInfoMetricLabelFormatsBinaryOctetStrings is the regression test for the
// defect: cbgpPeer2LastError is a two-octet binary pair, and the exporter used
// to put its raw bytes into a Prometheus label, so "Hold Timer Expired" reached
// the web UI as two invisible control characters.
func TestInfoMetricLabelFormatsBinaryOctetStrings(t *testing.T) {
	cache := newBGPCache(t)

	tests := []struct {
		name  string
		value any
		want  string
	}{
		{name: "no error", value: []byte{0x00, 0x00}, want: "00:00"},
		{name: "hold timer expired", value: []byte{0x04, 0x00}, want: "04:00"},
		{name: "notification with subcode", value: []byte{0x06, 0x02}, want: "06:02"},
		{name: "value arriving as a string", value: "\x04\x00", want: "04:00"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := infoLabel(t, cache, lastErrorOID, tt.value); got != tt.want {
				t.Errorf("value label = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestInfoMetricLabelKeepsTextualColumnsTextual checks the fix did not turn
// readable status columns into hex: padded text is still text, a DISPLAY-HINT
// still drives the rendering, and enumerations keep the bare label that is part
// of their series identity.
func TestInfoMetricLabelKeepsTextualColumnsTextual(t *testing.T) {
	cache := newBGPCache(t)

	tests := []struct {
		name  string
		oid   string
		value any
		want  string
	}{
		{name: "plain text", oid: descriptionOID, value: []byte("transit peer"), want: "transit peer"},
		{name: "NUL padded text", oid: descriptionOID, value: []byte("transit\x00\x00"), want: "transit"},
		{name: "declared DISPLAY-HINT still wins", oid: identifierOID, value: []byte("eth0"), want: "65:74:68:30"},
		{name: "enumeration", oid: peerStateOID, value: 6, want: "established"},
		{name: "enumeration outside the MIB", oid: peerStateOID, value: 99, want: "99"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := infoLabel(t, cache, tt.oid, tt.value); got != tt.want {
				t.Errorf("value label = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestInfoMetricLabelMatchesFormatValue pins the exporter's label path to the
// shared formatter so the two cannot drift into two renderings of one value.
func TestInfoMetricLabelMatchesFormatValue(t *testing.T) {
	cache := newBGPCache(t)

	lastError := bgpPeerTable[2]
	description := bgpPeerTable[4]
	identifier := bgpPeerTable[5]

	tests := []struct {
		name  string
		oid   string
		meta  *mib.OIDMetadata
		value any
	}{
		{name: "all NUL error code", oid: lastErrorOID, meta: lastError, value: []byte{0x00, 0x00}},
		{name: "hold timer expired", oid: lastErrorOID, meta: lastError, value: []byte{0x04, 0x00}},
		{name: "high bit blob", oid: lastErrorOID, meta: lastError, value: []byte{0xde, 0xad}},
		{name: "padded text", oid: descriptionOID, meta: description, value: []byte("transit\x00")},
		{name: "plain text", oid: descriptionOID, meta: description, value: []byte("transit peer")},
		{name: "hinted value", oid: identifierOID, meta: identifier, value: []byte("eth0")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want := mib.FormatValue(tt.meta, tt.value)
			if got := infoLabel(t, cache, tt.oid, tt.value); got != want {
				t.Errorf("exporter label = %q, mib.FormatValue = %q", got, want)
			}
		})
	}
}

// TestInfoMetricLabelOnUnusableValues checks the exporter emits nothing rather
// than a bogus label when a result carries no value it can render.
func TestInfoMetricLabelOnUnusableValues(t *testing.T) {
	cache := newBGPCache(t)

	tests := []struct {
		name  string
		value any
	}{
		{name: "nil value", value: nil},
		{name: "empty octet string", value: []byte{}},
		{name: "empty string", value: ""},
		{name: "type the exporter cannot render", value: struct{ A int }{A: 1}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results := []*snmp.Result{{OID: lastErrorOID, Value: tt.value, Timestamp: time.Now()}}
			if samples := ConvertStatusColumnsToInfoMetrics("router1", results, nil, cache, time.Now()); len(samples) != 0 {
				t.Errorf("expected no info metric, got %d with value label %q", len(samples), samples[0].Labels["value"])
			}
		})
	}
}

// TestFriendlyNameCacheUsesTheSharedFormatter checks the other consumer of a
// status column's value: the friendly-name labels attached to every sample of
// a row must be rendered by the same rule as the info metric, or the same peer
// would be described two different ways in one scrape.
func TestFriendlyNameCacheUsesTheSharedFormatter(t *testing.T) {
	tests := []struct {
		name  string
		oid   string
		value any
		label string
		want  string
	}{
		{
			name:  "binary error code",
			oid:   lastErrorOID,
			value: []byte{0x04, 0x00},
			label: "cbgppeer2lasterror",
			want:  "04:00",
		},
		{
			name:  "padded text",
			oid:   descriptionOID,
			value: []byte("transit\x00\x00"),
			label: "cbgppeer2description",
			want:  "transit",
		},
		{
			name:  "enumeration",
			oid:   peerStateOID,
			value: 6,
			label: "cbgppeer2state",
			want:  "established",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cache := newBGPCache(t)
			cache.CacheFriendlyNames("router1", []*snmp.Result{
				{OID: tt.oid, Value: tt.value, Timestamp: time.Now()},
			})

			labels := cache.ExtractIndexLabels("router1", tt.oid)
			if got := labels[tt.label]; got != tt.want {
				t.Errorf("%s label = %q, want %q", tt.label, got, tt.want)
			}
		})
	}
}

// TestFriendlyNameCacheOnUnusableValues checks nothing is cached, and so
// nothing is labeled, when a result carries no renderable value.
func TestFriendlyNameCacheOnUnusableValues(t *testing.T) {
	cache := newBGPCache(t)

	cache.CacheFriendlyNames("router1", []*snmp.Result{
		{OID: lastErrorOID, Value: nil, Timestamp: time.Now()},
		{OID: descriptionOID, Value: []byte{}, Timestamp: time.Now()},
		{OID: peerStateOID, Value: struct{ A int }{A: 1}, Timestamp: time.Now()},
	})

	labels := cache.ExtractIndexLabels("router1", lastErrorOID)
	for _, name := range []string{"cbgppeer2lasterror", "cbgppeer2description", "cbgppeer2state"} {
		if value, cached := labels[name]; cached {
			t.Errorf("%s was cached as %q from an unusable value", name, value)
		}
	}
}

// TestStatusColumnLabelValueWithoutMetadata checks the degenerate column: one
// carrying no MIB metadata at all still renders its values instead of panicking
// on the poll path, matching mib.FormatValue's own tolerance of a nil meta.
func TestStatusColumnLabelValueWithoutMetadata(t *testing.T) {
	column := statusColumnInfo{Name: "unknownColumn", ColumnID: "1"}

	tests := []struct {
		name       string
		value      any
		wantRender bool
	}{
		{name: "octets", value: []byte{0x04, 0x00}, wantRender: true},
		{name: "number", value: 6, wantRender: true},
		{name: "empty octets", value: []byte{}, wantRender: false},
		{name: "unrenderable", value: struct{ A int }{A: 1}, wantRender: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, rendered := column.labelValue(&snmp.Result{OID: lastErrorOID, Value: tt.value})
			if rendered != tt.wantRender {
				t.Fatalf("rendered = %v, want %v", rendered, tt.wantRender)
			}
			// What a metadata-less column renders is mib.FormatValue's nil-meta
			// contract, not a second rule owned here.
			if want := mib.FormatValue(nil, tt.value); rendered && got != want {
				t.Errorf("label = %q, want mib.FormatValue(nil) = %q", got, want)
			}
		})
	}
}
