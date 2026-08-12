package dashboard

import (
	"fmt"
	"strings"

	"github.com/jeremyhahn/go-netmon/internal/mib"
	"github.com/jeremyhahn/go-netmon/internal/mib/storage"
	"github.com/jeremyhahn/go-netmon/internal/promname"
)

// LearnedTable represents a dynamically discovered table from the OID learner
type LearnedTable struct {
	EntryOID   string   `json:"entry_oid"`
	IndexNames []string `json:"index_names"`
}

// LearnedTablesMap maps entry OIDs to learned table information
type LearnedTablesMap map[string]*LearnedTable

// Spec defines the complete dashboard configuration for a MIB
type Spec struct {
	MIBName       string         `json:"mib_name"`
	ScalarWidgets []ScalarWidget `json:"scalar_widgets,omitempty"`
	TableWidgets  []TableWidget  `json:"table_widgets,omitempty"`
}

// ScalarWidget represents a single scalar value display
type ScalarWidget struct {
	Type string `json:"type"` // "status" or "chart"
	OID  string `json:"oid"`
	Name string `json:"name"`

	// Metric is the Prometheus metric name to query. It is resolved here
	// rather than rebuilt by the browser from OID, because only the server
	// knows the MIB object name the exporter names a series after.
	Metric string `json:"metric"`

	Description string           `json:"description"`
	Unit        string           `json:"unit,omitempty"`
	EnumValues  map[int64]string `json:"enum_values,omitempty"`
	ChartType   string           `json:"chart_type,omitempty"` // "line", "gauge"
}

// TableWidget represents a table with instance selection and field display
type TableWidget struct {
	TableName        string           `json:"table_name"`
	InstanceSelector InstanceSelector `json:"instance_selector"`
	StatusFields     []StatusField    `json:"status_fields,omitempty"`
	MetricCharts     []MetricChart    `json:"metric_charts,omitempty"`
}

// InstanceSelector defines how to select table instances.
//
// A table's rows all share one metric name per column, so a row is picked out
// by matching on LabelName -- the label the exporter puts the chosen index
// column's decoded value in. IndexLabels lists every index label of the table,
// in INDEX order, for callers that need to identify a row exactly rather than
// by its most meaningful column alone.
type InstanceSelector struct {
	IndexColumn       string   `json:"index_column"`
	QueryTemplate     string   `json:"query_template"`
	LabelName         string   `json:"label_name"`                    // "ifindex", "cbgppeer2remoteaddr", etc.
	IndexLabels       []string `json:"index_labels,omitempty"`        // every index label of the table, in INDEX order
	FriendlyNameLabel string   `json:"friendly_name_label,omitempty"` // "ifname", "ifdescr", etc. (optional friendly name)
}

// StatusField represents a status/enum field in a table
type StatusField struct {
	OID         string `json:"oid"`
	Name        string `json:"name"`
	Description string `json:"description"`

	// Metric is the Prometheus metric name to query: the info metric when
	// IsInfoMetric, the column's own metric otherwise.
	Metric string `json:"metric"`

	EnumValues   map[int64]string `json:"enum_values,omitempty"`
	IsInfoMetric bool             `json:"is_info_metric,omitempty"` // If true, query as info metric (snmp_{name}_info)
}

// MetricChart represents a time-series metric field
type MetricChart struct {
	OID         string `json:"oid"`
	Name        string `json:"name"`
	Description string `json:"description"`

	// Metric is the Prometheus metric name to query. Every row of the column
	// shares it; rows are selected with the index labels below.
	Metric string `json:"metric"`

	Unit      string `json:"unit,omitempty"`
	ChartType string `json:"chart_type"`           // "line", "gauge", "bar"
	UseRate   bool   `json:"use_rate,omitempty"`   // If true, wrap query in rate() for counter metrics
	IsCounter bool   `json:"is_counter,omitempty"` // True if this is a counter type metric
}

