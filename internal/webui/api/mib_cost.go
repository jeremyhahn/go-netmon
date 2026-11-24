package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"

	"github.com/jeremyhahn/go-netmon/internal/metrics"
	"github.com/jeremyhahn/go-netmon/internal/mib"
	mibstorage "github.com/jeremyhahn/go-netmon/internal/mib/storage"
	"github.com/jeremyhahn/go-netmon/internal/promname"
)

// MIBSeriesEstimate is how many Prometheus series enabling a MIB on a device would add.
type MIBSeriesEstimate struct {
	MIB         string `json:"mib"`
	Device      string `json:"device"`
	Series      int    `json:"series"`
	Scalars     int    `json:"scalars"`
	Columns     int    `json:"columns"`
	Instances   int    `json:"instances"`   // instances assumed per table
	Exact       bool   `json:"exact"`       // true when counted from Prometheus
	Explanation string `json:"explanation"` // one sentence a human can act on
}

// ResourceNotFoundError reports that a device or MIB named in a request does
// not exist. It is shared by every endpoint addressed as a device/MIB pair, so
// that naming a missing one always reads the same way.
type ResourceNotFoundError struct {
	Kind string // "device" or "MIB"
	ID   int64
}

func (e *ResourceNotFoundError) Error() string {
	return fmt.Sprintf("%s %d not found", e.Kind, e.ID)
}

// seriesEvidence carries what the MIB defines, what Prometheus holds for the device, and the tables the poller walked.
type seriesEvidence struct {
	// objects are every stored object of the MIB, pollable or not. The
	// estimator applies the pollability rule itself so the rule stays visible.
	objects []*mib.OIDMetadata
	// deviceInstanceOIDs is the set of instance OIDs (oid label) Prometheus holds for the device, one per series.
	deviceInstanceOIDs map[string]bool
	// learned maps a table entry OID to what the poller has discovered about
	// it. The learner is process-wide rather than per device, so it describes
	// table shape; instance counts are taken from the device's own series.
	learned map[string]*metrics.LearnedTableInfo
}

// estimateMIBSeries reports how many Prometheus series enabling mibName on
// deviceName would add. It works whether or not the MIB is currently assigned,
// which is the point: the cost is what decides the assignment.
func (s *Server) estimateMIBSeries(ctx context.Context, deviceName, mibName string) (*MIBSeriesEstimate, error) {
	objects, err := s.moduleOIDs(mibName)
	if err != nil {
		return nil, err
	}
	return s.estimateObjectSeries(ctx, deviceName, mibName, objects), nil
}

// estimateObjectSeries prices a module whose objects the caller already holds.
func (s *Server) estimateObjectSeries(
	ctx context.Context,
	deviceName, mibName string,
	objects []*mib.OIDMetadata,
) *MIBSeriesEstimate {
	return estimateSeries(deviceName, mibName, seriesEvidence{
		objects:            objects,
		deviceInstanceOIDs: s.prometheusInstanceOIDs(ctx, map[string]string{promname.DeviceLabel: deviceName}),
		learned:            s.learnedTables(ctx),
	})
}

// estimateSeries counts observed series if any, else prices scalars at 1 and columns at the median row count; unpolled objects cost 0.
func estimateSeries(device, mibName string, ev seriesEvidence) *MIBSeriesEstimate {
	est := &MIBSeriesEstimate{MIB: mibName, Device: device}

	scalarKeys, columnKeys := pollableKeys(ev.objects)
	est.Scalars = len(scalarKeys)
	est.Columns = len(columnKeys)

	if est.Scalars == 0 && est.Columns == 0 {
		est.Explanation = fmt.Sprintf("%s has no pollable objects, so enabling it adds no series", mibName)
		return est
	}

	// Rule 1: the MIB is already being polled here, so Prometheus knows.
	if scalars, columns := countObservedSeries(ev.deviceInstanceOIDs, scalarKeys, columnKeys); scalars+columns > 0 {
		est.Exact = true
		est.Series = scalars + columns
		if est.Columns > 0 {
			est.Instances = columns / est.Columns
		}
		est.Explanation = fmt.Sprintf("exact count from Prometheus: %s already reports %d series for %s",
			device, est.Series, mibName)
		return est
	}

	// Rule 2, scalar-only case: no table means no multiplier, so the arithmetic
	// is exact even though Prometheus has not confirmed it.
	if est.Columns == 0 {
		est.Series = est.Scalars
		est.Explanation = fmt.Sprintf("%s defines no pollable table columns, so each of its %d scalars costs exactly 1 series",
			mibName, est.Scalars)
		return est
	}

	instances, source := instancesPerTable(ev)
	est.Instances = instances
	est.Series = est.Scalars + est.Columns*instances
	est.Explanation = source
	return est
}

