package metrics

import (
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jeremyhahn/go-netmon/internal/mib"
	"github.com/jeremyhahn/go-netmon/internal/mib/storage"
	"github.com/jeremyhahn/go-netmon/internal/snmp"
)

// TableIndexCache holds the MIB-derived metadata that names and labels the
// samples of a poll, plus the friendly names observed while polling.
//
// The MIB-derived half is rebuilt whenever the set of loaded MIB modules
// changes and published as one immutable mibIndex (see Refresh), so the poll
// path reads it without taking a lock. The friendly names are the other half:
// they are learned from SNMP responses rather than from MIBs, are written on
// every poll, and so stay behind a mutex.
type TableIndexCache struct {
	storage *storage.Storage

	// published is the current immutable MIB-derived index. It is nil until
	// the first sweep completes; readers go through index() to get the empty
	// index instead.
	published atomic.Pointer[mibIndex]

	// buildMu serializes index construction: a store sweep and a learner
	// export must not interleave, or one would publish an index missing what
	// the other just added. It is never held by a reader, so a rebuild --
	// tens of thousands of object reads -- blocks no poll.
	buildMu sync.Mutex

	// sweep and learned are the two sources merged into every published
	// index, guarded by buildMu. Keeping them apart is what lets a rebuild
	// replace the MIB half without discarding tables the OID learner
	// discovered, and lets a learner export republish without re-sweeping.
	sweep   *mibSweep
	learned *tableSet

	// friendlyNameCache maps device:entryOID:indexValue:columnName to the
	// value last seen for that status column, e.g.
	// "router1:.1.3.6.1.2.1.2.2.1:1:ifName" -> "GigabitEthernet0/0".
	friendlyNameCache map[string]string
	friendlyMu        sync.RWMutex
}

// statusColumnInfo contains information about status columns that can be used as friendly names
type statusColumnInfo struct {
	Name      string // Column name (e.g., "ifName", "ifDescr")
	OID       string // Column OID
	ColumnID  string // Last part of column OID (e.g., "2" for ifDescr)
	LabelName string // Lowercased name for label (e.g., "ifname", "ifdescr")

	// Meta is what labelValue needs to render the column's values: the
	// declared type, the DISPLAY-HINT and the enumeration. It is built once
	// when the index is built, so the poll path allocates nothing to format.
	Meta *mib.OIDMetadata
}

// labelValue renders an SNMP result as the Prometheus label value for this
// status column, and reports whether there was anything to render.
//
// Octet strings go through mib.FormatValue, the one formatter the CLI and the
// web UI also use, so a binary column such as cbgpPeer2LastError becomes
// readable hex instead of the raw bytes that used to reach the label. Numeric
// values keep the bare enumeration label, with no "(n)" suffix, because that
// text is part of an existing series' identity.
//
// A column with no metadata still renders, as mib.FormatValue does for a nil
// meta: a label path must not be the thing that fails a poll.
func (c statusColumnInfo) labelValue(result *snmp.Result) (string, bool) {
	if octets, err := result.GetStringValue(); err == nil {
		if octets == "" {
			return "", false
		}
		return mib.FormatValue(c.Meta, octets), true
	}

	number, err := result.GetInt64Value()
	if err != nil {
		return "", false
	}

	var enumValues map[int64]string
	if c.Meta != nil {
		enumValues = c.Meta.EnumValues
	}
	return mib.EnumLabel(enumValues, number), true
}

// TableIndexInfo contains metadata about a table's index structure
type TableIndexInfo struct {
	TableOID   string
	EntryOID   string
	IndexNames []string // Names of index columns (e.g., ["ifIndex"])
	IndexOIDs  []string // OIDs of index columns
	IndexTypes []string // Types of index columns (Integer32, IpAddress, etc.)

	// IndexColumns is the MIB-declared description of each index column:
	// resolved type, base type, IMPLIED flag and fixed width. It is empty
	// for tables discovered by the OID learner and for metadata stored
	// before index column types were recorded, in which case the decoder
	// falls back to IndexTypes and the index name heuristic.
	IndexColumns []mib.IndexColumn
}

// NewTableIndexCache creates a table index cache over the MIB metadata the
// store holds now. Modules loaded afterwards are picked up by Refresh.
func NewTableIndexCache(store *storage.Storage) *TableIndexCache {
	cache := &TableIndexCache{
		storage:           store,
		friendlyNameCache: make(map[string]string),
	}

	if err := cache.Refresh(); err != nil {
		log.Printf("Warning: Failed to load table metadata: %v", err)
	}

	return cache
}

