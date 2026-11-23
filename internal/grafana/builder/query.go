package builder

import (
	"fmt"
	"strings"

	"github.com/jeremyhahn/go-netmon/internal/grafana"
	"github.com/jeremyhahn/go-netmon/internal/mib"
	"github.com/jeremyhahn/go-netmon/internal/promname"
)

// QueryBuilder builds Prometheus queries for SNMP metrics
type QueryBuilder struct {
	metricPrefix string
	deviceLabel  string
	oidLabel     string
}

// NewQueryBuilder creates a new query builder with default settings
func NewQueryBuilder(metricPrefix string) *QueryBuilder {
	if metricPrefix == "" {
		metricPrefix = promname.Prefix
	}
	return &QueryBuilder{
		metricPrefix: metricPrefix,
		deviceLabel:  promname.DeviceLabel,
		oidLabel:     promname.OIDLabel,
	}
}

// NewQueryBuilderWithLabels creates a query builder with custom labels
func NewQueryBuilderWithLabels(metricPrefix, deviceLabel, oidLabel string) *QueryBuilder {
	if metricPrefix == "" {
		metricPrefix = promname.Prefix
	}
	if deviceLabel == "" {
		deviceLabel = promname.DeviceLabel
	}
	if oidLabel == "" {
		oidLabel = promname.OIDLabel
	}
	return &QueryBuilder{
		metricPrefix: metricPrefix,
		deviceLabel:  deviceLabel,
		oidLabel:     oidLabel,
	}
}

// BuildQuery generates a PromQL query for an OID based on its type
func (qb *QueryBuilder) BuildQuery(oid *mib.OIDMetadata) string {
	if oid.IsCounter() {
		return qb.buildCounterQueryStr(oid)
	}
	return qb.buildGaugeQueryStr(oid)
}

// buildGaugeQueryStr generates a query string for gauge-like metrics
func (qb *QueryBuilder) buildGaugeQueryStr(oid *mib.OIDMetadata) string {
	metricName := qb.metricName(oid)
	return fmt.Sprintf("%s{%s=~\"$%s\"}", metricName, qb.deviceLabel, qb.deviceLabel)
}

// buildCounterQueryStr generates a rate() query string for counter metrics
func (qb *QueryBuilder) buildCounterQueryStr(oid *mib.OIDMetadata) string {
	metricName := qb.metricName(oid)
	expr := fmt.Sprintf("rate(%s{%s=~\"$%s\"}[5m])", metricName, qb.deviceLabel, qb.deviceLabel)

	// Convert octets to bits
	if qb.isOctetCounter(oid) {
		expr = fmt.Sprintf("(%s) * 8", expr)
	}

	return expr
}

// BuildScalarQuery builds a query target for scalar OIDs
func (qb *QueryBuilder) BuildScalarQuery(oid *mib.OIDMetadata, useDeviceVariable bool) grafana.Target {
	metricName := qb.metricName(oid)
	deviceFilter := ""
	if useDeviceVariable {
		deviceFilter = fmt.Sprintf(`%s=~"$%s"`, qb.deviceLabel, qb.deviceLabel)
	}

	expr := fmt.Sprintf("%s{%s}", metricName, deviceFilter)
	legendFormat := qb.buildLegendFormat(oid, false)

	return grafana.Target{
		Expr:         expr,
		RefID:        "A",
		LegendFormat: legendFormat,
		Instant:      true,
	}
}

