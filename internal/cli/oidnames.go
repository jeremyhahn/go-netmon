package cli

import (
	"context"
	"math"
	"strings"
	"sync"

	"github.com/jeremyhahn/go-netmon/internal/mib"
	"github.com/jeremyhahn/go-netmon/internal/promname"
	"github.com/jeremyhahn/go-netmon/pkg/client"
)

// instanceOIDFromLabels returns the instance OID a series was read from.
//
// The exporter puts it in the "oid" label of every SNMP sample
// (internal/metrics/table_index_cache.go), which is the only part of a series
// that identifies its instance exactly. The metric name does not: it names the
// MIB object, so every row of a table column carries the same one.
//
// A series without the label falls back to decoding the name, which still works
// for the one case that keeps a raw-OID name — an instance OID no loaded MIB
// describes.
func instanceOIDFromLabels(labels map[string]string) string {
	if oid := normalizeOID(labels[promname.OIDLabel]); oid != "" {
		return oid
	}
	return oidFromMetricName(labels[seriesNameLabel])
}

// oidFromMetricName recovers the OID a metric name was derived from, or ""
// when the name did not come from one.
//
// This is the fallback path, not the primary one: the exporter names a metric
// after the MIB object it instantiates, so only an instance OID that no loaded
// MIB describes still reaches Prometheus under a raw-OID name. Those names are
// the sanitized form of "snmp_" + the OID (internal/metrics/name_index.go),
// which replaces every character outside [a-zA-Z0-9_:] with "_". An OID is
// nothing but digits and dots, so the sanitized form is digits and underscores,
// and the mapping inverts exactly:
//
//	snmp__1_3_6_1_2_1_2_2_1_10_10 -> 1.3.6.1.2.1.2.2.1.10.10
//
// The doubled underscore is the leading dot the poller carries on its OIDs;
// OIDs written without one produce a single underscore, so at most one leading
// underscore is dropped. Every remaining underscore was a dot, because a
// component of an OID cannot be anything but digits — which is also what makes
// names the exporter builds from text, such as "snmp_system_info", recognizable
// as not being OIDs.
func oidFromMetricName(metric string) string {
	rest, ok := strings.CutPrefix(metric, snmpMetricPrefix)
	if !ok {
		return ""
	}
	rest = strings.TrimPrefix(rest, "_")
	if rest == "" {
		return ""
	}

	components := strings.Split(rest, "_")
	for _, c := range components {
		if !isDigits(c) {
			return ""
		}
	}
	return strings.Join(components, ".")
}

// isDigits reports whether s is a non-empty run of decimal digits.
func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// oidIndex resolves an OID to the MIB object that defines it.
//
// A sample's OID is an object's OID followed by an instance — ".0" for a
// scalar, the row's index for a column — so resolution is a longest-prefix
// match and the leftover sub-identifiers are the instance.
type oidIndex struct {
	objects map[string]client.MIBObject

	// indexColumns maps a conceptual row's OID to the names of its INDEX
	// columns, in INDEX order. It is what turns a sample's labels into a
	// readable row identity: see decodedIndex.
	indexColumns map[string][]string
}

// newOIDIndex builds an index over a device's MIB objects. Objects are keyed
// by OID with the leading dot normalized away, so an index built from one
// source can answer lookups from another.
func newOIDIndex(objects []client.MIBObject) *oidIndex {
	ix := &oidIndex{
		objects:      make(map[string]client.MIBObject, len(objects)),
		indexColumns: make(map[string][]string),
	}
	for _, obj := range objects {
		key := normalizeOID(obj.OID)
		if key == "" {
			continue
		}
		// Two modules can define the same OID. Keeping the first definition
		// makes the label depend on the module order rather than on map
		// iteration, so successive frames render the same name.
		if _, taken := ix.objects[key]; !taken {
			ix.objects[key] = obj
		}
		// A conceptual row carries its INDEX clause; nothing else does.
		if len(obj.Index) > 0 {
			if _, taken := ix.indexColumns[key]; !taken {
				ix.indexColumns[key] = obj.Index
			}
		}
	}
	return ix
}

// rowIndexColumns returns the INDEX columns of the conceptual row obj is a
// column of, in INDEX order, or nil when obj is not a column or no loaded MIB
// describes its row.
//
// A column's row is its parent: ifInOctets is .1.3.6.1.2.1.2.2.1.10, ifEntry is
// .1.3.6.1.2.1.2.2.1. That is how the MIB parser derives the relationship in
// the first place, so deriving it back costs nothing on the wire.
func (ix *oidIndex) rowIndexColumns(obj client.MIBObject) []string {
	if !obj.IsColumn {
		return nil
	}
	return ix.indexColumns[parentOID(normalizeOID(obj.OID))]
}

