package api

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"

	"github.com/gorilla/mux"

	"github.com/jeremyhahn/go-netmon/internal/mib"
	mibstorage "github.com/jeremyhahn/go-netmon/internal/mib/storage"
	"github.com/jeremyhahn/go-netmon/internal/promname"
	"github.com/jeremyhahn/go-netmon/internal/webui/dashboard"
	webuistorage "github.com/jeremyhahn/go-netmon/internal/webui/storage"
)

// handleListStoredMIBs returns the stored MIB catalog, narrowed by the query.
//
// The catalog is served in name order, which storage.ListMIBs already
// guarantees, and the narrowing is applied to that order rather than replacing
// it: page two has to be the second fifty modules of the same list page one was
// the first fifty of.
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

	// Check if we should filter by available data. "device" narrows the
	// question to one device's own series: without it the answer is every
	// module holding data for anything on the fleet, which is not what a
	// caller asking on behalf of one device wants to know.
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

	body := map[string]interface{}{"mibs": selected}
	if query.narrowed {
		body["page"] = page
	}
	respondJSON(w, http.StatusOK, body)
}

// handleGetStoredMIB retrieves a stored MIB by ID, with its objects narrowed by
// the query.
//
// Without a query it answers exactly as it always has: every object the module
// defines, in the order the metadata store holds them. That order is a key scan
// and so is the same on every request, which is what makes the query's offset
// and limit mean anything.
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

// moduleObjects returns a module's stored objects, in the order the metadata
// store holds them.
//
// That order is a key scan of the module's OID index, so it is the same on
// every request. Paging depends on that: page two is only the continuation of
// page one if the list was not reassembled in between.
func moduleObjects(store *mibstorage.Storage, module string) ([]*mib.OIDMetadata, error) {
	return store.ListModuleOIDs(module)
}

// wireObjects converts stored OID metadata to the wire format the API serves.
//
// It runs after the query has narrowed the list, so a request for three objects
// of a ten-thousand-object module converts three.
func wireObjects(oids []*mib.OIDMetadata) []MIBObject {
	objects := make([]MIBObject, 0, len(oids))
	for _, oid := range oids {
		objects = append(objects, newMIBObject(oid))
	}
	return objects
}

// detectTables identifies SNMP tables in a module by analyzing OID patterns.
//
// It takes the full OID metadata rather than a reduced copy so the columns it
// emits carry enumeration labels and display hints, which clients need to
// render table values as text. Objects from other modules are ignored, so it
// accepts either one module's objects or the whole store.
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