// tableGroup holds OIDs grouped by table
type tableGroup struct {
	tableOID   string
	tableName  string
	entryOID   string
	indexCols  []*mib.OIDMetadata
	statusCols []*mib.OIDMetadata
	metricCols []*mib.OIDMetadata
}

// GenerateDashboard creates a dashboard specification for a MIB module
func GenerateDashboard(store *storage.Storage, moduleName string) (*Spec, error) {
	return GenerateDashboardWithLearnedTables(store, moduleName, nil)
}

// GenerateDashboardWithLearnedTables creates a dashboard specification with optional learned table data
func GenerateDashboardWithLearnedTables(store *storage.Storage, moduleName string, learnedTables LearnedTablesMap) (*Spec, error) {
	// Verify module exists
	_, err := store.GetModule(moduleName)
	if err != nil {
		return nil, fmt.Errorf("module not found: %w", err)
	}

	spec := &Spec{
		MIBName:       moduleName,
		ScalarWidgets: make([]ScalarWidget, 0),
		TableWidgets:  make([]TableWidget, 0),
	}

	// Get all OIDs for this module
	moduleOIDs, err := store.ListModuleOIDs(moduleName)
	if err != nil {
		return nil, fmt.Errorf("failed to list OIDs: %w", err)
	}

	// Build learned tables map from Entry objects if not provided
	if learnedTables == nil {
		learnedTables = buildLearnedTablesFromEntries(moduleOIDs)
	}

	// Group OIDs by table
	tables := make(map[string]*tableGroup)
	var scalars []*mib.OIDMetadata

	for _, oid := range moduleOIDs {
		switch oid.ObjectClass {
		case mib.ObjectClassScalar:
			// Only add if it's pollable and displayable/chartable
			if oid.Pollable && (oid.IsDisplayable || oid.IsChartable) {
				scalars = append(scalars, oid)
			}

		case mib.ObjectClassIndexColumn, mib.ObjectClassStatusColumn, mib.ObjectClassMetricColumn:
			// Group by table
			if oid.TableOID == "" {
				continue
			}

			group, exists := tables[oid.TableOID]
			if !exists {
				// Find table name
				tableName := extractTableName(oid.TableOID, moduleOIDs)
				group = &tableGroup{
					tableOID:   oid.TableOID,
					tableName:  tableName,
					entryOID:   oid.EntryOID,
					indexCols:  make([]*mib.OIDMetadata, 0),
					statusCols: make([]*mib.OIDMetadata, 0),
					metricCols: make([]*mib.OIDMetadata, 0),
				}
				tables[oid.TableOID] = group
			}

			// Categorize column
			switch oid.ObjectClass {
			case mib.ObjectClassIndexColumn:
				group.indexCols = append(group.indexCols, oid)
			case mib.ObjectClassStatusColumn:
				group.statusCols = append(group.statusCols, oid)
			case mib.ObjectClassMetricColumn:
				group.metricCols = append(group.metricCols, oid)
			}
		}
	}

	// Generate scalar widgets
	for _, scalar := range scalars {
		widget := buildScalarWidget(scalar)
		spec.ScalarWidgets = append(spec.ScalarWidgets, widget)
	}

	// Generate table widgets - only for tables that exist in learned data
	for _, group := range tables {
		// Skip tables that don't have learned data (not discovered on device)
		if learnedTables != nil && group.entryOID != "" {
			// Check if this table's entry OID exists in learned tables
			if _, exists := learnedTables[group.entryOID]; !exists {
				// Table not discovered - skip widget generation
				continue
			}
		}

		widget := buildTableWidgetWithLearnedData(group, learnedTables)
		spec.TableWidgets = append(spec.TableWidgets, widget)
	}

	return spec, nil
}