// index returns the published MIB-derived index, or the empty index when none
// has been published yet. It never returns nil and never blocks.
func (c *TableIndexCache) index() *mibIndex {
	if c == nil {
		return emptyMIBIndex
	}
	if published := c.published.Load(); published != nil {
		return published
	}
	return emptyMIBIndex
}

// objectNames returns the index that resolves an instance OID to the metric
// name of the MIB object it instantiates. It returns nil when no MIB
// metadata has been loaded, which resolves nothing rather than failing.
func (c *TableIndexCache) objectNames() *objectNameIndex {
	return c.index().names
}

// Refresh rebuilds the MIB-derived index from what the MIB store holds now and
// publishes it.
//
// This is what makes a module loaded after the cache was constructed usable.
// Without it the index describes only the modules that happened to be in the
// store at construction, every object of every module loaded since resolves to
// no MIB definition, and its samples fall back to raw-OID metric names with no
// index labels -- correct, lossless, and unusable.
//
// It is safe to call while devices are polling: the sweep builds a whole new
// index off to the side and installs it with one pointer store.
func (c *TableIndexCache) Refresh() error {
	c.buildMu.Lock()
	defer c.buildMu.Unlock()

	sweep, err := c.sweepStore()
	if err != nil {
		mibIndexRebuilds.WithLabelValues(rebuildOutcomeError).Inc()
		return err
	}

	c.sweep = sweep
	c.publishLocked()

	mibIndexRebuilds.WithLabelValues(rebuildOutcomeSuccess).Inc()
	mibIndexLastRebuildTimestamp.SetToCurrentTime()

	log.Printf("[TABLE INDEX CACHE] Indexed %d MIB objects and %d tables with index information",
		c.index().objectCount(), len(c.index().tables))

	return nil
}

// publishLocked merges the current sweep and learned tables into a new index
// and publishes it. The caller must hold buildMu.
func (c *TableIndexCache) publishLocked() {
	index := newMIBIndex(c.sweep, c.learned)
	c.published.Store(index)

	mibIndexObjects.Set(float64(index.objectCount()))
	mibIndexTables.Set(float64(len(index.tables)))
}

// addLearnedTables merges tables the OID learner discovered into the index and
// returns how many were new.
//
// Learned tables are kept separately from the MIB sweep so that a later Refresh
// does not discard them, and a table the MIBs already describe is never
// displaced by a learned one: the MIB definition carries index column types the
// learner can only infer from column names.
func (c *TableIndexCache) addLearnedTables(discovered []learnedTable) int {
	if c == nil || len(discovered) == 0 {
		return 0
	}

	c.buildMu.Lock()
	defer c.buildMu.Unlock()

	added := 0
	for _, table := range discovered {
		if c.sweep != nil && c.sweep.tables.has(table.info.EntryOID) {
			continue
		}
		if c.learned == nil {
			c.learned = newTableSet()
		}
		if c.learned.add(table.info, table.columns) {
			added++
		}
	}

	if added > 0 {
		c.publishLocked()
	}

	return added
}

// learnedTable is one table the OID learner discovered, ready to be merged.
type learnedTable struct {
	info    *TableIndexInfo
	columns []string
}

// sweepStore reads every object in the MIB store and derives the metric name
// index, the table index structures and the status columns from that one pass.
// It touches no cache state, so it can run while polls are in flight.
func (c *TableIndexCache) sweepStore() (*mibSweep, error) {
	if c.storage == nil {
		return nil, &StorageUnavailableError{Operation: "load MIB table metadata"}
	}

	allOIDs, err := c.storage.ListOIDs("")
	if err != nil {
		return nil, &OIDListError{Cause: err}
	}

	sweep := &mibSweep{
		// The metric name index is built from the same sweep that finds the
		// tables: a second full scan of the store would double the cost for
		// metadata already in hand.
		names:         newObjectNameIndex(allOIDs),
		tables:        newTableSet(),
		statusColumns: make(map[string][]statusColumnInfo),
	}

	// Group the objects by parent once. Finding a table's columns by scanning
	// every object, as this did, costs objects x tables per sweep -- on the
	// order of thirty million comparisons on an install with the vendor MIBs
	// loaded, paid twice over for the status columns. That was tolerable when
	// the sweep ran once at startup; it runs on every module load now.
	childrenByParent := make(map[string][]*mib.OIDMetadata, len(allOIDs))
	for _, oid := range allOIDs {
		if oid.Parent != "" {
			childrenByParent[oid.Parent] = append(childrenByParent[oid.Parent], oid)
		}
	}

	for _, oid := range allOIDs {
		if !oid.IsTableEntry || len(oid.Index) == 0 {
			continue
		}

		tableInfo := c.tableInfoFor(oid)
		if tableInfo == nil {
			continue
		}

		children := childrenByParent[oid.OID]

		columnOIDs := make([]string, 0, len(children))
		for _, child := range children {
			if child.IsColumn {
				columnOIDs = append(columnOIDs, child.OID)
			}
		}

		sweep.tables.add(tableInfo, columnOIDs)

		if statusCols := statusColumnsOf(children, tableInfo.EntryOID); len(statusCols) > 0 {
			sweep.statusColumns[tableInfo.EntryOID] = statusCols
		}
	}

	return sweep, nil
}