// BuildCounterQuery builds a rate() query target for counter types
func (qb *QueryBuilder) BuildCounterQuery(oid *mib.OIDMetadata, useDeviceVariable bool, rateInterval string) grafana.Target {
	if rateInterval == "" {
		rateInterval = "5m"
	}

	metricName := qb.metricName(oid)
	deviceFilter := ""
	if useDeviceVariable {
		deviceFilter = fmt.Sprintf(`%s=~"$%s"`, qb.deviceLabel, qb.deviceLabel)
	}

	// Apply rate() for counters
	expr := fmt.Sprintf("rate(%s{%s}[%s])", metricName, deviceFilter, rateInterval)

	// If this is octets (bytes), multiply by 8 to get bits
	if qb.isOctetCounter(oid) {
		expr = fmt.Sprintf("(%s) * 8", expr)
	}

	legendFormat := qb.buildLegendFormat(oid, false)

	return grafana.Target{
		Expr:         expr,
		RefID:        "A",
		LegendFormat: legendFormat,
		Range:        true,
		Interval:     rateInterval,
	}
}

// BuildGaugeQuery builds a query target for gauge types
func (qb *QueryBuilder) BuildGaugeQuery(oid *mib.OIDMetadata, useDeviceVariable bool) grafana.Target {
	return qb.BuildScalarQuery(oid, useDeviceVariable)
}

// BuildTableQuery builds a query for the given columns of one table.
//
// Several columns still need a __name__ alternation, because PromQL has no
// other way to name more than one metric in a single selector. It is an
// alternation of whole names rather than a prefix pattern, so Prometheus
// resolves it against the name index instead of scanning every series.
func (qb *QueryBuilder) BuildTableQuery(_ string, columns []*mib.OIDMetadata) string {
	if len(columns) == 0 {
		return ""
	}

	// For single column, return simple query
	if len(columns) == 1 {
		return qb.BuildTableColumnQuery(columns[0])
	}

	metricNames := make([]string, 0, len(columns))
	for _, column := range columns {
		metricNames = append(metricNames, qb.metricName(column))
	}
	pattern := strings.Join(metricNames, "|")
	return fmt.Sprintf("{__name__=~%q,%s=~\"$%s\"}", pattern, qb.deviceLabel, qb.deviceLabel)
}

// BuildTableColumnQuery generates a query for a specific table column. Every
// row of the column shares the column's metric name, so the selector is a plain
// name and the rows are told apart by their index labels.
func (qb *QueryBuilder) BuildTableColumnQuery(column *mib.OIDMetadata) string {
	metricName := qb.metricName(column)
	return fmt.Sprintf("%s{%s=~\"$%s\"}", metricName, qb.deviceLabel, qb.deviceLabel)
}

// BuildColumnQuery builds a query target for table columns
func (qb *QueryBuilder) BuildColumnQuery(oid *mib.OIDMetadata, useDeviceVariable, _ bool) grafana.Target {
	metricName := qb.metricName(oid)
	expr := fmt.Sprintf("%s{%s}", metricName, strings.Join(qb.columnFilters(oid, useDeviceVariable), ","))

	// Apply rate for counter columns
	if oid.IsCounter() {
		expr = fmt.Sprintf("rate(%s[5m])", expr)
		if qb.isOctetCounter(oid) {
			expr = fmt.Sprintf("(%s) * 8", expr)
		}
	}

	legendFormat := qb.buildLegendFormatWithOID()

	return grafana.Target{
		Expr:         expr,
		RefID:        "A",
		LegendFormat: legendFormat,
		Range:        true,
	}
}

// BuildTableCounterQuery builds a rate query target for table counter columns
func (qb *QueryBuilder) BuildTableCounterQuery(oid *mib.OIDMetadata, useDeviceVariable, _ bool, rateInterval string) grafana.Target {
	if rateInterval == "" {
		rateInterval = "5m"
	}

	metricName := qb.metricName(oid)
	expr := fmt.Sprintf("rate(%s{%s}[%s])", metricName, strings.Join(qb.columnFilters(oid, useDeviceVariable), ","), rateInterval)

	if qb.isOctetCounter(oid) {
		expr = fmt.Sprintf("(%s) * 8", expr)
	}

	legendFormat := qb.buildLegendFormatWithOID()

	return grafana.Target{
		Expr:         expr,
		RefID:        "A",
		LegendFormat: legendFormat,
		Range:        true,
		Interval:     rateInterval,
	}
}