// buildLearnedTablesFromEntries creates a LearnedTablesMap from Entry objects' Index fields
func buildLearnedTablesFromEntries(oids []*mib.OIDMetadata) LearnedTablesMap {
	learned := make(LearnedTablesMap)

	for _, oid := range oids {
		// Look for table entry objects with Index field set
		if oid.IsTableEntry && len(oid.Index) > 0 {
			learned[oid.OID] = &LearnedTable{
				EntryOID:   oid.OID,
				IndexNames: oid.Index,
			}
		}
	}

	return learned
}

// shouldChartScalar determines if a scalar value should be charted
// This function provides a secondary check on top of the parser's IsChartable determination
// It's primarily used for additional refinement in the dashboard generation context
func shouldChartScalar(oid *mib.OIDMetadata) bool {
	// Trust the parser's chartability determination first
	// The parser has comprehensive logic for determining chartability
	if !oid.IsChartable {
		return false
	}

	// Don't chart enums or strings (parser should have already filtered these)
	if len(oid.EnumValues) > 0 || oid.IsString() {
		return false
	}

	// Always chart counters (they're inherently time-series)
	if oid.IsCounter() {
		return true
	}

	// For gauges, apply additional dashboard-specific filtering
	// This provides a second layer of validation on top of parser logic
	if oid.IsGauge() {
		nameLower := strings.ToLower(oid.Name)
		descLower := strings.ToLower(oid.Description)

		// Additional static indicators to check in dashboard context
		// These extend the parser's checks with dashboard-specific concerns
		dashboardStaticKeywords := []string{
			"bitmask",    // Bitmask indicators
			"capability", // Capability flags
			"flag",       // Feature flags
			"bit",        // Bit fields (when not part of "bits per second")
			"service",    // Service designation
			"feature",    // Feature flags
		}

		for _, keyword := range dashboardStaticKeywords {
			// Special handling for "bit" - allow "bits per second" contexts
			if keyword == "bit" {
				if strings.Contains(descLower, keyword) &&
					!strings.Contains(descLower, "bits per second") &&
					!strings.Contains(descLower, "bit/s") {
					return false
				}
				continue
			}

			if strings.Contains(nameLower, keyword) || strings.Contains(descLower, keyword) {
				return false
			}
		}

		// If parser marked it chartable and we didn't find dashboard-specific reasons to reject it,
		// trust the parser's decision
		return true
	}

	// For other types, trust the parser's chartability determination
	return true
}

// buildScalarWidget creates a scalar widget from OID metadata
func buildScalarWidget(oid *mib.OIDMetadata) ScalarWidget {
	widget := ScalarWidget{
		OID:         oid.OID,
		Name:        oid.Name,
		Metric:      promname.MetricName(oid.Name),
		Description: oid.Description,
		Unit:        oid.Units,
		EnumValues:  oid.EnumValues,
		ChartType:   oid.ChartType,
	}

	// Determine widget type
	switch {
	case len(oid.EnumValues) > 0 || oid.IsString():
		widget.Type = "status"
	case shouldChartScalar(oid):
		widget.Type = "chart"
		if widget.ChartType == "" {
			widget.ChartType = "line"
		}
	default:
		widget.Type = "status"
	}

	// A reading that is words rather than a number reaches Prometheus as an
	// info metric: the text is a label on a constant-1 series, under a
	// different name from the one a numeric object would carry.
	//
	// Naming it the numeric way asked for a series nobody writes. The ATEN
	// KVM reports its power supplies, fan and temperature exactly this way, so
	// "PSU2 is abnormal, please check the device" was collected every 30
	// seconds and had no widget that could find it.
	if oid.IsString() {
		widget.Metric = promname.InfoMetricName(oid.Name)
	}

	return widget
}