// tableInfoFor derives a table's index structure from its conceptual row
// object, or returns nil if the row's own OID cannot be established.
func (c *TableIndexCache) tableInfoFor(oid *mib.OIDMetadata) *TableIndexInfo {
	// Some table entries -- BGP's among them -- are stored without their OID,
	// which then has to be recovered by name.
	entryOID := oid.OID
	if entryOID == "" {
		entryOIDInfo, err := c.storage.GetOIDByName(oid.Module, oid.Name)
		if err != nil {
			log.Printf("Warning: Could not find OID for table entry %s in module %s: %v",
				oid.Name, oid.Module, err)
			return nil
		}
		entryOID = entryOIDInfo.OID
	}

	tableInfo := &TableIndexInfo{
		TableOID:     oid.Parent, // Parent of entry is the table
		EntryOID:     normalizeOID(entryOID),
		IndexNames:   oid.Index,
		IndexOIDs:    make([]string, len(oid.Index)),
		IndexTypes:   make([]string, len(oid.Index)),
		IndexColumns: oid.IndexColumns,
	}

	if len(oid.IndexColumns) == len(oid.Index) {
		// The MIB parser already resolved every index column, including the
		// ones defined in another module, which a name lookup scoped to this
		// module would miss.
		for i, column := range oid.IndexColumns {
			tableInfo.IndexOIDs[i] = column.OID
			tableInfo.IndexTypes[i] = column.Type
		}
		return tableInfo
	}

	// Metadata stored before index columns were recorded: look each index
	// column up by name.
	for i, indexName := range oid.Index {
		indexOID, err := c.storage.GetOIDByName(oid.Module, indexName)
		if err != nil {
			log.Printf("Warning: Could not find index column %s in module %s: %v",
				indexName, oid.Module, err)
			continue
		}
		tableInfo.IndexOIDs[i] = indexOID.OID
		tableInfo.IndexTypes[i] = indexOID.Type
	}

	return tableInfo
}

// statusColumnsOf returns those of a table's columns whose values can serve as
// friendly-name labels, keyed for storage under normalizedEntry.
func statusColumnsOf(columns []*mib.OIDMetadata, normalizedEntry string) []statusColumnInfo {
	var statusCols []statusColumnInfo

	for _, oid := range columns {
		if !oid.IsColumn || oid.ObjectClass != mib.ObjectClassStatusColumn {
			continue
		}

		columnID := extractColumnID(oid.OID, normalizedEntry)
		if columnID == "" {
			continue
		}

		statusCols = append(statusCols, statusColumnInfo{
			Name:      oid.Name,
			OID:       oid.OID,
			ColumnID:  columnID,
			LabelName: strings.ToLower(oid.Name),
			Meta: &mib.OIDMetadata{
				Name:        oid.Name,
				Type:        oid.Type,
				DisplayHint: oid.DisplayHint,
				EnumValues:  oid.EnumValues,
				Units:       oid.Units,
			},
		})
	}

	return statusCols
}

// extractColumnID extracts the column ID from a column OID
// For example: columnOID=".1.3.6.1.2.1.2.2.1.2", entryOID=".1.3.6.1.2.1.2.2.1" -> "2"
func extractColumnID(columnOID, entryOID string) string {
	normalizedCol := normalizeOID(columnOID)
	normalizedEntry := normalizeOID(entryOID)

	if !strings.HasPrefix(normalizedCol, normalizedEntry+".") {
		return ""
	}

	suffix := strings.TrimPrefix(normalizedCol, normalizedEntry+".")
	parts := strings.Split(suffix, ".")
	if len(parts) > 0 {
		return parts[0]
	}
	return ""
}

