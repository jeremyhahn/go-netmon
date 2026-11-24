package api

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"

	"github.com/gorilla/mux"

	"github.com/jeremyhahn/go-netmon/internal/metrics"
	"github.com/jeremyhahn/go-netmon/internal/mib"
	mibstorage "github.com/jeremyhahn/go-netmon/internal/mib/storage"
	"github.com/jeremyhahn/go-netmon/internal/promname"
	webuistorage "github.com/jeremyhahn/go-netmon/internal/webui/storage"
)

// handleListStoredMIBs returns the stored MIB catalog in name order, narrowed by the query.
func (s *Server) handleListStoredMIBs(w http.ResponseWriter, r *http.Request) {
	// Refuse a malformed query before reading the catalog.
	query, err := parseMIBQuery(r.URL.Query())
	if err != nil {
		respondQueryError(w, err)
		return
	}

	mibs, err := s.storage.ListMIBs(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Filter by available data; device narrows it to one device's series.
	filterByData := r.URL.Query().Get("filter") == "available"

	if filterByData {
		filtered, filterErr := s.filterMIBsWithData(
			r.Context(), mibs, r.URL.Query().Get("device"))
		if filterErr != nil {
			http.Error(w, filterErr.Error(), http.StatusInternalServerError)
			return
		}
		mibs = filtered
	}

	selected, page, err := selectPage(mibs, query.listQuery, query.keep())
	if err != nil {
		respondQueryError(w, err)
		return
	}
	// The same path the detail view shows: the file the module is read from
	// now, or none, never the path recorded when it was imported. Resolved
	// for the page only, since each one is a stat.
	for i := range selected {
		selected[i].FilePath = s.mibSourcePath(selected[i].Name, selected[i].FilePath)
	}

	body := map[string]interface{}{"mibs": selected}
	if query.narrowed {
		body["page"] = page
	}
	respondJSON(w, http.StatusOK, body)
}

// handleGetStoredMIB retrieves a stored MIB by ID with its objects, in store order, narrowed by the query.
func (s *Server) handleGetStoredMIB(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id, err := strconv.ParseInt(vars["id"], 10, 64)
	if err != nil {
		http.Error(w, "invalid MIB ID", http.StatusBadRequest)
		return
	}

	// Refuse a malformed query before reading the record or the module.
	query, err := parseObjectQuery(r.URL.Query())
	if err != nil {
		respondQueryError(w, err)
		return
	}

	record, err := s.storage.GetMIB(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	// Show the module's current file, not the path recorded at import.
	record.FilePath = s.mibSourcePath(record.Name, record.FilePath)

	// Add the module's objects from the loader's metadata store. The lookup is
	// scoped to the module: reading the whole store to answer a question about
	// one MIB deserializes tens of thousands of records per request.
	if moduleStore, storeErr := s.mibStore(); storeErr == nil {
		objects, objectsErr := moduleObjects(moduleStore, record.Name)
		if objectsErr == nil {
			selected, page, pageErr := selectPage(objects, query.listQuery, query.keep())
			if pageErr != nil {
				respondQueryError(w, pageErr)
				return
			}

			body := map[string]interface{}{
				"id":           record.ID,
				"name":         record.Name,
				"file_path":    record.FilePath,
				"description":  record.Description,
				"object_count": record.ObjectCount,
				"imported_at":  record.ImportedAt,
				"objects":      wireObjects(selected),
			}
			if query.narrowed {
				body["page"] = page
			}
			respondJSON(w, http.StatusOK, body)
			return
		}
		log.Printf("WARNING: could not read objects for MIB %s: %v", record.Name, objectsErr)
	}

	respondJSON(w, http.StatusOK, record)
}

// handleDeleteStoredMIB deletes a stored MIB
func (s *Server) handleDeleteStoredMIB(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id, err := strconv.ParseInt(vars["id"], 10, 64)
	if err != nil {
		http.Error(w, "invalid MIB ID", http.StatusBadRequest)
		return
	}

	if err := s.storage.DeleteMIB(r.Context(), id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// TableMetadata represents table structure information
type TableMetadata struct {
	TableOID    string      `json:"table_oid"`
	TableName   string      `json:"table_name"`
	EntryOID    string      `json:"entry_oid"`
	EntryName   string      `json:"entry_name"`
	IndexColumn string      `json:"index_column,omitempty"`
	Columns     []MIBObject `json:"columns"`
}

// handleGetMIBTables extracts table metadata from a MIB
func (s *Server) handleGetMIBTables(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id, err := strconv.ParseInt(vars["id"], 10, 64)
	if err != nil {
		http.Error(w, "invalid MIB ID", http.StatusBadRequest)
		return
	}

	record, err := s.storage.GetMIB(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	// Get this MIB's OIDs
	loaderStorage, err := s.mibStore()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	moduleOIDs, err := loaderStorage.ListModuleOIDs(record.Name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Detect tables using OID pattern analysis
	tables := detectTables(moduleOIDs, record.Name)

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"tables": tables,
	})
}

// moduleObjects returns a module's stored objects in stable store order.
func moduleObjects(store *mibstorage.Storage, module string) ([]*mib.OIDMetadata, error) {
	return store.ListModuleOIDs(module)
}

// wireObjects converts stored OID metadata to the API wire format.
func wireObjects(oids []*mib.OIDMetadata) []MIBObject {
	objects := make([]MIBObject, 0, len(oids))
	for _, oid := range oids {
		objects = append(objects, newMIBObject(oid))
	}
	return objects
}

// detectTables identifies a module's SNMP tables from OID patterns, ignoring other modules' objects.
func detectTables(allOIDs []*mib.OIDMetadata, module string) []TableMetadata {
	oids := make(map[string]*mib.OIDMetadata, len(allOIDs))
	for _, oid := range allOIDs {
		if oid.Module == module {
			oids[oid.OID] = oid
		}
	}

	tables := make(map[string]*TableMetadata)

	// SNMP table pattern: X.Y.Z (table) -> X.Y.Z.1 (entry) -> X.Y.Z.1.N (columns)
	for oidStr, oidInfo := range oids {
		parts := splitOID(oidStr)
		if len(parts) < 10 {
			continue // Too short to be a table column
		}

		// Check if this looks like a table column (parent ends in .1)
		if len(parts) >= 2 && parts[len(parts)-2] == "1" {
			// This is potentially a column
			entryOID := joinOID(parts[:len(parts)-1])
			tableOID := joinOID(parts[:len(parts)-2])

			// Find or create table entry
			if _, exists := tables[tableOID]; !exists {
				// Find table and entry names
				tableName := ""
				entryName := ""
				if tableInfo, ok := oids[tableOID]; ok {
					tableName = tableInfo.Name
				}
				if entryInfo, ok := oids[entryOID]; ok {
					entryName = entryInfo.Name
				}

				tables[tableOID] = &TableMetadata{
					TableOID:  tableOID,
					TableName: tableName,
					EntryOID:  entryOID,
					EntryName: entryName,
					Columns:   make([]MIBObject, 0),
				}
			}

			// Add column if it's pollable
			if oidInfo.Pollable {
				tables[tableOID].Columns = append(tables[tableOID].Columns, newMIBObject(oidInfo))
			}

			// Track index column (usually column 1)
			if parts[len(parts)-1] == "1" && tables[tableOID].IndexColumn == "" {
				tables[tableOID].IndexColumn = oidInfo.Name
			}
		}
	}

	// Convert map to slice and filter out empty tables
	result := make([]TableMetadata, 0, len(tables))
	for _, table := range tables {
		if len(table.Columns) > 0 {
			result = append(result, *table)
		}
	}

	return result
}

// splitOID splits an OID string into parts
func splitOID(oid string) []string {
	var parts []string
	current := ""
	for _, ch := range oid {
		if ch == '.' {
			if current != "" {
				parts = append(parts, current)
				current = ""
			}
		} else {
			current += string(ch)
		}
	}
	if current != "" {
		parts = append(parts, current)
	}
	return parts
}

// joinOID joins OID parts with dots
func joinOID(parts []string) string {
	result := ""
	for i, part := range parts {
		if i > 0 {
			result += "."
		}
		result += part
	}
	return result
}

// handleGetMIBDashboard generates one MIB's dashboard for one device with the whole-device builder.
func (s *Server) handleGetMIBDashboard(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id, err := strconv.ParseInt(vars["id"], 10, 64)
	if err != nil {
		http.Error(w, "invalid MIB ID", http.StatusBadRequest)
		return
	}

	record, err := s.storage.GetMIB(r.Context(), id)
	if err != nil {
		http.Error(w, "MIB not found", http.StatusNotFound)
		return
	}

	// A dashboard needs a device: panels are the metrics it reports.
	name := r.URL.Query().Get("device")
	if name == "" {
		http.Error(w, "device is required", http.StatusBadRequest)
		return
	}

	device, err := s.storage.GetDeviceByName(r.Context(), name)
	if err != nil {
		http.Error(w, "device not found", http.StatusNotFound)
		return
	}

	dash := s.generateDashboard(r.Context(), device, []string{record.Name}, record.Name, scopeModule)
	log.Printf("INFO: dashboard for %s on %s: %d panels", record.Name, name, len(dash.Panels))

	respondJSON(w, http.StatusOK, dash)
}

// filterMIBsWithData keeps modules with a learned table or a scalar metric in Prometheus, reading OIDs and metric names once.
func (s *Server) filterMIBsWithData(
	ctx context.Context, mibs []webuistorage.MIB, device string,
) ([]webuistorage.MIB, error) {
	loaderStorage, err := s.mibStore()
	if err != nil {
		return nil, err
	}

	allOIDs, err := loaderStorage.ListOIDs("")
	if err != nil {
		return nil, &MIBStorageError{Reason: "list OIDs: " + err.Error()}
	}

	// One pass over the store, grouped by module.
	oidsByModule := make(map[string][]*mib.OIDMetadata, len(mibs))
	for _, oid := range allOIDs {
		oidsByModule[oid.Module] = append(oidsByModule[oid.Module], oid)
	}

	// An empty device asks the fleet-wide question, which is what the module
	// catalog wants. A named one asks only about that device's series, and then
	// the process-wide learned tables are not evidence about it.
	var (
		matchers      map[string]string
		learnedTables map[string]*metrics.LearnedTableInfo
	)
	if device == "" {
		learnedTables = s.learnedTables(ctx)
	} else {
		matchers = map[string]string{promname.DeviceLabel: device}
	}
	metricNames := s.prometheusMetricNames(ctx, matchers)

	filtered := make([]webuistorage.MIB, 0, len(mibs))
	for _, m := range mibs {
		if moduleHasData(oidsByModule[m.Name], learnedTables, metricNames) {
			filtered = append(filtered, m)
		}
	}
	return filtered, nil
}

// prometheusMetricNames returns metric names Prometheus currently receives, optionally narrowed by matchers; empty means unknown.
func (s *Server) prometheusMetricNames(ctx context.Context, matchers map[string]string) map[string]bool {
	// Always restrict to SNMP families, excluding the poller's own instrumentation.
	parts := append(matcherParts(matchers),
		promname.NameLabel+`=~"`+promname.Prefix+`.*"`)
	selector := "{" + joinStrings(parts, ",") + "}"

	values, err := s.promClient.Query(ctx, fmt.Sprintf(
		"count by (%s) (last_over_time(%s[%s]))",
		promname.NameLabel, selector, MetricPresenceWindow))
	if err != nil {
		log.Printf("WARNING: could not list the metric names Prometheus holds: %v", err)
		return nil
	}

	names := make(map[string]bool, len(values))
	for _, v := range values {
		if name := v.Metric[promname.NameLabel]; name != "" {
			names[name] = true
		}
	}
	return names
}

// MetricPresenceWindow is how far back "is this metric arriving?" looks. It has
// to outlast the slowest poll interval, or a device polled every five minutes
// would read as having stopped between polls.
const MetricPresenceWindow = "30m"

// matcherParts renders label matchers as PromQL selector terms.
func matcherParts(matchers map[string]string) []string {
	parts := make([]string, 0, len(matchers))
	for k, v := range matchers {
		parts = append(parts, fmt.Sprintf("%s=%q", k, v))
	}
	sort.Strings(parts)
	return parts
}

// prometheusInstanceOIDs returns the set of instance OIDs (oid label) Prometheus holds.
func (s *Server) prometheusInstanceOIDs(ctx context.Context, matchers map[string]string) map[string]bool {
	return s.prometheusLabelValues(ctx, promname.OIDLabel, matchers)
}

// prometheusLabelValues returns the distinct values of one label, as a set.
func (s *Server) prometheusLabelValues(ctx context.Context, label string, matchers map[string]string) map[string]bool {
	if s.promClient == nil {
		return nil
	}

	names, err := s.promClient.QueryLabelValues(ctx, label, matchers)
	if err != nil {
		log.Printf("WARNING: could not list Prometheus values of label %q: %v", label, err)
		return nil
	}

	set := make(map[string]bool, len(names))
	for _, name := range names {
		set[name] = true
	}
	return set
}

// moduleHasData reports whether a module's objects have collected data: metric names first, learned tables only when not device-scoped.
func moduleHasData(
	oids []*mib.OIDMetadata,
	learnedTables map[string]*metrics.LearnedTableInfo,
	metricNames map[string]bool,
) bool {
	if len(metricNames) > 0 {
		for _, oid := range oids {
			if !oid.Pollable {
				continue
			}
			if metricNames[promname.MetricName(oid.Name)] ||
				metricNames[promname.InfoMetricName(oid.Name)] {
				return true
			}
		}
	}

	for _, oid := range oids {
		if !oid.IsTableEntry || len(learnedTables) == 0 {
			continue
		}
		// A table the poller has walked at least once.
		entryOID := oid.EntryOID
		if entryOID == "" {
			entryOID = oid.OID
		}
		if entryOID != "" && learnedTables[entryOID] != nil {
			return true
		}
	}
	return false
}