// BuildRateQuery generates a rate query with custom interval
func (qb *QueryBuilder) BuildRateQuery(oid *mib.OIDMetadata, interval string) string {
	metricName := qb.metricName(oid)
	expr := fmt.Sprintf("rate(%s{%s=~\"$%s\"}[%s])", metricName, qb.deviceLabel, qb.deviceLabel, interval)

	if qb.isOctetCounter(oid) {
		expr = fmt.Sprintf("(%s) * 8", expr)
	}

	return expr
}

// BuildIncreaseQuery generates an increase query
func (qb *QueryBuilder) BuildIncreaseQuery(oid *mib.OIDMetadata, interval string) string {
	metricName := qb.metricName(oid)
	return fmt.Sprintf("increase(%s{%s=~\"$%s\"}[%s])", metricName, qb.deviceLabel, qb.deviceLabel, interval)
}

// BuildAggregateQuery builds an aggregation query target (sum, avg, max, min)
func (qb *QueryBuilder) BuildAggregateQuery(oid *mib.OIDMetadata, aggregation string, groupBy []string) grafana.Target {
	metricName := qb.metricName(oid)

	// Build base query
	baseExpr := fmt.Sprintf("%s{%s=~\"$%s\"}", metricName, qb.deviceLabel, qb.deviceLabel)

	// Apply rate for counters
	if oid.IsCounter() {
		baseExpr = fmt.Sprintf("rate(%s[5m])", baseExpr)
		if qb.isOctetCounter(oid) {
			baseExpr = fmt.Sprintf("(%s) * 8", baseExpr)
		}
	}

	// Apply aggregation
	if len(groupBy) > 0 {
		groupByStr := strings.Join(groupBy, ",")
		baseExpr = fmt.Sprintf("%s by (%s) (%s)", aggregation, groupByStr, baseExpr)
	} else {
		baseExpr = fmt.Sprintf("%s(%s)", aggregation, baseExpr)
	}

	return grafana.Target{
		Expr:         baseExpr,
		RefID:        "A",
		LegendFormat: fmt.Sprintf("%s - %s", aggregation, oid.Name),
		Range:        true,
	}
}

// BuildPercentileQuery generates a percentile query using histogram quantile
func (qb *QueryBuilder) BuildPercentileQuery(oid *mib.OIDMetadata, percentile float64) string {
	metricName := qb.metricName(oid)
	return fmt.Sprintf("histogram_quantile(%f, rate(%s{%s=~\"$%s\"}[5m]))",
		percentile, metricName, qb.deviceLabel, qb.deviceLabel)
}

// BuildLegendFormat generates a legend format string for the query
func (qb *QueryBuilder) BuildLegendFormat(oid *mib.OIDMetadata) string {
	if oid.IsTable || oid.IsColumn {
		// For table data, include the device and OID name
		return fmt.Sprintf("{{%s}} - %s - {{%s}}", qb.deviceLabel, oid.Name, qb.oidLabel)
	}
	// For scalar values, just show device and name
	return fmt.Sprintf("{{%s}} - %s", qb.deviceLabel, oid.Name)
}

// buildLegendFormat creates a legend format string (internal version)
func (qb *QueryBuilder) buildLegendFormat(oid *mib.OIDMetadata, includeIndex bool) string {
	parts := []string{}

	parts = append(parts, fmt.Sprintf("{{%s}}", qb.deviceLabel))

	if includeIndex {
		parts = append(parts, "{{index}}")
	}

	// Add OID name for clarity
	if oid.Name != "" {
		parts = append(parts, oid.Name)
	}

	return strings.Join(parts, " - ")
}