// CacheFriendlyNames processes SNMP results and caches friendly name values from status columns
func (c *TableIndexCache) CacheFriendlyNames(deviceName string, results []*snmp.Result) {
	index := c.index()

	c.friendlyMu.Lock()
	defer c.friendlyMu.Unlock()

	for _, result := range results {
		// Find which table this OID belongs to
		tableInfo := index.tableFor(result.OID)
		if tableInfo == nil {
			continue
		}

		// Check if this table has status columns
		statusCols := index.statusColumnsFor(tableInfo)
		if len(statusCols) == 0 {
			continue
		}

		// Extract the column ID and index from the OID
		normalizedOID := normalizeOID(result.OID)
		normalizedEntry := normalizeOID(tableInfo.EntryOID)

		if !strings.HasPrefix(normalizedOID, normalizedEntry+".") {
			continue
		}

		suffix := strings.TrimPrefix(normalizedOID, normalizedEntry+".")
		parts := strings.Split(suffix, ".")

		if len(parts) < 2 {
			continue
		}

		columnID := parts[0]
		indexParts := parts[1:]

		// Check if this column ID matches any status column
		for _, statusCol := range statusCols {
			if statusCol.ColumnID != columnID {
				continue
			}

			friendlyValue, rendered := statusCol.labelValue(result)
			if !rendered {
				break
			}

			// Build the index value string
			indexValue := strings.Join(indexParts, ".")

			// Create cache key: device:entryOID:indexValue:columnName
			cacheKey := fmt.Sprintf("%s:%s:%s:%s", deviceName, tableInfo.EntryOID, indexValue, statusCol.LabelName)

			// Cache the friendly name
			c.friendlyNameCache[cacheKey] = friendlyValue
			break
		}
	}
}

// ExtractIndexLabels extracts index values from an OID and returns them as Prometheus labels
// Also adds friendly name labels if available in cache
// For example: OID ".1.3.6.1.2.1.2.2.1.10.5" with index ["ifIndex"] returns {"ifindex": "5"}
// If friendly names are cached, also returns {"ifname": "GigabitEthernet0/0", "ifdescr": "..."}
// For BGP: OID "1.3.6.1.4.1.9.9.187.1.2.8.1.1.1.4.203.0.113.218.1.1" with indexes
// ["cbgpPeer2Type", "cbgpPeer2RemoteAddr", "cbgpPeer2RemoteAddrType", "cbgpPeer2AddrFamilyAfi", "cbgpPeer2AddrFamilySafi"]
// returns labels for peer address, AFI, SAFI, etc.
func (c *TableIndexCache) ExtractIndexLabels(deviceName, oid string) map[string]string {
	return c.extractIndexLabels(c.index(), deviceName, oid)
}

// extractIndexLabels is ExtractIndexLabels against a caller-supplied view of
// the MIBs, so that a whole poll can be named and labeled from one index even
// if a module is loaded while it runs.
func (c *TableIndexCache) extractIndexLabels(index *mibIndex, deviceName, oid string) map[string]string {
	// Find which table this OID belongs to
	tableInfo := index.tableFor(oid)
	if tableInfo == nil {
		return nil
	}

	// Extract the index suffix from the OID
	// The suffix comes after the column OID
	// For example: column OID is ".1.3.6.1.2.1.2.2.1.10" and full OID is ".1.3.6.1.2.1.2.2.1.10.5"
	// The suffix is ".5"

	// Find where the entry OID ends in the full OID
	normalizedOID := normalizeOID(oid)
	normalizedEntry := normalizeOID(tableInfo.EntryOID)

	if !strings.HasPrefix(normalizedOID, normalizedEntry+".") {
		// OID doesn't match expected pattern
		return nil
	}

	// Extract the part after entryOID.columnID
	// The OID structure is: entryOID.columnID.index1.index2...
	// We need to skip entryOID and the first number (columnID)
	suffix := strings.TrimPrefix(normalizedOID, normalizedEntry+".")
	parts := strings.Split(suffix, ".")

	if len(parts) < 1 {
		return nil
	}

	// First part is the column ID, rest are index values
	indexParts := parts[1:]

	// Parse index values based on index types
	labels := buildIndexLabels(tableInfo, indexParts)

	// Add friendly name labels if available
	// Build the index value string (reconstruct from parts used)
	if len(indexParts) > 0 {
		indexValue := strings.Join(indexParts, ".")

		// Check if we have status columns for this table
		if statusCols := index.statusColumnsFor(tableInfo); len(statusCols) > 0 {
			c.friendlyMu.RLock()
			for _, statusCol := range statusCols {
				// Build cache key
				cacheKey := fmt.Sprintf("%s:%s:%s:%s", deviceName, tableInfo.EntryOID, indexValue, statusCol.LabelName)

				// Look up friendly name
				if friendlyName, found := c.friendlyNameCache[cacheKey]; found {
					labels[statusCol.LabelName] = friendlyName
				}
			}
			c.friendlyMu.RUnlock()
		}
	}

	return labels
}

