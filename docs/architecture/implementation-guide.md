# MIB Dashboard Redesign - Implementation Guide

## Quick Start

This guide provides concrete code examples for implementing the redesigned architecture.

## Phase 1: Enhanced Parser

### 1.1 Add ObjectClass to types.go

```go
// internal/mib/types.go

// ObjectClass defines the semantic role of an OID
type ObjectClass string

const (
    ObjectClassScalar       ObjectClass = "scalar"
    ObjectClassTableNode    ObjectClass = "table_node"
    ObjectClassEntryNode    ObjectClass = "entry_node"
    ObjectClassIndexColumn  ObjectClass = "index_column"
    ObjectClassStatusColumn ObjectClass = "status_column"
    ObjectClassMetricColumn ObjectClass = "metric_column"
    ObjectClassUnknown      ObjectClass = "unknown"
)

// Add to OIDMetadata struct:
type OIDMetadata struct {
    // ... existing fields ...

    // NEW: Semantic classification
    ObjectClass  ObjectClass       `json:"object_class"`

    // NEW: Enhanced hierarchy
    TableOID     string            `json:"table_oid,omitempty"`
    EntryOID     string            `json:"entry_oid,omitempty"`

    // NEW: Index identification
    IsIndex      bool              `json:"is_index"`
    IndexFor     string            `json:"index_for,omitempty"`

    // NEW: Display metadata
    DisplayHint  string            `json:"display_hint,omitempty"`
    EnumValues   map[int64]string  `json:"enum_values,omitempty"`
    RangeMin     *int64            `json:"range_min,omitempty"`
    RangeMax     *int64            `json:"range_max,omitempty"`

    // NEW: Rendering hints
    IsChartable  bool              `json:"is_chartable"`
    IsDisplayable bool             `json:"is_displayable"`
    ChartType    string            `json:"chart_type,omitempty"`

    // ... rest of existing fields ...
}
```

### 1.2 Enhance parseNode() in parser.go