// pollableKeys splits a module's pollable objects (mib.OIDMetadata.IsPollable) into scalars and columns by normalized OID.
func pollableKeys(objects []*mib.OIDMetadata) (scalars, columns map[string]bool) {
	scalars = make(map[string]bool)
	columns = make(map[string]bool)

	for _, oid := range objects {
		if !oid.IsPollable() {
			continue
		}
		key := oidKey(oid.OID)
		if key == "" {
			continue
		}
		if oid.IsColumn || oid.EntryOID != "" {
			columns[key] = true
			continue
		}
		scalars[key] = true
	}
	return scalars, columns
}

// countObservedSeries counts the device's series belonging to the MIB, split into scalar and column series.
func countObservedSeries(deviceInstanceOIDs, scalars, columns map[string]bool) (scalarSeries, columnSeries int) {
	for instance := range deviceInstanceOIDs {
		switch key := longestPrefixMatch(instance, scalars, columns); {
		case key == "":
		case columns[key]:
			columnSeries++
		default:
			scalarSeries++
		}
	}
	return scalarSeries, columnSeries
}

// instancesPerTable decides how many rows to assume per table and explains the
// choice in the sentence it returns.
func instancesPerTable(ev seriesEvidence) (int, string) {
	rows := observedTableRows(ev.deviceInstanceOIDs, ev.learned)
	if len(rows) > 0 {
		instances := median(rows)
		return instances, fmt.Sprintf("estimated from a median of %d instances across %d tables observed on this device",
			instances, len(rows))
	}

	if ratios := learnedSampleRatios(ev.learned); len(ratios) > 0 {
		instances := median(ratios)
		return instances, fmt.Sprintf("estimated from a median of %d instances across %d tables the poller has learned",
			instances, len(ratios))
	}

	return 1, "no tables observed yet on this device; assuming 1 instance per table, so tables may cost far more"
}

// observedTableRows counts the rows the device reports for each learned table, the widest column deciding.
func observedTableRows(deviceInstanceOIDs map[string]bool, learned map[string]*metrics.LearnedTableInfo) []int {
	if len(deviceInstanceOIDs) == 0 || len(learned) == 0 {
		return nil
	}

	columnKeys := make(map[string]bool)
	for _, table := range learned {
		for _, column := range table.ColumnOIDs {
			if key := oidKey(column); key != "" {
				columnKeys[key] = true
			}
		}
	}

	counts := make(map[string]int, len(columnKeys))
	for instance := range deviceInstanceOIDs {
		if key := longestPrefixMatch(instance, columnKeys); key != "" {
			counts[key]++
		}
	}

	rows := make([]int, 0, len(learned))
	for _, table := range learned {
		widest := 0
		for _, column := range table.ColumnOIDs {
			if n := counts[oidKey(column)]; n > widest {
				widest = n
			}
		}
		if widest > 0 {
			rows = append(rows, widest)
		}
	}
	return rows
}

// learnedSampleRatios derives a row count per learned table as SampleCount/len(ColumnOIDs); used only without device series.
func learnedSampleRatios(learned map[string]*metrics.LearnedTableInfo) []int {
	ratios := make([]int, 0, len(learned))
	for _, table := range learned {
		if len(table.ColumnOIDs) == 0 {
			continue
		}
		if rows := table.SampleCount / len(table.ColumnOIDs); rows > 0 {
			ratios = append(ratios, rows)
		}
	}
	return ratios
}

// median returns the middle value of the counts, averaging the two middle
// values when there is an even number of them. It reports 0 for no counts,
// which callers treat as "no evidence".
func median(counts []int) int {
	if len(counts) == 0 {
		return 0
	}

	sorted := make([]int, len(counts))
	copy(sorted, counts)
	sort.Ints(sorted)

	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	return (sorted[mid-1] + sorted[mid]) / 2
}