// buildLegendFormatWithOID creates a legend format using the OID label from metrics
func (qb *QueryBuilder) buildLegendFormatWithOID() string {
	// Use the interface name if available (ifname label), otherwise show the interface index
	// Format: "device - ifname [ifindex]"
	// Grafana uses simple mustache syntax, not Go template conditionals
	return fmt.Sprintf("{{%s}} - {{ifname}} [{{ifindex}}]", qb.deviceLabel)
}

// BuildSimpleLegendFormat generates a simple legend with just device name
func (qb *QueryBuilder) BuildSimpleLegendFormat() string {
	return fmt.Sprintf("{{%s}}", qb.deviceLabel)
}

// BuildCustomLegendFormat generates a legend format with custom template
func (qb *QueryBuilder) BuildCustomLegendFormat(template string) string {
	return template
}

// BuildDeviceSelectorQuery generates a query for the device variable selector
func (qb *QueryBuilder) BuildDeviceSelectorQuery() string {
	return fmt.Sprintf("label_values(%s)", qb.deviceLabel)
}

// BuildLabelValuesQuery generates a query to get unique values for a label
func (qb *QueryBuilder) BuildLabelValuesQuery(labelName string) string {
	return fmt.Sprintf("label_values(%s)", labelName)
}

// BuildLabelValuesForMetricQuery generates a query to get label values for a specific metric
func (qb *QueryBuilder) BuildLabelValuesForMetricQuery(metricName, labelName string) string {
	return fmt.Sprintf("label_values(%s, %s)", metricName, labelName)
}

// columnFilters returns the label matchers that select the rows a panel shows.
//
// A table column's rows all share one metric name, so the row is selected by
// label. The interface variable is applied to every column because it is the
// only row selector these dashboards offer; a table with no ifname label is
// unaffected while $interface is "All", since an empty label value still
// matches ".*".
func (qb *QueryBuilder) columnFilters(oid *mib.OIDMetadata, useDeviceVariable bool) []string {
	filters := make([]string, 0, 2)
	if useDeviceVariable {
		filters = append(filters, fmt.Sprintf(`%s=~"$%s"`, qb.deviceLabel, qb.deviceLabel))
	}
	if oid.IsColumn {
		filters = append(filters, `ifname=~"$interface"`)
	}
	return filters
}

// metricName returns the metric name the exporter gives every instance of an
// object. It is derived from the object's name, never from its OID: the OID of
// a sample identifies one table row, and naming a metric after it would give
// Prometheus one metric name per row.
func (qb *QueryBuilder) metricName(oid *mib.OIDMetadata) string {
	return promname.NameWithPrefix(qb.metricPrefix, oid.Name)
}

// isOctetCounter returns true if this is a byte counter (should convert to bits)
func (qb *QueryBuilder) isOctetCounter(oid *mib.OIDMetadata) bool {
	if !oid.IsCounter() {
		return false
	}
	name := strings.ToLower(oid.Name)
	return strings.Contains(name, "octet") ||
		strings.Contains(name, "byte") ||
		strings.Contains(name, "hcin") ||
		strings.Contains(name, "hcout")
}

// ObjectMetricName returns the Prometheus metric name of a MIB object, for
// callers outside this package that need to name a series without building a
// whole query. An empty prefix means the exporter's own.
//
// It takes the object's name rather than its OID because that is what the
// exporter names a metric after. An OID alone cannot answer the question: it
// takes the loaded MIBs to say which object an OID belongs to.
func ObjectMetricName(objectName, prefix string) string {
	return promname.NameWithPrefix(prefix, objectName)
}

// GetDeviceLabel returns the device label used by this builder
func (qb *QueryBuilder) GetDeviceLabel() string {
	return qb.deviceLabel
}

// GetOIDLabel returns the OID label used by this builder
func (qb *QueryBuilder) GetOIDLabel() string {
	return qb.oidLabel
}

// GetMetricPrefix returns the metric prefix used by this builder
func (qb *QueryBuilder) GetMetricPrefix() string {
	return qb.metricPrefix
}