```go
// internal/mib/parser/parser.go

func (p *Parser) parseNode(node gosmi.SmiNode, moduleName string) *OIDMetadata {
    raw := node.GetRaw()
    numericOID := raw.Oid.String()

    meta := &mib.OIDMetadata{
        OID:         numericOID,
        Name:        string(raw.Name),
        Module:      moduleName,
        Description: string(raw.Description),
        CreatedAt:   time.Now(),
        UpdatedAt:   time.Now(),
        Tags:        make(map[string]string),
        EnumValues:  make(map[int64]string),
    }

    // Parse node kind
    switch raw.NodeKind {
    case types.NodeNode:
        meta.IsScalar = false
    case types.NodeScalar:
        meta.IsScalar = true
    case types.NodeTable:
        meta.IsTable = true
    case types.NodeRow:
        meta.IsTableEntry = true
    case types.NodeColumn:
        meta.IsColumn = true
    }

    // Parse type and extract enum values
    if node.SmiType != nil {
        rawType := node.SmiType.GetRaw()
        typeName := string(rawType.Name)
        if typeName == "" {
            typeName = p.getTypeName(rawType.BaseType)
        }
        meta.Type = typeName
        meta.Syntax = typeName

        if rawType.Units != "" {
            meta.Units = string(rawType.Units)
        }

        // NEW: Extract enum values for INTEGER types
        if rawType.BaseType == types.BaseTypeEnum {
            namedNumbers := node.SmiType.GetNamedNumbers()
            for _, nn := range namedNumbers {
                meta.EnumValues[int64(nn.Value)] = nn.Name
            }
        }

        // NEW: Extract ranges
        ranges := node.SmiType.GetRanges()
        if len(ranges) > 0 {
            minVal := int64(ranges[0].MinValue)
            maxVal := int64(ranges[0].MaxValue)
            meta.RangeMin = &minVal
            meta.RangeMax = &maxVal
        }
    }

    // Parse access
    meta.Access = p.getAccessString(raw.Access)
    meta.Status = p.getStatusString(raw.Status)

    // Get parent and build hierarchy
    if len(raw.Oid) > 1 {
        parentOid := raw.Oid[:len(raw.Oid)-1]
        if parentNode, err := gosmi.GetNodeByOID(parentOid); err == nil {
            meta.Parent = parentNode.RenderNumeric()

            // NEW: If parent is an entry, track table hierarchy
            parentRaw := parentNode.GetRaw()
            if parentRaw.NodeKind == types.NodeRow {
                meta.EntryOID = meta.Parent
                // Get grandparent (table)
                if len(parentOid) > 1 {
                    tableOid := parentOid[:len(parentOid)-1]
                    if tableNode, err := gosmi.GetNodeByOID(tableOid); err == nil {
                        meta.TableOID = tableNode.RenderNumeric()
                    }
                }
            }
        }
    }

    // Get index for table rows
    if raw.NodeKind == types.NodeRow {
        indexes := node.GetIndex()
        meta.Index = make([]string, len(indexes))
        for i, idx := range indexes {
            idxRaw := idx.GetRaw()
            meta.Index[i] = string(idxRaw.Name)
        }

        if augment := node.GetAugment(); augment.GetRaw() != nil {
            augRaw := augment.GetRaw()
            meta.Augments = string(augRaw.Name)
        }
    }

    // NEW: Determine object class and chartability
    meta.ObjectClass = p.determineObjectClass(meta, node)
    meta.IsChartable, meta.ChartType = p.determineChartability(meta)
    meta.IsDisplayable = p.determineDisplayability(meta)

    // Determine if pollable
    meta.Pollable = meta.IsPollable()

    return meta
}

// NEW: Determine the semantic class of an object
func (p *Parser) determineObjectClass(meta *mib.OIDMetadata, node gosmi.SmiNode) mib.ObjectClass {
    // Tables and entries are organizational nodes
    if meta.IsTable {
        return mib.ObjectClassTableNode
    }
    if meta.IsTableEntry {
        return mib.ObjectClassEntryNode
    }

    // Scalars are single-instance objects
    if meta.IsScalar {
        return mib.ObjectClassScalar
    }

    // Columns need further classification
    if meta.IsColumn {
        // Check if this column is an index
        if meta.EntryOID != "" {
            // Get the entry node to check its index list
            if entryNode, err := gosmi.GetNodeByOID(parseOID(meta.EntryOID)); err == nil {
                indexes := entryNode.GetIndex()
                for _, idx := range indexes {
                    if string(idx.GetRaw().Name) == meta.Name {
                        meta.IsIndex = true
                        meta.IndexFor = meta.EntryOID
                        return mib.ObjectClassIndexColumn
                    }
                }
            }
        }

        // Not an index, classify by type
        if meta.IsCounter() || meta.IsGauge() {
            return mib.ObjectClassMetricColumn
        }
        if meta.IsString() || len(meta.EnumValues) > 0 {
            return mib.ObjectClassStatusColumn
        }

        // Default for columns
        return mib.ObjectClassMetricColumn
    }

    return mib.ObjectClassUnknown
}

// NEW: Determine if object should be charted
func (p *Parser) determineChartability(meta *mib.OIDMetadata) (bool, string) {
    // Index columns should never be charted
    if meta.IsIndex {
        return false, ""
    }

    // Status/info columns should not be charted
    if meta.ObjectClass == mib.ObjectClassStatusColumn {
        return false, ""
    }

    // Table and entry nodes are not charted
    if meta.ObjectClass == mib.ObjectClassTableNode || meta.ObjectClass == mib.ObjectClassEntryNode {
        return false, ""
    }

    // Counters and gauges should be charted
    if meta.IsCounter() {
        return true, "line"
    }
    if meta.IsGauge() {
        // Use gauge for bounded values, line for unbounded
        if meta.RangeMax != nil {
            return true, "gauge"
        }
        return true, "line"
    }

    // Scalars can be charted if numeric
    if meta.ObjectClass == mib.ObjectClassScalar && !meta.IsString() {
        return true, "line"
    }

    return false, ""
}

// NEW: Determine if object should be displayed as text
func (p *Parser) determineDisplayability(meta *mib.OIDMetadata) bool {
    // Status columns should be displayed
    if meta.ObjectClass == mib.ObjectClassStatusColumn {
        return true
    }

    // Scalars can be displayed
    if meta.ObjectClass == mib.ObjectClassScalar {
        return true
    }

    return false
}

// Helper to parse OID string to gosmi OID
func parseOID(oidStr string) types.Oid {
    parts := strings.Split(strings.Trim(oidStr, "."), ".")
    oid := make(types.Oid, len(parts))
    for i, part := range parts {
        val, _ := strconv.ParseUint(part, 10, 32)
        oid[i] = types.SubId(val)
    }
    return oid
}
```

### 1.3 Add Enhanced Table Info

