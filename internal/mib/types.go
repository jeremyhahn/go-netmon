// Package mib holds the MIB data model shared across the project: OID metadata,
// module and table descriptions, and the value formatting they imply.
package mib

import (
	"fmt"
	"time"
)

// ObjectClass defines the semantic role of an OID
type ObjectClass string

// The object classes an OID can be assigned to.
const (
	ObjectClassScalar       ObjectClass = "scalar"
	ObjectClassTableNode    ObjectClass = "table_node"
	ObjectClassEntryNode    ObjectClass = "entry_node"
	ObjectClassIndexColumn  ObjectClass = "index_column"
	ObjectClassStatusColumn ObjectClass = "status_column"
	ObjectClassMetricColumn ObjectClass = "metric_column"
	ObjectClassUnknown      ObjectClass = "unknown"
)

// OIDMetadata contains metadata about an SNMP OID
type OIDMetadata struct {
	OID          string            `json:"oid"`
	Name         string            `json:"name"`
	Module       string            `json:"module"`
	Type         string            `json:"type"`   // Counter32, Gauge32, Integer, OctetString, etc.
	Access       string            `json:"access"` // read-only, read-write, not-accessible
	Status       string            `json:"status"` // current, deprecated, obsolete
	Description  string            `json:"description"`
	Units        string            `json:"units,omitempty"`
	Syntax       string            `json:"syntax,omitempty"`
	Parent       string            `json:"parent,omitempty"`
	Index        []string          `json:"index,omitempty"`         // For tables, index column names only
	IndexColumns []IndexColumn     `json:"index_columns,omitempty"` // For tables, index columns with resolved types
	Augments     string            `json:"augments,omitempty"`      // For augmented tables
	IsTable      bool              `json:"is_table"`
	IsTableEntry bool              `json:"is_table_entry"`
	IsColumn     bool              `json:"is_column"`
	IsScalar     bool              `json:"is_scalar"`
	Pollable     bool              `json:"pollable"` // Whether this OID should be polled
	CreatedAt    time.Time         `json:"created_at"`
	UpdatedAt    time.Time         `json:"updated_at"`
	Tags         map[string]string `json:"tags,omitempty"`

	// NEW: Semantic classification
	ObjectClass ObjectClass `json:"object_class"`

	// NEW: Enhanced hierarchy
	TableOID string `json:"table_oid,omitempty"`
	EntryOID string `json:"entry_oid,omitempty"`

	// NEW: Index identification
	IsIndex  bool   `json:"is_index"`
	IndexFor string `json:"index_for,omitempty"`

	// NEW: Display metadata
	DisplayHint string           `json:"display_hint,omitempty"`
	EnumValues  map[int64]string `json:"enum_values,omitempty"`
	RangeMin    *int64           `json:"range_min,omitempty"`
	RangeMax    *int64           `json:"range_max,omitempty"`

	// NEW: Rendering hints
	IsChartable   bool   `json:"is_chartable"`
	IsDisplayable bool   `json:"is_displayable"`
	ChartType     string `json:"chart_type,omitempty"`
}

// IndexColumn describes one column of a conceptual row's INDEX clause,
// carrying everything an index decoder needs to split an OID suffix into
// values: the resolved type, its SMI base type, whether the column is IMPLIED
// and, for fixed-width strings, the octet count.
type IndexColumn struct {
	Name string `json:"name"`
	OID  string `json:"oid,omitempty"`
	// Type is the resolved SNMP type name, for example "InterfaceIndex",
	// "InetAddressType" or "OctetString". It is empty when the defining
	// module is not on the MIB search path.
	Type string `json:"type,omitempty"`
	// BaseType is the SMI base type the resolved type reduces to, for
	// example "Integer32", "OctetString" or "ObjectIdentifier". Decoders
	// key off this rather than Type, which may name any textual convention.
	BaseType string `json:"base_type,omitempty"`
	// Implied is true only for the final index column of an IMPLIED row,
	// whose value fills the rest of the OID with no length prefix.
	Implied bool `json:"implied,omitempty"`
	// FixedLength is the octet count of a string index with a fixed SIZE
	// constraint, and zero otherwise. A fixed-width string index carries no
	// length prefix, so the decoder needs the width to split it.
	FixedLength int `json:"fixed_length,omitempty"`
}

