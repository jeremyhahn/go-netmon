package api

import (
	"context"
	"log"
	"strconv"
	"strings"
	"sync"
)

// The dashboard reads metric meaning from the snmp_metric_catalog series in Prometheus, not from MIBs.

// Catalog metric and label names, duplicated from internal/metrics as a wire contract.
const (
	catalogMetric     = "snmp_metric_catalog"
	catalogEnumMetric = "snmp_metric_enum"

	catMetric       = "metric"
	catModule       = "module"
	catObject       = "object"
	catOID          = "oid"
	catUnit         = "unit"
	catKind         = "kind"
	catDescription  = "description"
	catIdentity     = "identity_label"
	catIdentityInfo = "identity_info_metric"
	catIndexLabels  = "index_labels"
	catEnumValue    = "value"
	catEnumLabel    = "label"

	// catValueLabel is the label holding an info metric's text.
	catValueLabel = "value"
)

// CatalogLookback is how far back the retained catalog read reaches; see currentCatalogQuery for superseded rows.
const CatalogLookback = "1h"

// Two reads: a retained last_over_time window and a current instant read, which drops superseded rows; range functions stamp evaluation time.
func currentCatalogQuery(family string) string {
	return family
}

// retainedCatalogQuery reads the wide window, which still holds superseded rows.
func retainedCatalogQuery(family string) string {
	return "last_over_time(" + family + "[" + CatalogLookback + "])"
}

// CatalogEntry is what the catalog says about one published metric.
type CatalogEntry struct {
	Metric      string
	Module      string
	Object      string
	OID         string
	Unit        string
	Kind        string
	Description string

	// Identity is a name label on the series; IdentityInfo is an info metric to join for the name; at most one is set.
	Identity     string
	IdentityInfo string

	IndexLabels []string

	// Enum maps a numeric sample to its label, so a renderer can show "up"
	// rather than 1 without knowing what ifOperStatus is.
	Enum map[string]string
}

// Chartable reports whether a panel from this entry can plot anything; string families cannot.
func (e CatalogEntry) Chartable() bool {
	return e.Kind != string(kindString)
}

type metricKind string

const (
	kindCounter metricKind = "counter"
	kindGauge   metricKind = "gauge"
	kindEnum    metricKind = "enum"
	kindString  metricKind = "string"
	kindUnknown metricKind = "unknown"
)

// ChartType is the widget for this entry, chosen from its declared kind and enums.
func (e CatalogEntry) ChartType() string {
	switch metricKind(e.Kind) {
	case kindString:
		return "text"
	case kindEnum:
		return "state"
	case kindCounter:
		return "rate"
	default:
		return "line"
	}
}

// metricCatalogResult is the catalog keyed by metric name; readable reports whether either catalog read answered.
type metricCatalogResult struct {
	entries  map[string]CatalogEntry
	readable bool
}

func (s *Server) metricCatalog(ctx context.Context) metricCatalogResult {
	// Four independent reads, issued concurrently.
	var (
		wait                    sync.WaitGroup
		retained, current       map[string]CatalogEntry
		retainedOK, currentOK   bool
		retainedEnums, curEnums map[string]map[string]string
	)
	read := func(f func()) {
		wait.Add(1)
		go func() {
			defer wait.Done()
			f()
		}()
	}
	read(func() { retained, retainedOK = s.catalogRows(ctx, retainedCatalogQuery(catalogMetric)) })
	read(func() { current, currentOK = s.catalogRows(ctx, currentCatalogQuery(catalogMetric)) })
	read(func() { retainedEnums = s.enumRows(ctx, retainedCatalogQuery(catalogEnumMetric)) })
	read(func() { curEnums = s.enumRows(ctx, currentCatalogQuery(catalogEnumMetric)) })
	wait.Wait()

	// The retained window first, then current rows laid over it.
	entries := retained
	for name, entry := range current {
		entries[name] = entry
	}

	attachEnums(entries, retainedEnums, curEnums)
	return metricCatalogResult{entries: entries, readable: retainedOK || currentOK}
}

// catalogRows reads one catalog query into entries keyed by metric name.
func (s *Server) catalogRows(ctx context.Context, query string) (map[string]CatalogEntry, bool) {
	rows, err := s.promClient.Query(ctx, query)
	if err != nil {
		log.Printf("WARNING: could not read the metric catalog (%s): %v", query, err)
		return map[string]CatalogEntry{}, false
	}

	entries := make(map[string]CatalogEntry, len(rows))
	for _, row := range rows {
		name := row.Metric[catMetric]
		if name == "" {
			continue
		}
		entry := CatalogEntry{
			Metric:       name,
			Module:       row.Metric[catModule],
			Object:       row.Metric[catObject],
			OID:          row.Metric[catOID],
			Unit:         row.Metric[catUnit],
			Kind:         row.Metric[catKind],
			Description:  row.Metric[catDescription],
			Identity:     row.Metric[catIdentity],
			IdentityInfo: row.Metric[catIdentityInfo],
			IndexLabels:  splitLabels(row.Metric[catIndexLabels]),
		}
		if prev, seen := entries[name]; seen && rowOrder(prev) <= rowOrder(entry) {
			continue
		}
		entries[name] = entry
	}
	return entries, true
}

// rowOrder breaks ties between rows for one metric deterministically, by every label.
func rowOrder(e CatalogEntry) string {
	// Order on every row field, length-prefixed so the encoding is injective.
	var key strings.Builder
	for _, field := range []string{
		e.Module, e.Object, e.OID, e.Unit, e.Kind, e.Description,
		e.Identity, e.IdentityInfo, strings.Join(e.IndexLabels, ","),
	} {
		key.WriteString(strconv.Itoa(len(field)))
		key.WriteByte(':')
		key.WriteString(field)
	}
	return key.String()
}

// attachEnums folds enumeration rows into their metrics, current rows overriding retained ones per value.
func attachEnums(entries map[string]CatalogEntry, retained, current map[string]map[string]string) {
	for name, values := range current {
		if retained[name] == nil {
			retained[name] = values
			continue
		}
		for value, label := range values {
			retained[name][value] = label
		}
	}

	for name, values := range retained {
		// An enumeration row must not invent a metric. It describes how to
		// read one, and a metric with no catalog row has nothing to read.
		entry, ok := entries[name]
		if !ok {
			continue
		}
		entry.Enum = values
		entries[name] = entry
	}
}

// enumRows reads one enumeration query into metric -> value -> label.
func (s *Server) enumRows(ctx context.Context, query string) map[string]map[string]string {
	rows, err := s.promClient.Query(ctx, query)
	if err != nil {
		log.Printf("WARNING: could not read catalog enumerations (%s): %v", query, err)
		return map[string]map[string]string{}
	}

	decodes := make(map[string]map[string]string)
	for _, row := range rows {
		name := row.Metric[catMetric]
		value, label := row.Metric[catEnumValue], row.Metric[catEnumLabel]
		if name == "" || value == "" || label == "" {
			continue
		}
		values := decodes[name]
		if values == nil {
			values = map[string]string{}
			decodes[name] = values
		}
		// Ordered rather than first-seen, for the reason rowOrder gives.
		if prev, seen := values[value]; !seen || label < prev {
			values[value] = label
		}
	}
	return decodes
}

// splitLabels parses the comma-separated index label tuple.
func splitLabels(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