```go
// internal/mib/types.go

type ColumnInfo struct {
    OID          string            `json:"oid"`
    Name         string            `json:"name"`
    Type         string            `json:"type"`
    Syntax       string            `json:"syntax"`
    Description  string            `json:"description"`
    Units        string            `json:"units,omitempty"`
    DisplayHint  string            `json:"display_hint,omitempty"`
    EnumValues   map[int64]string  `json:"enum_values,omitempty"`
    IsIndex      bool              `json:"is_index"`
    IsChartable  bool              `json:"is_chartable"`
    ChartType    string            `json:"chart_type,omitempty"`
}

type TableInfo struct {
    OID         string       `json:"oid"`
    Name        string       `json:"name"`
    Module      string       `json:"module"`
    Description string       `json:"description"`
    EntryOID    string       `json:"entry_oid"`
    EntryName   string       `json:"entry_name"`

    // Categorized columns
    IndexColumns  []ColumnInfo `json:"index_columns"`
    StatusColumns []ColumnInfo `json:"status_columns"`
    MetricColumns []ColumnInfo `json:"metric_columns"`

    Augments      string       `json:"augments,omitempty"`
    AugmentedBy   []string     `json:"augmented_by,omitempty"`
    InstanceLabels []string    `json:"instance_labels"`
}
```

## Phase 2: Enhanced Storage

### 2.1 Add GetTableInfoEnhanced() method

```go
// internal/mib/storage/storage.go

// GetTableInfoEnhanced retrieves table information with categorized columns
func (s *Storage) GetTableInfoEnhanced(module, tableName string) (*mib.TableInfo, error) {
    s.mu.RLock()
    defer s.mu.RUnlock()

    // Get the table node
    tableKey := []byte(PrefixName + module + ":" + tableName)
    oidBytes, closer, err := s.db.Get(tableKey)
    if err != nil {
        return nil, fmt.Errorf("table %s::%s not found: %w", module, tableName, err)
    }
    tableOID := string(oidBytes)
    closer.Close()

    // Get table metadata
    tableMeta, err := s.GetOID(tableOID)
    if err != nil {
        return nil, err
    }

    if !tableMeta.IsTable {
        return nil, fmt.Errorf("%s is not a table", tableName)
    }

    // Find entry node (should be immediate child with .1 suffix)
    entryOID := tableOID + ".1"
    entryMeta, err := s.GetOID(entryOID)
    if err != nil {
        return nil, fmt.Errorf("entry not found for table %s: %w", tableName, err)
    }

    tableInfo := &mib.TableInfo{
        OID:           tableOID,
        Name:          tableMeta.Name,
        Module:        module,
        Description:   tableMeta.Description,
        EntryOID:      entryOID,
        EntryName:     entryMeta.Name,
        Augments:      entryMeta.Augments,
        IndexColumns:  make([]mib.ColumnInfo, 0),
        StatusColumns: make([]mib.ColumnInfo, 0),
        MetricColumns: make([]mib.ColumnInfo, 0),
        InstanceLabels: make([]string, 0),
    }

    // Get all columns (children of entry)
    allOIDs, err := s.ListOIDs(entryOID + ".")
    if err != nil {
        return nil, err
    }

    // Categorize columns
    for _, oid := range allOIDs {
        if oid.Module != module || !oid.IsColumn {
            continue
        }

        colInfo := mib.ColumnInfo{
            OID:         oid.OID,
            Name:        oid.Name,
            Type:        oid.Type,
            Syntax:      oid.Syntax,
            Description: oid.Description,
            Units:       oid.Units,
            DisplayHint: oid.DisplayHint,
            EnumValues:  oid.EnumValues,
            IsIndex:     oid.IsIndex,
            IsChartable: oid.IsChartable,
            ChartType:   oid.ChartType,
        }

        switch oid.ObjectClass {
        case mib.ObjectClassIndexColumn:
            tableInfo.IndexColumns = append(tableInfo.IndexColumns, colInfo)
            // Add to instance labels (use lowercase name)
            labelName := strings.ToLower(oid.Name)
            // Common label mappings
            if strings.Contains(labelName, "index") {
                tableInfo.InstanceLabels = append(tableInfo.InstanceLabels, "ifindex")
            } else if strings.Contains(labelName, "name") {
                tableInfo.InstanceLabels = append(tableInfo.InstanceLabels, "ifname")
            }

        case mib.ObjectClassStatusColumn:
            tableInfo.StatusColumns = append(tableInfo.StatusColumns, colInfo)

        case mib.ObjectClassMetricColumn:
            tableInfo.MetricColumns = append(tableInfo.MetricColumns, colInfo)
        }
    }

    // Ensure at least one instance label
    if len(tableInfo.InstanceLabels) == 0 && len(tableInfo.IndexColumns) > 0 {
        tableInfo.InstanceLabels = []string{"instance"}
    }

    return tableInfo, nil
}

// ListTablesEnhanced lists all tables in a module with enhanced metadata
func (s *Storage) ListTablesEnhanced(module string) ([]*mib.TableInfo, error) {
    // Get all OIDs for this module
    allOIDs, err := s.ListOIDs("")
    if err != nil {
        return nil, err
    }

    // Find all table nodes
    tableNames := make([]string, 0)
    for _, oid := range allOIDs {
        if oid.Module == module && oid.IsTable {
            tableNames = append(tableNames, oid.Name)
        }
    }

    // Get enhanced info for each table
    tables := make([]*mib.TableInfo, 0, len(tableNames))
    for _, tableName := range tableNames {
        tableInfo, err := s.GetTableInfoEnhanced(module, tableName)
        if err != nil {
            log.Printf("Warning: failed to get enhanced info for %s::%s: %v", module, tableName, err)
            continue
        }
        tables = append(tables, tableInfo)
    }

    return tables, nil
}
```