// handleGetMIBDashboard generates a dashboard specification for a MIB
func (s *Server) handleGetMIBDashboard(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id, err := strconv.ParseInt(vars["id"], 10, 64)
	if err != nil {
		http.Error(w, "invalid MIB ID", http.StatusBadRequest)
		return
	}

	// Get MIB from database to retrieve the name
	record, err := s.storage.GetMIB(r.Context(), id)
	if err != nil {
		http.Error(w, "MIB not found", http.StatusNotFound)
		return
	}

	log.Printf("INFO: Generating dashboard for MIB ID=%d, Name=%s", id, record.Name)

	// Get MIB storage from loader
	if s.mibLoader == nil {
		log.Printf("ERROR: MIB loader not available for dashboard generation")
		http.Error(w, "MIB loader not available", http.StatusInternalServerError)
		return
	}

	storageInterface := s.mibLoader.GetStorage()
	if storageInterface == nil {
		log.Printf("ERROR: MIB storage not available for dashboard generation")
		http.Error(w, "MIB storage not available", http.StatusInternalServerError)
		return
	}

	loaderStorage, ok := storageInterface.(*mibstorage.Storage)
	if !ok {
		log.Printf("ERROR: Invalid storage type for dashboard generation")
		http.Error(w, "invalid storage type", http.StatusInternalServerError)
		return
	}

	// Fetch learned tables from go-netmon service
	learnedTables := s.fetchLearnedTables(r.Context())
	if learnedTables != nil {
		log.Printf("INFO: Using %d learned tables for dashboard generation", len(learnedTables))
	} else {
		log.Printf("INFO: No learned tables available, using MIB data only")
	}

	// Generate dashboard specification with learned tables
	spec, err := dashboard.GenerateDashboardWithLearnedTables(loaderStorage, record.Name, learnedTables)
	if err != nil {
		log.Printf("ERROR: Failed to generate dashboard for MIB %s (ID=%d): %v", record.Name, id, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Log dashboard statistics for debugging
	log.Printf("INFO: Generated dashboard for %s (ID=%d): %d scalar widgets, %d table widgets",
		record.Name, id, len(spec.ScalarWidgets), len(spec.TableWidgets))

	// Verify the dashboard is for the correct MIB
	if spec.MIBName != record.Name {
		log.Printf("ERROR: Dashboard MIB name mismatch! Requested: %s, Generated: %s", record.Name, spec.MIBName)
		http.Error(w, "dashboard generation produced wrong MIB", http.StatusInternalServerError)
		return
	}

	respondJSON(w, http.StatusOK, spec)
}

// fetchLearnedTables presents the poller's learned tables in the shape the
// dashboard generator wants: the entry OID and its index names.
func (s *Server) fetchLearnedTables(ctx context.Context) dashboard.LearnedTablesMap {
	tables := s.learnedTables(ctx)
	if tables == nil {
		return nil
	}

	result := make(dashboard.LearnedTablesMap, len(tables))
	for oid, table := range tables {
		result[oid] = &dashboard.LearnedTable{
			EntryOID:   table.EntryOID,
			IndexNames: table.IndexNames,
		}
	}
	return result
}

// filterMIBsWithData narrows a MIB list to the modules that actually have data
// flowing — either a table the poller has learned, or a scalar metric present in
// Prometheus.
//
// The work is arranged so cost grows with the size of the store, not with the
// store multiplied by the number of modules. Every OID is read once and grouped
// by module, and Prometheus is asked once for the set of metric names it holds.
// Checking each module against those two in-memory structures is then trivial.
// The previous shape re-read all OIDs per module and issued up to ten Prometheus
// queries per module, which at 170 modules and ~29,000 OIDs meant millions of
// deserializations and well over a thousand round trips — the request timed out,
// and the web UI reported it as "no MIBs assigned".
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
		learnedTables dashboard.LearnedTablesMap
	)
	if device == "" {
		learnedTables = s.fetchLearnedTables(ctx)
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

// prometheusMetricNames returns the metric names Prometheus is currently
// receiving, optionally narrowed by label matchers such as a device name. An
// empty set means the question could not be answered, which callers treat as
// "no scalar evidence" rather than as an error — a MIB with learned table data
// still shows up.
//
// This asks with a query rather than through the label API, which answers at
// block granularity: a metric that stopped arriving half an hour ago was still
// listed as present, so narrowing a MIB left its dashboard panels behind
// promising data that could never come. last_over_time states the window
// exactly, and on this fleet it costs about 30ms against 13,000 series.
func (s *Server) prometheusMetricNames(ctx context.Context, matchers map[string]string) map[string]bool {
	selector := "{" + joinStrings(matcherParts(matchers), ",") + "}"
	if len(matchers) == 0 {
		// A selector has to match something; every metric this project writes
		// carries the name prefix.
		selector = `{__name__=~"` + promname.Prefix + `.*"}`
	}

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

// prometheusInstanceOIDs returns the set of instance OIDs Prometheus holds,
// read from the "oid" label every SNMP series carries. It is what counts
// series: a metric name now covers every row of a table column, so only the
// instance OID distinguishes one row from another.
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

// moduleHasData reports whether a module's objects are backed by collected data.
//
// Metric names are the stronger evidence and the only kind that can be narrowed
// to one device, so they are tried first and for every pollable object. A table
// column counts as well as a scalar: the exporter names a column's metric after
// the object and puts the row in labels, so one name covers every row.
//
// Learned tables are the fallback, for a module whose data has been walked but
// whose names are not in the metric set the caller supplied. They are discovered
// process-wide rather than per device, so a caller asking about one device must
// pass none -- otherwise every module holding a table anyone has walked answers
// yes, which is how the dashboard came to believe 13 modules had data for a
// device that has 2.
func moduleHasData(
	oids []*mib.OIDMetadata,
	learnedTables dashboard.LearnedTablesMap,
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