// parentOID drops an OID's last sub-identifier, or returns "" when there is
// nothing left to drop.
func parentOID(oid string) string {
	cut := strings.LastIndex(oid, ".")
	if cut <= 0 {
		return ""
	}
	return oid[:cut]
}

// lookup returns the object whose OID is the longest prefix of oid, together
// with the sub-identifiers left over as the instance.
//
// Matching walks whole sub-identifiers rather than characters: column
// 1.3.6.1.2.1.2.2.1.2 must not claim a sample of 1.3.6.1.2.1.2.2.1.20, which a
// string prefix test would hand it.
func (ix *oidIndex) lookup(oid string) (client.MIBObject, string, bool) {
	normalized := normalizeOID(oid)
	if normalized == "" {
		return client.MIBObject{}, "", false
	}

	components := strings.Split(normalized, ".")
	for i := len(components); i > 0; i-- {
		if obj, ok := ix.objects[strings.Join(components[:i], ".")]; ok {
			return obj, strings.Join(components[i:], "."), true
		}
	}
	return client.MIBObject{}, "", false
}

// Describe names a series the way a MIB browser would and returns the metadata
// needed to format its value, e.g. "ifInOctets[10]" for a sample of
// .1.3.6.1.2.1.2.2.1.10.10.
//
// It takes the whole label set because the instance is a label, not part of the
// name: the exporter names a metric after the MIB object and puts the row index
// in labels, so snmp_ifInOctets alone says nothing about which interface a
// sample belongs to. The exact instance comes from the "oid" label, which names
// the object; the row's identity comes from the index labels beside it, which
// the exporter has already decoded -- "203.0.113.218" rather than the 4.203.0.113.218
// the OID carries. Which labels those are is read from the MIB's INDEX clause
// and not guessed, so the enrichment labels the exporter adds alongside them --
// ifname, ifdescr, ifalias and the rest -- are never mistaken for part of the
// row.
//
// The reported ok is false when the series is not OID-derived or when no loaded
// MIB defines it; the label is then the series' full PromQL identity, so a
// caller can render every row without a second code path.
func (ix *oidIndex) Describe(labels map[string]string) (string, *mib.OIDMetadata, bool) {
	if ix == nil {
		return formatSeries(labels), nil, false
	}
	oid := instanceOIDFromLabels(labels)
	if oid == "" {
		return formatSeries(labels), nil, false
	}
	obj, instance, ok := ix.lookup(oid)
	if !ok {
		return formatSeries(labels), nil, false
	}
	return instanceLabel(obj, instance, labels, ix.rowIndexColumns(obj)), objectMetadata(obj), true
}

// instanceLabel names one instance of a MIB object. A scalar has exactly one
// instance, ".0", which carries no information and is left off; a column's
// index is what tells two interfaces apart, so it is kept.
//
// The index is rendered from the decoded values the exporter put in labels when
// the MIB says which labels those are, and from the raw OID sub-identifiers
// when it does not. The difference is the difference between
// cbgpPeer2AdminStatus[cbgpPeer2Type=1,cbgpPeer2RemoteAddr=203.0.113.218] and
// cbgpPeer2AdminStatus[1.4.203.0.113.218], which carry the same information and
// are not equally readable.
func instanceLabel(
	obj client.MIBObject,
	instance string,
	labels map[string]string,
	indexColumns []string,
) string {
	if instance == "" || (instance == "0" && !obj.IsColumn) {
		return obj.Name
	}
	if decoded := decodedIndex(indexColumns, labels); decoded != "" {
		return obj.Name + "[" + decoded + "]"
	}
	return obj.Name + "[" + instance + "]"
}

// decodedIndex renders a row's index from the labels the exporter decoded it
// into, or "" when it cannot be rendered whole.
//
// The columns are the row's INDEX clause, so every label read here is part of
// the row's identity by definition; a blocklist of the exporter's enrichment
// labels would instead have to be kept in step with the exporter forever, and
// would still be wrong for the next label it learns to attach.
//
// A single-column index renders as the bare value: the object already names its
// table, so "ifInOctets[10]" says everything "ifInOctets[ifIndex=10]" does. A
// composite index names each column, because position alone does not say which
// of four values is the peer address.
//
// A column the exporter did not decode -- an index type it could not split, a
// table it learned rather than read from a MIB -- leaves the row only partly
// identified, and half an index is worse than the raw sub-identifiers, which at
// least identify the row exactly. That is when "" is returned.
func decodedIndex(columns []string, labels map[string]string) string {
	if len(columns) == 0 {
		return ""
	}

	parts := make([]string, 0, len(columns))
	for _, column := range columns {
		value := labels[promname.IndexLabel(column)]
		if value == "" {
			return ""
		}
		if len(columns) == 1 {
			parts = append(parts, value)
			continue
		}
		parts = append(parts, column+"="+value)
	}
	return strings.Join(parts, ",")
}