// buildIndexLabels splits an OID's index suffix into one Prometheus label per
// index column, named after the lowercased column name.
//
// Each column is decoded from its MIB-declared type when the table carries
// index column metadata. Columns are consumed left to right, so a column that
// reports the wrong width misaligns every column after it.
func buildIndexLabels(tableInfo *TableIndexInfo, indexParts []string) map[string]string {
	labels := make(map[string]string, len(tableInfo.IndexNames))
	pos := 0

	for i, indexName := range tableInfo.IndexNames {
		if pos >= len(indexParts) {
			break
		}

		var value string
		var consumed int

		if i < len(tableInfo.IndexColumns) && tableInfo.IndexColumns[i].BaseType != "" {
			value, consumed = decodeIndexColumn(indexParts[pos:], tableInfo.IndexColumns[i])
		} else {
			// Fallback for tables without index column metadata: those
			// discovered by the OID learner, and OIDs stored before the
			// MIB parser recorded index types. The declared type is used
			// when the index column was found by name, and the name
			// heuristic when even that failed.
			indexType := ""
			if i < len(tableInfo.IndexTypes) {
				indexType = tableInfo.IndexTypes[i]
			}
			if indexType == "" {
				indexType = inferTypeFromName(indexName)
			}
			value, consumed = parseIndexValue(indexParts[pos:], indexType)
		}

		if value != "" {
			labels[strings.ToLower(indexName)] = value
		}
		if consumed <= 0 {
			break
		}
		pos += consumed
	}

	return labels
}

// decodeIndexColumn decodes one index value from the OID sub-identifiers that
// start at parts[0], using the column's MIB-declared type. It returns the
// rendered value and the number of sub-identifiers consumed. Truncated input
// yields an empty value and consumes what is left, since nothing after a
// truncated column can be located.
func decodeIndexColumn(parts []string, column mib.IndexColumn) (string, int) {
	if len(parts) == 0 {
		return "", 0
	}

	// Address types reduce to OCTET STRING but have their own presentation
	// and, for InetAddress, their own leading address-family sub-identifier.
	switch column.Type {
	case "IpAddress", "InetAddress", "InetAddressIPv4", "InetAddressIPv6":
		return parseIndexValue(parts, column.Type)
	}

	switch column.BaseType {
	case "Integer32", "Unsigned32", "Integer64", "Unsigned64", "Enum":
		return parts[0], 1

	case "ObjectIdentifier":
		if column.Implied {
			return strings.Join(parts, "."), len(parts)
		}
		length, ok := subIDLength(parts)
		if !ok {
			return "", len(parts)
		}
		return strings.Join(parts[1:1+length], "."), length + 1

	case "OctetString", "Bits":
		if column.FixedLength > 0 {
			if len(parts) < column.FixedLength {
				return "", len(parts)
			}
			return renderOctets(parts[:column.FixedLength]), column.FixedLength
		}
		if column.Implied {
			return renderOctets(parts), len(parts)
		}
		length, ok := subIDLength(parts)
		if !ok {
			return "", len(parts)
		}
		return renderOctets(parts[1 : 1+length]), length + 1
	}

	// No usable base type: the defining module was not on the MIB search
	// path when the OID was parsed. Fall back to the name heuristic.
	indexType := column.Type
	if indexType == "" {
		indexType = inferTypeFromName(column.Name)
	}
	return parseIndexValue(parts, indexType)
}

// subIDLength reads the length prefix of a variable-length index and reports
// whether the sub-identifiers it announces are actually present.
func subIDLength(parts []string) (int, bool) {
	length, err := strconv.Atoi(parts[0])
	if err != nil || length < 0 || len(parts) < length+1 {
		return 0, false
	}
	return length, true
}

