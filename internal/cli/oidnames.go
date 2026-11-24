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

// instanceOIDFromLabels returns a series' instance OID from its oid label, else decodes the metric name.
func instanceOIDFromLabels(labels map[string]string) string {
	if oid := normalizeOID(labels[promname.OIDLabel]); oid != "" {
		return oid
	}
	return oidFromMetricName(labels[seriesNameLabel])
}

// oidFromMetricName inverts a raw-OID metric name (snmp__1_3_6 -> 1.3.6), or returns "" when the name is not OID-derived.
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

// oidIndex resolves an OID to its defining MIB object by longest prefix; the rest is the instance.
type oidIndex struct {
	objects map[string]client.MIBObject

	// indexColumns maps a row's OID to its INDEX column names, in INDEX order.
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

// rowIndexColumns returns the INDEX columns of obj's row (its parent OID), or nil.
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

// lookup returns the object whose OID is the longest whole-sub-identifier prefix of oid, and the leftover instance.
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

// instanceLabel names one instance of an object: scalars drop .0, columns render their decoded index or raw sub-identifiers.
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

// decodedIndex renders a row's index from the exporter's decoded INDEX labels, or "" if any column is undecoded.
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

// formatSeriesValue renders a sample with MIB metadata: enum labels, TimeTicks durations and UNITS.
func formatSeriesValue(meta *mib.OIDMetadata, value float64) string {
	if meta == nil || value != math.Trunc(value) || math.Abs(value) >= maxInt64Float {
		return formatValue(value)
	}
	return mib.FormatValueWithUnits(meta, int64(value))
}

// mibFetchConcurrency bounds concurrent MIB module fetches.
const mibFetchConcurrency = 6

// deviceOIDIndex returns a device's OID index, cached for the process; single goroutine only.
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

// fetchDeviceMIBObjects collects the objects of every MIB a device polls, skipping modules that fail to load.
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