// buildTableWidgetWithLearnedData creates a table widget with optional learned table data
func buildTableWidgetWithLearnedData(group *tableGroup, learnedTables LearnedTablesMap) TableWidget {
	widget := TableWidget{
		TableName:    group.tableName,
		StatusFields: make([]StatusField, 0),
		MetricCharts: make([]MetricChart, 0),
	}

	// Build instance selector from index columns
	if len(group.indexCols) > 0 {
		// For composite indexes, choose the most meaningful index for selection
		widget.InstanceSelector = buildInstanceSelectorFromMultipleWithFriendlyName(group.indexCols, group)
	} else {
		// No index columns from MIB - check learned tables
		if learnedTables != nil && group.entryOID != "" {
			if learned, exists := learnedTables[group.entryOID]; exists && len(learned.IndexNames) > 0 {
				// Build pseudo index metadata from learned names
				indexCols := make([]*mib.OIDMetadata, 0, len(learned.IndexNames))
				for _, indexName := range learned.IndexNames {
					indexCols = append(indexCols, &mib.OIDMetadata{
						Name: indexName,
					})
				}
				// Use the standard selection logic on the learned indexes
				widget.InstanceSelector = buildInstanceSelectorFromMultipleWithFriendlyName(indexCols, group)
			} else {
				// Fallback to hardcoded heuristics
				widget.InstanceSelector = buildFallbackInstanceSelector(group.tableName)
			}
		} else {
			// Fallback to hardcoded heuristics
			widget.InstanceSelector = buildFallbackInstanceSelector(group.tableName)
		}
	}

	// Build status fields
	for _, col := range group.statusCols {
		// Determine if this should query an info metric
		// String-type status columns (DisplayString, OCTET STRING, etc.) AND enum status columns use info metrics
		isInfoMetric := false
		if col.Type == "DisplayString" || col.Type == "OCTET STRING" || col.Type == "OctetString" ||
			col.Type == "SnmpAdminString" || strings.Contains(strings.ToLower(col.Type), "string") {
			isInfoMetric = true
		}
		// Also mark enum fields as info metrics (they'll be exported with enum string values)
		if len(col.EnumValues) > 0 {
			isInfoMetric = true
		}

		metric := promname.MetricName(col.Name)
		if isInfoMetric {
			metric = promname.InfoMetricName(col.Name)
		}

		field := StatusField{
			OID:          col.OID,
			Name:         col.Name,
			Description:  col.Description,
			Metric:       metric,
			EnumValues:   col.EnumValues,
			IsInfoMetric: isInfoMetric,
		}
		widget.StatusFields = append(widget.StatusFields, field)
	}

	// Build metric charts
	for _, col := range group.metricCols {
		isCounter := col.IsCounter()
		chart := MetricChart{
			OID:         col.OID,
			Name:        col.Name,
			Description: col.Description,
			Metric:      promname.MetricName(col.Name),
			Unit:        col.Units,
			ChartType:   col.ChartType,
			UseRate:     isCounter, // Counter metrics should use rate() to show bandwidth
			IsCounter:   isCounter,
		}

		// Ensure chart type is set
		if chart.ChartType == "" {
			switch {
			case isCounter:
				chart.ChartType = "line"
			case col.IsGauge():
				chart.ChartType = "gauge"
			default:
				chart.ChartType = "line"
			}
		}

		widget.MetricCharts = append(widget.MetricCharts, chart)
	}

	return widget
}

// buildFallbackInstanceSelector creates a fallback instance selector when no index info is available
func buildFallbackInstanceSelector(tableName string) InstanceSelector {
	// Fallback: Tables that augment other tables (like ifXTable) inherit their index
	// Try to use ifindex as the default for interface tables
	labelName := "ifindex"
	if strings.Contains(strings.ToLower(tableName), "if") &&
		(strings.Contains(strings.ToLower(tableName), "table") || strings.Contains(strings.ToLower(tableName), "x")) {
		// This is likely an interface-related table, use ifindex
		return InstanceSelector{
			IndexColumn:   "ifIndex",
			LabelName:     labelName,
			IndexLabels:   []string{labelName},
			QueryTemplate: buildInstanceQueryTemplate(labelName),
		}
	}
	// Generic fallback - no query template
	return InstanceSelector{
		IndexColumn:   "instance",
		LabelName:     "instance",
		QueryTemplate: "",
	}
}