// oidKey normalizes an OID for use as a map key by dropping the leading dot
// some sources write and others do not, so "1.3.6.1.2.1.2.2.1.10" and
// ".1.3.6.1.2.1.2.2.1.10" are the same key.
func oidKey(oid string) string {
	return strings.TrimPrefix(strings.TrimSpace(oid), ".")
}

// longestPrefixMatch returns the longest whole-sub-identifier object key matching an instance OID, or "".
func longestPrefixMatch(instance string, sets ...map[string]bool) string {
	key := oidKey(instance)
	for key != "" {
		for _, set := range sets {
			if set[key] {
				return key
			}
		}
		cut := strings.LastIndex(key, ".")
		if cut < 0 {
			return ""
		}
		key = key[:cut]
	}
	return ""
}

// moduleOIDs returns every stored object belonging to a MIB module.
func (s *Server) moduleOIDs(module string) ([]*mib.OIDMetadata, error) {
	store, err := s.mibStore()
	if err != nil {
		return nil, err
	}

	objects, err := store.ListModuleOIDs(module)
	if err != nil {
		return nil, &MIBStorageError{Reason: "list module OIDs: " + err.Error()}
	}
	return objects, nil
}

// mibStore returns the parsed-MIB metadata store the loader owns.
func (s *Server) mibStore() (*mibstorage.Storage, error) {
	if s.mibLoader == nil {
		return nil, &MIBStorageError{Reason: "MIB loader not available"}
	}

	storageInterface := s.mibLoader.GetStorage()
	if storageInterface == nil {
		return nil, &MIBStorageError{Reason: "MIB storage not available"}
	}

	store, ok := storageInterface.(*mibstorage.Storage)
	if !ok {
		return nil, &MIBStorageError{Reason: "unexpected MIB storage type"}
	}
	return store, nil
}

// learnedTables fetches the tables the poller has discovered by walking
// devices. A nil result means the question could not be answered, which callers
// treat as "no table evidence" rather than as an error.
func (s *Server) learnedTables(ctx context.Context) map[string]*metrics.LearnedTableInfo {
	if s.goNetmonBaseURL == "" {
		return nil
	}

	url := fmt.Sprintf("%s/api/oid-learner/tables", s.goNetmonBaseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		log.Printf("WARNING: Failed to create learned tables request: %v", err)
		return nil
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("WARNING: Failed to fetch learned tables: %v", err)
		return nil
	}
	// Closing the body only returns the connection to the pool; the call's
	// outcome is already determined by what was read above.
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		log.Printf("WARNING: go-netmon service returned status %d for learned tables", resp.StatusCode)
		return nil
	}

	var learnedResp struct {
		Tables map[string]*metrics.LearnedTableInfo `json:"tables"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&learnedResp); err != nil {
		log.Printf("WARNING: Failed to decode learned tables response: %v", err)
		return nil
	}

	// A successful fetch that found nothing is still an answer, so return an
	// empty map rather than nil.
	if learnedResp.Tables == nil {
		return map[string]*metrics.LearnedTableInfo{}
	}
	return learnedResp.Tables
}

// handleEstimateMIBSeries reports the series a MIB would cost on a device, assigned or not.
func (s *Server) handleEstimateMIBSeries(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)

	deviceID, err := strconv.ParseInt(vars["id"], 10, 64)
	if err != nil {
		http.Error(w, "invalid device ID", http.StatusBadRequest)
		return
	}
	mibID, err := strconv.ParseInt(vars["mibId"], 10, 64)
	if err != nil {
		http.Error(w, "invalid MIB ID", http.StatusBadRequest)
		return
	}

	device, err := s.storage.GetDevice(r.Context(), deviceID)
	if err != nil {
		notFound := &ResourceNotFoundError{Kind: "device", ID: deviceID}
		http.Error(w, notFound.Error(), http.StatusNotFound)
		return
	}

	storedMIB, err := s.storage.GetMIB(r.Context(), mibID)
	if err != nil {
		notFound := &ResourceNotFoundError{Kind: "MIB", ID: mibID}
		http.Error(w, notFound.Error(), http.StatusNotFound)
		return
	}

	estimate, err := s.estimateMIBSeries(r.Context(), device.Name, storedMIB.Name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	respondJSON(w, http.StatusOK, estimate)
}