// ModuleInfo contains information about a MIB module
type ModuleInfo struct {
	Name         string            `json:"name"`
	Path         string            `json:"path"`
	Organization string            `json:"organization,omitempty"`
	ContactInfo  string            `json:"contact_info,omitempty"`
	Description  string            `json:"description,omitempty"`
	Revision     string            `json:"revision,omitempty"`
	Imports      []string          `json:"imports,omitempty"`
	OIDCount     int               `json:"oid_count"`
	LoadedAt     time.Time         `json:"loaded_at"`
	Tags         map[string]string `json:"tags,omitempty"`
}

// TableInfo contains information about an SNMP table
type TableInfo struct {
	OID         string   `json:"oid"`
	Name        string   `json:"name"`
	Module      string   `json:"module"`
	Description string   `json:"description"`
	Indexes     []string `json:"indexes"`
	Columns     []string `json:"columns"` // Column OIDs
	EntryOID    string   `json:"entry_oid"`

	// IndexColumns carries the same indexes as Indexes but with their
	// resolved types; Implied reports whether the row's INDEX clause ends
	// with an IMPLIED column.
	IndexColumns []IndexColumn `json:"index_columns,omitempty"`
	Implied      bool          `json:"implied,omitempty"`
}

// PollingProfile defines which OIDs to poll for a device
type PollingProfile struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Modules     []string `json:"modules"`  // MIB modules to include
	OIDs        []string `json:"oids"`     // Specific OIDs to poll
	Tables      []string `json:"tables"`   // Tables to poll
	Interval    int      `json:"interval"` // Polling interval in seconds
	Enabled     bool     `json:"enabled"`
}

// String returns a string representation of OID metadata
func (o *OIDMetadata) String() string {
	return fmt.Sprintf("%s (%s) - %s [%s]", o.Name, o.OID, o.Type, o.Access)
}

// IsPollable returns true if this OID should be polled
func (o *OIDMetadata) IsPollable() bool {
	// Must be readable
	if o.Access != "read-only" && o.Access != "read-write" {
		return false
	}

	// Must be current (not deprecated or obsolete)
	if o.Status != "current" {
		return false
	}

	// Tables and table entries themselves aren't pollable (their columns are).
	// A separate not-accessible check would be dead code here: the access
	// guard above already admits only read-only and read-write.
	if o.IsTable || o.IsTableEntry {
		return false
	}

	return true
}

// IsCounter returns true if this is a counter type
func (o *OIDMetadata) IsCounter() bool {
	return o.Type == "Counter32" || o.Type == "Counter64" || o.Type == "Counter" ||
		o.Type == "TimeTicks" // TimeTicks is counter-like (monotonically increasing)
}

// IsGauge returns true if this is a gauge type
func (o *OIDMetadata) IsGauge() bool {
	return o.Type == "Gauge32" || o.Type == "Gauge" ||
		o.Type == "Integer32" || o.Type == "Integer" ||
		o.Type == "Unsigned32"
}

// IsString returns true if this is a string type
func (o *OIDMetadata) IsString() bool {
	return o.Type == "OctetString" || o.Type == "DisplayString"
}

// String returns a string representation of module info
func (m *ModuleInfo) String() string {
	return fmt.Sprintf("%s (%d OIDs) - loaded %s", m.Name, m.OIDCount, m.LoadedAt.Format("2006-01-02"))
}

// String returns a string representation of table info
func (t *TableInfo) String() string {
	return fmt.Sprintf("%s (%s) - %d columns", t.Name, t.OID, len(t.Columns))
}