## Phase 3: Dashboard Generator

### 3.1 Create dashboard package

```go
// internal/mib/dashboard/types.go

package dashboard

// DashboardSpec defines how to render a MIB dashboard
type DashboardSpec struct {
    MIBName     string         `json:"mib_name"`
    MIBModule   string         `json:"mib_module"`
    Description string         `json:"description"`
    Scalars     []ScalarWidget `json:"scalars,omitempty"`
    Tables      []TableWidget  `json:"tables,omitempty"`
}

type ScalarWidget struct {
    OID           string            `json:"oid"`
    Name          string            `json:"name"`
    DisplayName   string            `json:"display_name"`
    Type          string            `json:"type"`
    Description   string            `json:"description"`
    Units         string            `json:"units,omitempty"`
    WidgetType    string            `json:"widget_type"`
    EnumValues    map[int64]string  `json:"enum_values,omitempty"`
    QueryTemplate string            `json:"query_template"`
}

type TableWidget struct {
    TableName        string           `json:"table_name"`
    Description      string           `json:"description"`
    InstanceSelector InstanceSelector `json:"instance_selector"`
    StatusFields     []StatusField    `json:"status_fields,omitempty"`
    MetricCharts     []MetricChart    `json:"metric_charts,omitempty"`
}

type InstanceSelector struct {
    LabelName             string   `json:"label_name"`
    DisplayLabel          string   `json:"display_label"`
    IndexColumns          []string `json:"index_columns"`
    InstanceQueryTemplate string   `json:"instance_query_template"`
}

type StatusField struct {
    OID           string            `json:"oid"`
    Name          string            `json:"name"`
    DisplayName   string            `json:"display_name"`
    Description   string            `json:"description"`
    Type          string            `json:"type"`
    EnumValues    map[int64]string  `json:"enum_values,omitempty"`
    QueryTemplate string            `json:"query_template"`
}

type MetricChart struct {
    OID           string `json:"oid"`
    Name          string `json:"name"`
    DisplayName   string `json:"display_name"`
    Description   string `json:"description"`
    Type          string `json:"type"`
    Units         string `json:"units,omitempty"`
    ChartType     string `json:"chart_type"`
    QueryTemplate string `json:"query_template"`
}
```