// renderOctets formats the octets of a string index. Text indexes such as
// interface or partition names render as themselves; binary ones such as MAC
// addresses render as colon-separated hex, which is how operators read them.
// The choice is mib.FormatOctets', so an index and a value made of the same
// octets are shown the same way.
//
// Sub-identifiers that are not octets are left as they arrived so a malformed
// index is visible rather than silently mangled.
func renderOctets(parts []string) string {
	octets := make([]byte, len(parts))

	for i, part := range parts {
		value, err := strconv.Atoi(part)
		if err != nil || value < 0 || value > 255 {
			return strings.Join(parts, ".")
		}
		octets[i] = byte(value)
	}

	return mib.FormatOctets(octets)
}

// inferTypeFromName infers the SNMP type from the index name when MIB type is unavailable.
//
// This is a guess, kept only as a fallback for tables with no index column
// metadata: OIDs stored before the parser recorded index types, and tables
// discovered by the OID learner. Anything with metadata is decoded from its
// declared type by decodeIndexColumn instead.
func inferTypeFromName(indexName string) string {
	lowerName := strings.ToLower(indexName)

	// IP address patterns
	if strings.Contains(lowerName, "addr") || strings.Contains(lowerName, "ipaddress") {
		return "InetAddress"
	}

	// BGP peer fields are typically InetAddress
	// e.g., cbgpRoutePeer, cbgpPeer2RemoteAddr, bgpPeerRemoteAddr
	if strings.Contains(lowerName, "peer") && !strings.Contains(lowerName, "type") {
		return "InetAddress"
	}

	// Common type indicators
	if strings.Contains(lowerName, "type") && !strings.Contains(lowerName, "subtype") {
		return "Integer32"
	}

	// AFI/SAFI are integers
	if strings.Contains(lowerName, "afi") || strings.Contains(lowerName, "safi") {
		return "Integer32"
	}

	// Index fields are typically integers
	if strings.HasSuffix(lowerName, "index") || strings.HasSuffix(lowerName, "id") {
		return "Integer32"
	}

	// Default to integer for unknown
	return "Integer32"
}

// parseIndexValue parses an index value from OID parts based on its type
// Returns the parsed value and the number of parts consumed
func parseIndexValue(parts []string, indexType string) (string, int) {
	if len(parts) == 0 {
		return "", 0
	}

	switch indexType {
	case "Integer32", "Integer", "Gauge32", "Counter32", "Counter64", "TimeTicks", "Unsigned32":
		// Simple numeric index
		return parts[0], 1

	case "IpAddress":
		// IPv4 address - 4 octets
		if len(parts) < 4 {
			return "", len(parts)
		}
		return fmt.Sprintf("%s.%s.%s.%s", parts[0], parts[1], parts[2], parts[3]), 4

	case "InetAddress", "InetAddressIPv4", "InetAddressIPv6":
		// InetAddress can appear in two formats:
		// 1. Standalone: type.length.octets (e.g., 1.4.198.51.100.101)
		// 2. After type index: length.octets (e.g., in CISCO-BGP4-MIB where type is separate index)
		//
		// We detect which format by checking if first value is 1 or 2 (types) followed by 4 or 16 (lengths)
		// vs just starting with 4 or 16 (length only)
		if len(parts) < 1 {
			return "", 0
		}

		firstVal, err := strconv.Atoi(parts[0])
		if err != nil {
			return parts[0], 1
		}

		// Check if this looks like length-only format (starts with 4 or 16)
		if firstVal == 4 || firstVal == 16 {
			// Format: length.octets
			length := firstVal
			if len(parts) < length+1 {
				return "", len(parts)
			}

			switch length {
			case 4:
				// IPv4
				result := fmt.Sprintf("%s.%s.%s.%s", parts[1], parts[2], parts[3], parts[4])
				return result, 5
			case 16:
				// IPv6
				octets := make([]string, 16)
				for i := 0; i < 16; i++ {
					octets[i] = parts[1+i]
				}
				result := formatIPv6(octets)
				return result, 17
			}
		}

		// Otherwise, assume type.length.octets format
		if len(parts) < 2 {
			return "", len(parts)
		}

		addrType := firstVal
		length, err := strconv.Atoi(parts[1])
		if err != nil {
			return parts[0], 1
		}

		if addrType == 1 && length == 4 { // IPv4
			if len(parts) < 6 { // type + length + 4 octets
				return "", len(parts)
			}
			result := fmt.Sprintf("%s.%s.%s.%s", parts[2], parts[3], parts[4], parts[5])
			return result, 6
		} else if addrType == 2 && length == 16 { // IPv6
			if len(parts) < 18 { // type + length + 16 octets
				return "", len(parts)
			}
			// Convert 16 octets to IPv6 format
			octets := make([]string, 16)
			for i := 0; i < 16; i++ {
				octets[i] = parts[2+i]
			}
			result := formatIPv6(octets)
			return result, 18
		}

		// If we reach here, none of the format checks matched
		return parts[0], 1

	case "OCTET STRING", "OctetString", "DisplayString":
		// Variable length string
		// First part is the length
		if len(parts) < 1 {
			return "", 0
		}

		length, err := strconv.Atoi(parts[0])
		if err != nil || length == 0 {
			return parts[0], 1
		}

		if len(parts) < length+1 {
			return "", len(parts)
		}

		// Convert octets to string
		str := ""
		for i := 1; i <= length; i++ {
			val, err := strconv.Atoi(parts[i])
			if err == nil && val > 0 && val < 128 {
				str += string(rune(val))
			}
		}

		return str, length + 1

	default:
		// Unknown type - treat as simple numeric
		return parts[0], 1
	}
}