// objectMetadata converts an API MIB object to the metadata mib.FormatValue
// reads, which is how the CLI renders values exactly as the web UI does.
func objectMetadata(obj client.MIBObject) *mib.OIDMetadata {
	return &mib.OIDMetadata{
		OID:         obj.OID,
		Name:        obj.Name,
		Type:        obj.Type,
		Access:      obj.Access,
		Status:      obj.Status,
		Description: obj.Description,
		Units:       obj.Unit,
		DisplayHint: obj.DisplayHint,
		EnumValues:  obj.EnumValues,
		IsTable:     obj.IsTable,
		IsColumn:    obj.IsColumn,
		IsScalar:    obj.IsScalar,
		IsIndex:     obj.IsIndex,
		TableOID:    obj.TableOID,
		Pollable:    obj.Pollable,
	}
}

// normalizeOID drops the leading dot SNMP OIDs are often written with so that
// index keys and lookups agree whichever form their source used.
func normalizeOID(oid string) string {
	return strings.TrimPrefix(strings.TrimSpace(oid), ".")
}

// maxInt64Float bounds the values that can be converted to an int64. Anything
// larger is beyond both int64 and a Counter64, so it cannot be an SNMP value.
const maxInt64Float = 9.2e18

// formatSeriesValue renders a sample value, using MIB metadata when there is
// any: enumerations become labels, TimeTicks become durations, and a UNITS
// clause is appended.
//
// Prometheus carries every sample as a float64. A value that is not a whole
// number in int64 range cannot be an SNMP integer, so it keeps the numeric
// rendering the metrics commands use rather than being mangled by a formatter
// that expects one.
func formatSeriesValue(meta *mib.OIDMetadata, value float64) string {
	if meta == nil || value != math.Trunc(value) || math.Abs(value) >= maxInt64Float {
		return formatValue(value)
	}
	return mib.FormatValueWithUnits(meta, int64(value))
}

// mibFetchConcurrency bounds how many MIB modules are fetched at once. A
// device commonly polls a dozen or more and each is its own round trip, so
// fetching them one at a time is what makes the view feel slow; fetching all
// of them at once would instead stampede the API.
const mibFetchConcurrency = 6

// deviceOIDIndex returns the OID index for a device, building it on first use.
//
// MIB definitions do not change while netmon runs, so a --refresh loop pays
// for the index once instead of on every frame. Commands render on a single
// goroutine, so the cache needs no synchronization.
func (e *Env) deviceOIDIndex(ctx context.Context, api *client.Client, deviceID int64) *oidIndex {
	if ix, ok := e.oidIndexes[deviceID]; ok {
		return ix
	}

	ix := newOIDIndex(fetchDeviceMIBObjects(ctx, api, deviceID))
	if e.oidIndexes == nil {
		e.oidIndexes = make(map[int64]*oidIndex)
	}
	e.oidIndexes[deviceID] = ix
	return ix
}

// fetchDeviceMIBObjects collects the objects of every MIB a device polls,
// directly or through a profile.
//
// A module that cannot be loaded is skipped rather than reported: a partial
// index still names most of a view, and no naming problem is worth failing a
// command whose numbers are all correct.
func fetchDeviceMIBObjects(
	ctx context.Context,
	api *client.Client,
	deviceID int64,
) []client.MIBObject {
	mibs, err := api.DeviceMIBs(ctx, deviceID)
	if err != nil || mibs == nil {
		return nil
	}

	// Each fetch owns one slot, so the results need no locking and stay in
	// module order, which keeps the index deterministic between runs.
	perModule := make([][]client.MIBObject, len(mibs.AllMIBs))
	slots := make(chan struct{}, mibFetchConcurrency)
	var wg sync.WaitGroup

	for i, m := range mibs.AllMIBs {
		wg.Add(1)
		go func() {
			defer wg.Done()

			slots <- struct{}{}
			defer func() { <-slots }()

			detail, detailErr := api.GetMIB(ctx, m.ID)
			if detailErr != nil {
				return
			}
			perModule[i] = detail.Objects
		}()
	}
	wg.Wait()

	total := 0
	for _, objects := range perModule {
		total += len(objects)
	}
	objects := make([]client.MIBObject, 0, total)
	for _, module := range perModule {
		objects = append(objects, module...)
	}
	return objects
}
