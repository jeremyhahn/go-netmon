// Package parser reads MIB module definitions and turns them into the OID,
// table and module metadata the rest of the project consumes.
package parser

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/sleepinggenius2/gosmi"
	"github.com/sleepinggenius2/gosmi/models"
	"github.com/sleepinggenius2/gosmi/types"

	"github.com/jeremyhahn/go-netmon/internal/mib"
)

// Parser handles MIB parsing using gosmi
type Parser struct {
	paths []string
}

// New creates a new MIB parser
func New(paths ...string) *Parser {
	return &Parser{
		paths: paths,
	}
}

// Init initializes the gosmi library with MIB paths
func (p *Parser) Init() error {
	gosmi.Init()

	// Set MIB paths
	for _, path := range p.paths {
		gosmi.AppendPath(path)
	}

	return nil
}

// Close cleans up the gosmi library
func (p *Parser) Close() {
	gosmi.Exit()
}

// LoadModule loads a MIB module by name
func (p *Parser) LoadModule(moduleName string) (*mib.ModuleInfo, error) {
	// Load the module
	_, err := gosmi.LoadModule(moduleName)
	if err != nil {
		return nil, fmt.Errorf("failed to load module %s: %w", moduleName, err)
	}

	// Get the loaded module
	module, err := gosmi.GetModule(moduleName)
	if err != nil {
		return nil, fmt.Errorf("failed to get module %s: %w", moduleName, err)
	}

	rawModule := module.GetRaw()

	info := &mib.ModuleInfo{
		Name:         string(rawModule.Name),
		Path:         rawModule.Path,
		Organization: rawModule.Organization,
		ContactInfo:  rawModule.ContactInfo,
		Description:  rawModule.Description,
		LoadedAt:     time.Now(),
		Tags:         make(map[string]string),
	}

	// Get OID count
	nodes := module.GetNodes()
	info.OIDCount = len(nodes)

	// Get imports
	imports := module.GetImports()
	info.Imports = make([]string, len(imports))
	for i, imp := range imports {
		info.Imports[i] = imp.Module
	}

	return info, nil
}

// ParseModule parses a MIB module and returns all OIDs
func (p *Parser) ParseModule(moduleName string) ([]*mib.OIDMetadata, error) {
	// Load the module
	_, err := gosmi.LoadModule(moduleName)
	if err != nil {
		return nil, fmt.Errorf("failed to load module: %w", err)
	}

	module, err := gosmi.GetModule(moduleName)
	if err != nil {
		return nil, fmt.Errorf("failed to get module: %w", err)
	}

	nodes := module.GetNodes()
	oids := make([]*mib.OIDMetadata, 0, len(nodes))

	for _, node := range nodes {
		meta := p.parseNode(node, moduleName)
		if meta != nil {
			oids = append(oids, meta)
		}
	}

	return oids, nil
}