// formatIPv6 formats 16 octets as an IPv6 address
func formatIPv6(octets []string) string {
	// Convert octets to hex words
	words := make([]string, 8)
	for i := 0; i < 8; i++ {
		byte1, _ := strconv.Atoi(octets[i*2])
		byte2, _ := strconv.Atoi(octets[i*2+1])
		word := (byte1 << 8) | byte2
		words[i] = fmt.Sprintf("%x", word)
	}
	return strings.Join(words, ":")
}

// normalizeOID ensures OID has consistent format (with leading dot)
func normalizeOID(oid string) string {
	if !strings.HasPrefix(oid, ".") {
		return "." + oid
	}
	return oid
}

// ConvertStatusColumnsToInfoMetrics creates info metrics for status columns (both string and enum).
// These metrics have constant value 1.0 with the status value as a "value" label.
// Example: snmp_ifdescr_info{device="router1", ifindex="3", value="GigabitEthernet0/0"} 1
// Example: snmp_cbgppeer2state_info{device="router1", cbgppeer2remoteaddr="198.51.100.101", value="established"} 1
//
// extraLabels are the device's configured tags. They are attached here exactly
// as they are to ordinary readings: an info metric is what dashboards and
// federation join against, so it has to be selectable by the same tags as the
// series being joined to it.
func ConvertStatusColumnsToInfoMetrics(
	deviceName string,
	results []*snmp.Result,
	extraLabels map[string]string,
	indexCache *TableIndexCache,
	timestamp time.Time,
) []*Sample {
	if indexCache == nil {
		return nil
	}

	index := indexCache.index()

	infoSamples := make([]*Sample, 0)

	// Process each SNMP result
	for _, result := range results {
		// Find which table this OID belongs to
		tableInfo := index.tableFor(result.OID)
		if tableInfo == nil {
			continue
		}

		// Check if this table has status columns
		statusCols := index.statusColumnsFor(tableInfo)
		if len(statusCols) == 0 {
			continue
		}

		// Extract column ID and index from OID
		normalizedOID := normalizeOID(result.OID)
		normalizedEntry := normalizeOID(tableInfo.EntryOID)

		if !strings.HasPrefix(normalizedOID, normalizedEntry+".") {
			continue
		}

		suffix := strings.TrimPrefix(normalizedOID, normalizedEntry+".")
		parts := strings.Split(suffix, ".")

		if len(parts) < 2 {
			continue
		}

		columnID := parts[0]
		indexParts := parts[1:]

		// Check if this column ID matches any status column
		for _, statusCol := range statusCols {
			if statusCol.ColumnID != columnID {
				continue
			}

			valueLabel, rendered := statusCol.labelValue(result)
			if !rendered {
				break
			}

			// Build the info metric's structural labels first -- the row
			// this reading came from and the value it reports -- then let
			// the device's tags fill in around them.
			labels := newSampleLabels(deviceName)
			for name, value := range buildIndexLabels(tableInfo, indexParts) {
				labels[name] = value
			}
			labels[labelValue] = valueLabel
			mergeExtraLabels(deviceName, labels, extraLabels)

			// The same naming a text scalar gets, so both kinds of info
			// metric are found the same way.
			metricName := infoMetricName(statusCol.Name)

			infoSamples = append(infoSamples, &Sample{
				Name:      metricName,
				Labels:    labels,
				Value:     infoMetricValue,
				Timestamp: timestamp,
			})
			break
		}
	}

	return infoSamples
}