// buildInstanceSelectorFromMultiple chooses the best index column from a list for instance selection
func buildInstanceSelectorFromMultiple(indexCols []*mib.OIDMetadata) InstanceSelector {
	// Priority order for choosing the best index column:
	// 1a. Actual IP addresses (remoteaddr, ipaddr, ipaddress, peeraddr, etc.)
	// 1b. Generic address-like fields (but not addrfamily, addrtype, etc.)
	// 2. Names (ifname, etc.)
	// 3. Interface indexes (ifindex)
	// 4. Generic indexes
	// 5. First index if nothing else matches

	var bestIndex *mib.OIDMetadata
	bestPriority := 999

	for _, indexCol := range indexCols {
		lower := strings.ToLower(indexCol.Name)
		priority := 999

		// Priority 1a: Actual IP address columns (highest priority)
		// Look for specific patterns that indicate an actual IP address
		switch {
		case strings.Contains(lower, "remoteaddr"),
			strings.Contains(lower, "ipaddr"),
			strings.Contains(lower, "ipaddress"),
			strings.Contains(lower, "peeraddr"),
			strings.Contains(lower, "addr") && !strings.Contains(lower, "addrfamily") &&
				!strings.Contains(lower, "addrtype") && !strings.Contains(lower, "addrsafi") &&
				!strings.Contains(lower, "addrafi"):
			// Further refine: prefer columns ending with "addr" over those containing it
			switch {
			case strings.HasSuffix(lower, "addr") || strings.HasSuffix(lower, "address"):
				priority = 1
			case strings.Contains(lower, "remoteaddr") || strings.Contains(lower, "ipaddr"):
				priority = 1
			default:
				// Generic addr field, slightly lower priority
				priority = 2
			}
		case strings.Contains(lower, "name"):
			// Priority 3: Names
			priority = 3
		case strings.HasPrefix(lower, "if") && strings.Contains(lower, "index"):
			// Priority 4: Interface indexes
			priority = 4
		case strings.Contains(lower, "index"):
			// Priority 5: Generic indexes
			priority = 5
		}

		if priority < bestPriority {
			bestPriority = priority
			bestIndex = indexCol
		}
	}

	// Fallback to first index if no priority matched
	if bestIndex == nil {
		bestIndex = indexCols[0]
	}

	selector := buildInstanceSelector(bestIndex)
	selector.IndexLabels = indexLabels(indexCols)
	return selector
}

// indexLabels lists the labels the exporter puts a table's index columns in,
// in INDEX order. A row is identified exactly by all of them; LabelName alone
// identifies it only as far as that column is unique within the table.
func indexLabels(indexCols []*mib.OIDMetadata) []string {
	labels := make([]string, 0, len(indexCols))
	for _, col := range indexCols {
		if col.Name == "" {
			continue
		}
		labels = append(labels, promname.IndexLabel(col.Name))
	}
	return labels
}

// buildInstanceSelector creates an instance selector from index column
func buildInstanceSelector(indexCol *mib.OIDMetadata) InstanceSelector {
	// Determine the best label to use for instance selection
	// For composite indexes, we want the most meaningful label (e.g., IP address for BGP)
	labelName := determineBestLabelForInstance(indexCol.Name)

	selector := InstanceSelector{
		IndexColumn:   indexCol.Name,
		LabelName:     labelName,
		QueryTemplate: buildInstanceQueryTemplate(labelName),
	}

	return selector
}