// parseNode converts a gosmi node to OIDMetadata
func (p *Parser) parseNode(node gosmi.SmiNode, moduleName string) *mib.OIDMetadata {
	raw := node.GetRaw()

	// FIX: Use node.RenderNumeric() instead of raw.Oid.String()
	// The raw.Oid.String() method returns empty strings for some MIBs (e.g., CISCO-BGP4-MIB)
	// while node.RenderNumeric() correctly renders the OID in all cases
	numericOID := node.RenderNumeric()

	// FALLBACK: If gosmi returns empty OID, try to construct from children.
	// Table entries are the only node kind observed to hit this.
	if numericOID == "" && string(raw.Name) != "" && raw.NodeKind == types.NodeRow {
		numericOID = p.getOIDFromFirstChild(node, moduleName)
	}

	meta := &mib.OIDMetadata{
		OID:         numericOID,
		Name:        string(raw.Name),
		Module:      moduleName,
		Description: raw.Description,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
		Tags:        make(map[string]string),
		EnumValues:  make(map[int64]string),
	}

	// Parse node kind
	switch raw.NodeKind {
	case types.NodeNode:
		// NodeNode can be either a scalar or an organizational node
		// We'll determine this more precisely below
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

	// FIX: Improved scalar detection for NodeNode types
	// A NodeNode should only be marked as scalar if it has a type definition.
	// Organizational nodes (like "system", "snmp") have NodeKind=Node but no type (SmiType == nil).
	// Actual leaf scalar nodes have both NodeKind=Node (or NodeScalar) and a type definition.
	if raw.NodeKind == types.NodeNode && !meta.IsTable && !meta.IsTableEntry && !meta.IsColumn {
		// Only mark as scalar if this node has a type definition
		// Organizational/parent nodes do NOT have a type, they're just containers
		if node.SmiType != nil {
			meta.IsScalar = true
		}
	}

	// Parse type, units, enums, ranges and display hint.
	//
	// All of these are read from the resolved type (node.SmiType), never from
	// node.SmiType.GetRaw(). GetRaw returns the type exactly as written in the
	// SYNTAX clause: for a subtyped textual convention such as
	// "DisplayString (SIZE (0..255))" that is an anonymous type carrying no
	// name, no DISPLAY-HINT and no UNITS. gosmi's CreateType walks to the
	// parent type to fill those in, and CreateTypeFromNode then overlays the
	// UNITS and DISPLAY-HINT declared on the OBJECT-TYPE itself. Reading the
	// raw type silently discards both.
	if node.SmiType != nil {
		resolved := node.SmiType

		// Prefer the declared type name (Counter32, DisplayString, TruthValue)
		// over the base type, since downstream code keys counter-vs-gauge and
		// textual-convention handling off it.
		typeName := resolved.Name
		if typeName == "" {
			typeName = p.getTypeName(resolved.BaseType)
		}
		meta.Type = typeName
		meta.Syntax = typeName

		meta.Units = resolved.Units
		meta.DisplayHint = resolved.Format

		if resolved.Enum != nil {
			for _, named := range resolved.Enum.Values {
				meta.EnumValues[named.Value] = named.Name
			}
		}

		// A type may declare several disjoint ranges (e.g. "1..4 | 8..15").
		// Collapse them to the outer bounds, which is what callers use for
		// axis scaling and validation.
		for i, r := range resolved.Ranges {
			if i == 0 || r.MinValue < *meta.RangeMin {
				minVal := r.MinValue
				meta.RangeMin = &minVal
			}
			if i == 0 || r.MaxValue > *meta.RangeMax {
				maxVal := r.MaxValue
				meta.RangeMax = &maxVal
			}
		}
	}

	// Parse access
	meta.Access = p.getAccessString(raw.Access)

	// Parse status
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
		meta.IndexColumns = p.indexColumns(node, indexes)

		// Check for augments
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

// getOIDFromFirstChild derives a table entry's OID from one of its columns,
// for the MIBs where gosmi renders the row node itself as an empty OID.
// A column such as .1.3.6.1.4.1.9.9.187.1.2.5.1.1 yields the entry OID
// .1.3.6.1.4.1.9.9.187.1.2.5.1 by dropping the final sub-identifier.
//
// Columns come straight from gosmi's child list rather than a scan of every
// node in the module, which would cost a node lookup per candidate.
func (p *Parser) getOIDFromFirstChild(entryNode gosmi.SmiNode, _ string) string {
	columns, order := entryNode.GetColumns()

	for _, name := range order {
		childOID := strings.TrimPrefix(columns[name].RenderNumeric(), ".")
		if childOID == "" {
			continue
		}

		parts := strings.Split(childOID, ".")
		if len(parts) < 2 {
			continue
		}
		return strings.Join(parts[:len(parts)-1], ".")
	}

	return ""
}

// indexColumns describes a row's INDEX clause: the name, OID and resolved type
// of every index column, plus the IMPLIED flag.
//
// Types come from the resolved node type (idx.SmiType), never from
// idx.SmiType.GetRaw(), for the reason spelled out in parseNode: the raw type
// is the SYNTAX as written, which is anonymous for a subtyped textual
// convention such as "SnmpAdminString (SIZE (1..32))" and so carries neither a
// name nor its SIZE constraint. An index column whose type lives in a module
// that is not on the search path has no resolved type at all; it still gets an
// entry, with empty Type and BaseType, so callers can fall back to a heuristic
// rather than mistake the row for having fewer indexes than it does.
func (p *Parser) indexColumns(row gosmi.SmiNode, indexes []gosmi.SmiNode) []mib.IndexColumn {
	if len(indexes) == 0 {
		return nil
	}

	// IMPLIED applies to the final index column only: it is what lets that
	// column run to the end of the OID without a length prefix.
	implied := row.GetImplied()

	columns := make([]mib.IndexColumn, len(indexes))
	for i, idx := range indexes {
		column := mib.IndexColumn{
			Name:    string(idx.GetRaw().Name),
			OID:     idx.RenderNumeric(),
			Implied: implied && i == len(indexes)-1,
		}

		if idx.SmiType != nil {
			resolved := &idx.SmiType.Type
			column.Type = resolved.Name
			if column.Type == "" {
				column.Type = p.getTypeName(resolved.BaseType)
			}
			if resolved.BaseType != types.BaseTypeUnknown {
				column.BaseType = resolved.BaseType.String()
			}
			column.FixedLength = fixedOctetLength(resolved)
		}

		columns[i] = column
	}
	return columns
}

// fixedOctetLength returns the octet count of a string type constrained to a
// single exact SIZE, and zero for every other type. A fixed-width string index
// is encoded without a leading length sub-identifier, so a decoder can only
// split it if it knows the width up front.
func fixedOctetLength(t *models.Type) int {
	if t == nil || t.BaseType != types.BaseTypeOctetString || len(t.Ranges) != 1 {
		return 0
	}

	r := t.Ranges[0]
	if r.MinValue != r.MaxValue || r.MinValue <= 0 {
		return 0
	}
	return int(r.MinValue)
}

// determineObjectClass determines the semantic class of an object
func (p *Parser) determineObjectClass(meta *mib.OIDMetadata, _ gosmi.SmiNode) mib.ObjectClass {
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

// isTextualConvention checks if a type is a textual convention that shouldn't be charted
func (p *Parser) isTextualConvention(typeName string) bool {
	// Common textual conventions that represent timestamps, dates, or boolean values
	// These should NOT be charted as time-series metrics
	textualConventions := []string{
		"TimeStamp",      // Represents a timestamp value
		"DateAndTime",    // Date and time representation
		"TruthValue",     // Boolean true/false
		"RowStatus",      // Row status in tables
		"StorageType",    // Storage type indication
		"TestAndIncr",    // Test-and-increment value
		"AutonomousType", // OID-based type indication
	}

	for _, tc := range textualConventions {
		if typeName == tc {
			return true
		}
	}
	return false
}

// isBitmaskDescription checks if description indicates a bitmask
func (p *Parser) isBitmaskDescription(description string) bool {
	descLower := strings.ToLower(description)

	// Strong bitmask indicators
	bitmaskKeywords := []string{
		"bitmask",
		"bit mask",
		"value is a sum",  // sysServices uses this pattern
		"sum of",          // Common bitmask pattern
		"2 raised to",     // Mathematical bitmask indicator
		"2^",              // Power-of-2 bitmask
		"bit is set",      // Direct bit reference
		"bits are set",    // Multiple bits
		"each bit",        // Per-bit meaning
		"capability bits", // Capability bitmask
	}

	for _, keyword := range bitmaskKeywords {
		if strings.Contains(descLower, keyword) {
			return true
		}
	}

	// Check for OSI layer patterns (e.g., "layer 1", "layer 2", etc.)
	// But not general "layer" references like "sub-layer" or "physical layer"
	// Look for "layer" followed by a digit or colon in table format
	osiLayerPattern := regexp.MustCompile(`layer\s*[0-9:]`)
	return osiLayerPattern.MatchString(descLower)
}

// isStaticConfigValue checks if name/description indicates a static configuration value
func (p *Parser) isStaticConfigValue(name, description string) bool {
	nameLower := strings.ToLower(name)
	descLower := strings.ToLower(description)

	// Don't flag counters - they count events, even if name has "version"
	// (e.g., snmpInBadVersions counts packets with bad versions)
	if strings.Contains(nameLower, "count") || strings.Contains(nameLower, "total") ||
		strings.Contains(nameLower, "bad") || strings.Contains(nameLower, "error") ||
		strings.Contains(nameLower, "drop") || strings.Contains(nameLower, "discard") {
		return false
	}

	// Check NAME for version/type patterns (but not in counter contexts)
	// These typically indicate static identification, not time-series metrics
	if strings.Contains(nameLower, "version") && !strings.Contains(nameLower, "badversion") {
		// Allow version if it's clearly a counter (e.g., "badversions")
		return true
	}
	if strings.Contains(nameLower, "type") && !strings.Contains(descLower, "number of") {
		// "type" usually indicates static config, unless description says "number of"
		return true
	}

	// Check NAME + DESCRIPTION for other static patterns
	combined := nameLower + " " + descLower
	staticKeywords := []string{
		"mtu",        // Maximum transmission unit - static config
		"capability", // Capability indication
		"feature",    // Feature flag
		"max size",   // Maximum size configuration
		"table size", // Table size configuration
		"admin",      // Administrative setting
		"configured", // Configured value
	}

	for _, keyword := range staticKeywords {
		if strings.Contains(combined, keyword) {
			return true
		}
	}

	return false
}

// determineChartability determines if object should be charted
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

	// String types and OIDs should never be charted
	if meta.IsString() {
		return false, ""
	}
	if meta.Type == "ObjectIdentifier" {
		return false, ""
	}

	// Check for textual conventions that shouldn't be charted
	if p.isTextualConvention(meta.Type) || p.isTextualConvention(meta.Syntax) {
		return false, ""
	}

	// Check for enum values - enums are status, not chartable
	if len(meta.EnumValues) > 0 {
		return false, ""
	}

	// Check for bitmask indicators in description
	if p.isBitmaskDescription(meta.Description) {
		return false, ""
	}

	// Check for static configuration values
	if p.isStaticConfigValue(meta.Name, meta.Description) {
		return false, ""
	}

	// Counters should be charted (they're inherently time-series)
	if meta.IsCounter() {
		return true, "line"
	}

	// For Gauge types, use more careful analysis
	if meta.IsGauge() {
		// Check if it's a time-varying metric vs static config
		nameLower := strings.ToLower(meta.Name)
		descLower := strings.ToLower(meta.Description)

		// Keywords indicating time-series metrics worth charting
		timeSeriesKeywords := []string{
			"speed", "bandwidth", "rate", "usage", "util",
			"count", "number", "queue", "buffer", "active",
			"current", "available", "free", "used",
		}

		for _, keyword := range timeSeriesKeywords {
			if strings.Contains(nameLower, keyword) || strings.Contains(descLower, keyword) {
				// Use gauge for bounded values, line for unbounded
				if meta.RangeMax != nil {
					return true, "gauge"
				}
				return true, "line"
			}
		}

		// If no clear time-series indicator, don't chart
		return false, ""
	}

	// For scalars, be conservative - only chart if they're numeric counters/gauges
	// that passed all the checks above
	if meta.ObjectClass == mib.ObjectClassScalar {
		// Already filtered out non-chartable types above
		// If we get here, it's a numeric scalar that might be chartable
		// But be conservative - require explicit Counter/Gauge type
		return false, ""
	}

	return false, ""
}

// determineDisplayability determines if object should be displayed as text
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

// parseOID converts an OID string to gosmi OID type
func parseOID(oidStr string) types.Oid {
	parts := strings.Split(strings.Trim(oidStr, "."), ".")
	oid := make(types.Oid, len(parts))
	for i, part := range parts {
		val, _ := strconv.ParseUint(part, 10, 32)
		oid[i] = types.SmiSubId(val)
	}
	return oid
}

// getTypeName converts gosmi base type to string
func (p *Parser) getTypeName(baseType types.BaseType) string {
	switch baseType {
	case types.BaseTypeInteger32:
		return "Integer32"
	case types.BaseTypeInteger64:
		return "Integer64"
	case types.BaseTypeUnsigned32:
		return "Unsigned32"
	case types.BaseTypeUnsigned64:
		return "Unsigned64"
	case types.BaseTypeOctetString:
		return "OctetString"
	case types.BaseTypeObjectIdentifier:
		return "ObjectIdentifier"
	case types.BaseTypeEnum:
		return "Integer"
	case types.BaseTypeBits:
		return "Bits"
	default:
		return "Unknown"
	}
}

// getAccessString converts gosmi access to string
func (p *Parser) getAccessString(access types.Access) string {
	switch access {
	case types.AccessNotAccessible:
		return "not-accessible"
	case types.AccessReadOnly:
		return "read-only"
	case types.AccessReadWrite:
		return "read-write"
	case types.AccessNotify:
		return "accessible-for-notify"
	default:
		return "unknown"
	}
}

// getStatusString converts gosmi status to string
func (p *Parser) getStatusString(status types.Status) string {
	switch status {
	case types.StatusCurrent:
		return "current"
	case types.StatusDeprecated:
		return "deprecated"
	case types.StatusObsolete:
		return "obsolete"
	case types.StatusMandatory:
		return "mandatory"
	case types.StatusOptional:
		return "optional"
	default:
		return "unknown"
	}
}

// GetTableInfo extracts table information from a module
func (p *Parser) GetTableInfo(moduleName string) ([]*mib.TableInfo, error) {
	module, err := gosmi.GetModule(moduleName)
	if err != nil {
		return nil, fmt.Errorf("failed to get module: %w", err)
	}

	nodes := module.GetNodes(types.NodeTable)
	tables := make([]*mib.TableInfo, 0, len(nodes))

	for _, node := range nodes {
		table := p.parseTable(node, moduleName)
		if table != nil {
			tables = append(tables, table)
		}
	}

	return tables, nil
}

// parseTable converts a table node to TableInfo
func (p *Parser) parseTable(node gosmi.SmiNode, moduleName string) *mib.TableInfo {
	raw := node.GetRaw()

	table := &mib.TableInfo{
		OID:         node.RenderNumeric(),
		Name:        string(raw.Name),
		Module:      moduleName,
		Description: raw.Description,
		Columns:     make([]string, 0),
	}

	// Get the row node
	row := node.AsTable().GetRow()
	if row.GetRaw() != nil {
		table.EntryOID = row.RenderNumeric()

		// Get indexes
		indexes := row.GetIndex()
		table.Indexes = make([]string, len(indexes))
		for i, idx := range indexes {
			idxRaw := idx.GetRaw()
			table.Indexes[i] = string(idxRaw.Name)
		}
		table.IndexColumns = p.indexColumns(row, indexes)
		table.Implied = row.GetImplied()

		// Get columns
		columns, columnOrder := node.AsTable().GetColumns()
		table.Columns = make([]string, len(columnOrder))
		for i, colName := range columnOrder {
			if col, ok := columns[colName]; ok {
				table.Columns[i] = col.RenderNumeric()
			}
		}
	}

	return table
}

// LoadModuleFromFile loads a MIB module from a file path
func (p *Parser) LoadModuleFromFile(path string) (*mib.ModuleInfo, error) {
	// Add the directory to the search path
	dir := filepath.Dir(path)
	gosmi.AppendPath(dir)

	// Extract module name from filename
	filename := filepath.Base(path)
	moduleName := strings.TrimSuffix(filename, filepath.Ext(filename))

	return p.LoadModule(moduleName)
}

// GetModules returns a list of all loaded modules
func (p *Parser) GetModules() []string {
	modules := gosmi.GetLoadedModules()
	names := make([]string, len(modules))
	for i, mod := range modules {
		raw := mod.GetRaw()
		names[i] = string(raw.Name)
	}
	return names
}