// ConvertSNMPResultsWithTableIndexes converts SNMP results to samples with generic table index labels
func ConvertSNMPResultsWithTableIndexes(
	deviceName string,
	results []*snmp.Result,
	extraLabels map[string]string,
	indexCache *TableIndexCache,
	oidLearner *OIDLearner,
) []*Sample {
	// First, cache friendly names from string results
	if indexCache != nil {
		indexCache.CacheFriendlyNames(deviceName, results)
	}

	// Read this poll's entity sensor metadata and entity names before any of
	// its readings are converted. Nothing about a sensor survives the poll it
	// arrived in (internal/metrics/entity_sensor_poll.go), so this is the
	// only thing that can interpret or name this poll's readings.
	pollSensors := observeEntitySensors(results)

	// Learn from OIDs if learner is provided
	if oidLearner != nil {
		oids := make([]string, len(results))
		for i, result := range results {
			oids[i] = result.OID
		}
		oidLearner.LearnFromResponses(oids)

		// Try to enrich learner with MIB metadata
		oidLearner.MergeWithMIBMetadata(indexCache)

		// Export learned tables back to index cache
		if exported := oidLearner.ExportToIndexCache(indexCache); exported > 0 {
			log.Printf("[OID LEARNER] Exported %d learned tables to index cache", exported)
		}
	}
	samples := make([]*Sample, 0, len(results))
	systemInfo := make(map[string]string) // Collect system text values

	// The MIB index is read once per conversion rather than once per sample:
	// it is immutable, so every sample of a poll is named and labeled against
	// the same view of the MIBs even if a module is loaded midway through.
	index := indexCache.index()
	nameIndex := index.names
	unresolvedNames := 0

	for _, result := range results {
		// Try numeric value first
		value, err := result.GetFloat64Value()
		if err != nil {
			// Not a number. It may be one of the system scalars that
			// snmp_system_info reports as labels, and otherwise it is a text
			// scalar in its own right -- a power supply or a fan saying how it
			// is, in words. Those used to be dropped here.
			recordSystemInfo(systemInfo, result)
			if sample := textScalarSample(deviceName, result, index, extraLabels); sample != nil {
				samples = append(samples, sample)
			}
			continue
		}

		// An entity sensor's value column is the one reading whose raw
		// number is not the measurement: it is a fixed-point integer in
		// some SI scale, and publishing it as read would report a
		// transceiver at -115 dBm. It is replaced here -- not
		// supplemented -- by the scaled sample, so the wrong number is
		// never exported under any name. A reading that cannot be scaled
		// is dropped and counted rather than published raw.
		if key, isSensor := entitySensorValueKey(result.OID); isSensor {
			scaled, err := sensorSample(deviceName, key, result, extraLabels, pollSensors)
			if err != nil {
				countSensorDrop(deviceName, err)
				continue
			}
			samples = append(samples, scaled)
			continue
		}

		labels := newInstanceLabels(deviceName, result.OID)

		// Extract table index labels if this is a table column
		if indexCache != nil {
			for k, v := range indexCache.extractIndexLabels(index, deviceName, result.OID) {
				labels[k] = v
			}
		}

		mergeExtraLabels(deviceName, labels, extraLabels)

		// Name the sample after the MIB object it instantiates. The row
		// index stays in the labels above; folding it into the name
		// would give Prometheus one metric name per row.
		metricName, resolved := resolveMetricName(nameIndex, result.OID)
		if !resolved {
			unresolvedNames++
		}

		samples = append(samples, &Sample{
			Name:      metricName,
			Labels:    labels,
			Value:     value,
			Timestamp: result.Timestamp,
		})
	}

	// Create system info metric if we have any system text values
	if len(systemInfo) > 0 {
		samples = append(samples, newSystemInfoSample(deviceName, systemInfo, extraLabels, pollTimestamp(results)))
	}

	// Surface OIDs that no loaded MIB describes. They keep a raw-OID
	// metric name so no data is lost, and this counter makes the gap
	// measurable instead of invisible.
	if unresolvedNames > 0 {
		metricNameUnresolvedSamples.WithLabelValues(deviceName).Add(float64(unresolvedNames))
	}

	// Generate info metrics for status columns
	if indexCache != nil {
		samples = append(samples, ConvertStatusColumnsToInfoMetrics(deviceName, results, extraLabels, indexCache, pollTimestamp(results))...)
	}

	return samples
}