```go
// internal/mib/dashboard/generator.go

package dashboard

import (
    "fmt"
    "strings"

    "github.com/jeremyhahn/go-netmon/internal/mib"
    "github.com/jeremyhahn/go-netmon/internal/mib/storage"
)

type Generator struct {
    storage *storage.Storage
}

func NewGenerator(storage *storage.Storage) *Generator {
    return &Generator{storage: storage}
}

// Generate creates a dashboard specification for a MIB module
func (g *Generator) Generate(moduleName string) (*DashboardSpec, error) {
    // Get module info
    moduleInfo, err := g.storage.GetModule(moduleName)
    if err != nil {
        return nil, fmt.Errorf("module not found: %w", err)
    }

    spec := &DashboardSpec{
        MIBName:     moduleName,
        MIBModule:   moduleName,
        Description: moduleInfo.Description,
        Scalars:     make([]ScalarWidget, 0),
        Tables:      make([]TableWidget, 0),
    }

    // Get all OIDs for this module
    allOIDs, err := g.storage.ListOIDs("")
    if err != nil {
        return nil, err
    }

    // Filter for this module and categorize
    for _, oid := range allOIDs {
        if oid.Module != moduleName {
            continue
        }

        // Generate scalar widgets
        if oid.ObjectClass == mib.ObjectClassScalar {
            widget := g.generateScalarWidget(oid)
            spec.Scalars = append(spec.Scalars, widget)
        }
    }

    // Get tables
    tables, err := g.storage.ListTablesEnhanced(moduleName)
    if err != nil {
        return nil, err
    }

    // Generate table widgets
    for _, table := range tables {
        widget := g.generateTableWidget(table)
        spec.Tables = append(spec.Tables, widget)
    }

    return spec, nil
}

func (g *Generator) generateScalarWidget(oid *mib.OIDMetadata) ScalarWidget {
    return ScalarWidget{
        OID:           oid.OID,
        Name:          oid.Name,
        DisplayName:   humanize(oid.Name),
        Type:          oid.Type,
        Description:   oid.Description,
        Units:         oid.Units,
        WidgetType:    g.determineWidgetType(oid),
        EnumValues:    oid.EnumValues,
        QueryTemplate: g.buildQueryTemplate(oid.OID, false, "", ""),
    }
}

func (g *Generator) generateTableWidget(table *mib.TableInfo) TableWidget {
    widget := TableWidget{
        TableName:    table.Name,
        Description:  table.Description,
        StatusFields: make([]StatusField, 0),
        MetricCharts: make([]MetricChart, 0),
    }

    // Generate instance selector
    labelName := "instance"
    if len(table.InstanceLabels) > 0 {
        labelName = table.InstanceLabels[0]
    }

    indexColumns := make([]string, len(table.IndexColumns))
    for i, col := range table.IndexColumns {
        indexColumns[i] = col.Name
    }

    // Use first index column for instance query
    var instanceQueryOID string
    if len(table.IndexColumns) > 0 {
        instanceQueryOID = table.IndexColumns[0].OID
    }

    widget.InstanceSelector = InstanceSelector{
        LabelName:             labelName,
        DisplayLabel:          humanize(table.Name) + " Instance",
        IndexColumns:          indexColumns,
        InstanceQueryTemplate: g.buildInstanceQueryTemplate(instanceQueryOID, labelName),
    }

    // Generate status fields
    for _, col := range table.StatusColumns {
        field := StatusField{
            OID:           col.OID,
            Name:          col.Name,
            DisplayName:   humanize(col.Name),
            Description:   col.Description,
            Type:          col.Type,
            EnumValues:    col.EnumValues,
            QueryTemplate: g.buildQueryTemplate(col.OID, false, labelName, "{{instance}}"),
        }
        widget.StatusFields = append(widget.StatusFields, field)
    }

    // Generate metric charts
    for _, col := range table.MetricColumns {
        isCounter := strings.Contains(col.Type, "Counter")
        chart := MetricChart{
            OID:           col.OID,
            Name:          col.Name,
            DisplayName:   humanize(col.Name),
            Description:   col.Description,
            Type:          col.Type,
            Units:         col.Units,
            ChartType:     col.ChartType,
            QueryTemplate: g.buildQueryTemplate(col.OID, isCounter, labelName, "{{instance}}"),
        }
        widget.MetricCharts = append(widget.MetricCharts, chart)
    }

    return widget
}

func (g *Generator) buildQueryTemplate(object *mib.OIDMetadata, useRate bool, labelName, labelValue string) string {
    // The exporter names a metric after the MIB object, not after an instance
    // OID, so every row of a column shares one name. See docs/metric-naming.md.
    metricName := promname.MetricName(object.Name)

    // Build label selectors
    labels := `device="{{device}}"`
    if labelName != "" && labelValue != "" {
        labels += fmt.Sprintf(`,%s="%s"`, labelName, labelValue)
    }

    query := fmt.Sprintf("%s{%s}", metricName, labels)

    if useRate {
        query = fmt.Sprintf("rate(%s[5m])", query)
    }

    return query
}

func (g *Generator) buildInstanceQueryTemplate(indexColumn *mib.OIDMetadata) string {
    label := promname.IndexLabel(indexColumn.Name)
    return fmt.Sprintf("label_values({device=\"{{device}}\"}, %s)", label)
}

func (g *Generator) determineWidgetType(oid *mib.OIDMetadata) string {
    if len(oid.EnumValues) > 0 || oid.IsString() {
        return "status"
    }
    if oid.RangeMax != nil {
        return "gauge"
    }
    return "chart"
}

func humanize(name string) string {
    // Convert camelCase or snake_case to Title Case
    // ifInOctets → If In Octets
    // bgp_peer_state → Bgp Peer State

    var result strings.Builder
    for i, r := range name {
        if i > 0 && (r >= 'A' && r <= 'Z') {
            result.WriteRune(' ')
        }
        if i == 0 {
            result.WriteRune(r)
        } else {
            result.WriteRune(r)
        }
    }
    return strings.ReplaceAll(result.String(), "_", " ")
}
```

(continued in next message due to length...)