// determineBestLabelForInstance determines the best prometheus label for
// instance selection. For composite indexes, this picks the most meaningful one
// (e.g., IP address over type/afi/safi).
//
// The label is always the exporter's rendering of the index column's own name,
// so it matches the series whatever the column is called; the special cases
// below only decide which of a composite index's columns is offered, never what
// the label is named.
func determineBestLabelForInstance(indexName string) string {
	lower := strings.ToLower(indexName)

	// Priority 2: Interface index. ifXTable and other tables that augment
	// ifTable are indexed by ifIndex under their own column names, and the
	// exporter labels every one of them "ifindex".
	if strings.HasPrefix(lower, "if") && strings.Contains(lower, "index") {
		return "ifindex"
	}

	// Priority 4: Names
	if strings.Contains(lower, "name") && strings.HasPrefix(lower, "if") {
		return "ifname"
	}

	return promname.IndexLabel(indexName)
}

// buildInstanceQueryTemplate builds a Prometheus query for listing unique label values
// This queries Prometheus for all unique values of a specific label (e.g., ifindex, cbgppeer2remoteaddr)
func buildInstanceQueryTemplate(labelName string) string {
	// Query for all unique values of this label for the selected device
	// Example: label_values({device="router1"}, ifindex)
	// Example: label_values({device="router1"}, cbgppeer2remoteaddr)
	return fmt.Sprintf("label_values({device=\"{{device}}\"}, %s)", labelName)
}

// extractTableName finds the table name from a table OID
func extractTableName(tableOID string, allOIDs []*mib.OIDMetadata) string {
	for _, oid := range allOIDs {
		if oid.OID == tableOID && oid.IsTable {
			return oid.Name
		}
	}
	// Fallback to OID
	return tableOID
}

// buildInstanceSelectorFromMultipleWithFriendlyName creates an instance selector with friendly name support
func buildInstanceSelectorFromMultipleWithFriendlyName(indexCols []*mib.OIDMetadata, group *tableGroup) InstanceSelector {
	// First get the base selector
	selector := buildInstanceSelectorFromMultiple(indexCols)

	// Try to find a friendly name label
	selector.FriendlyNameLabel = findFriendlyNameLabel(selector.LabelName, group)

	return selector
}

// findFriendlyNameLabel finds a friendly name label for the given primary label
// For example, for "ifindex" it would return "ifname" or "ifdescr" if available
func findFriendlyNameLabel(primaryLabel string, group *tableGroup) string {
	lower := strings.ToLower(primaryLabel)

	// For interface indexes, look for ifName or ifDescr
	if lower == "ifindex" {
		// Check status columns first (ifName, ifDescr are usually status columns)
		for _, col := range group.statusCols {
			colLower := strings.ToLower(col.Name)
			if colLower == "ifname" {
				return "ifname"
			}
		}
		for _, col := range group.statusCols {
			colLower := strings.ToLower(col.Name)
			if colLower == "ifdescr" || colLower == "ifalias" {
				return colLower
			}
		}
	}

	// For BGP peer tables, look for State column to show peer status
	// E.g., "cbgppeer2remoteaddr" -> look for "cbgpPeer2State"
	if strings.Contains(lower, "peer") && strings.Contains(lower, "addr") {
		// Extract the table prefix (e.g., "cbgppeer2" from "cbgppeer2remoteaddr")
		parts := strings.Split(lower, "remote")
		if len(parts) > 0 {
			prefix := parts[0] // e.g., "cbgppeer2"
			// Look for {prefix}state
			for _, col := range group.statusCols {
				colLower := strings.ToLower(col.Name)
				if colLower == prefix+"state" {
					return colLower
				}
			}
		}
	}

	// For other indexes, look for a "Name" or "Descr" column with similar prefix
	// E.g., for "cbgppeer2remoteaddr", no friendly name needed (IP is already friendly)
	// But for generic indexes, look for corresponding name columns
	if strings.Contains(lower, "index") {
		prefix := strings.TrimSuffix(lower, "index")

		// Look for {prefix}name or {prefix}descr
		for _, col := range group.statusCols {
			colLower := strings.ToLower(col.Name)
			if colLower == prefix+"name" || colLower == prefix+"descr" {
				return colLower
			}
		}
	}

	// No friendly name found
	return ""
}
