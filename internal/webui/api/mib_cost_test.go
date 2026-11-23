package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/mux"

	"github.com/jeremyhahn/go-netmon/internal/metrics"
	"github.com/jeremyhahn/go-netmon/internal/mib"
	mibstorage "github.com/jeremyhahn/go-netmon/internal/mib/storage"
	webuistorage "github.com/jeremyhahn/go-netmon/internal/webui/storage"
)

// ifTable, reduced to the shape that matters for cost: a not-accessible row
// node, an accessible index column, and metric columns.
const (
	ifEntryOID   = "1.3.6.1.2.1.2.2.1"
	ifIndexOID   = "1.3.6.1.2.1.2.2.1.1"
	ifInOctets   = "1.3.6.1.2.1.2.2.1.10"
	ifOutOctets  = "1.3.6.1.2.1.2.2.1.16"
	sysUpTimeOID = "1.3.6.1.2.1.1.3"
	sysDescrOID  = "1.3.6.1.2.1.1.1"
)

// scalar builds a pollable scalar object.
func scalar(oid, name string) *mib.OIDMetadata {
	return &mib.OIDMetadata{
		OID: oid, Name: name, Module: "IF-MIB",
		Type: "Counter32", Access: "read-only", Status: "current",
		IsScalar: true, Pollable: true,
	}
}

// column builds a pollable table column.
func column(oid, name string) *mib.OIDMetadata {
	return &mib.OIDMetadata{
		OID: oid, Name: name, Module: "IF-MIB",
		Type: "Counter32", Access: "read-only", Status: "current",
		IsColumn: true, Pollable: true, EntryOID: ifEntryOID, TableOID: "1.3.6.1.2.1.2.2",
	}
}

// instanceOIDs builds the set of instance OIDs a device would report for a
// column with the given row indexes -- one instance OID per series, which is
// what the estimator counts now that a metric name covers a whole column.
func instanceOIDs(oid string, indexes ...int) map[string]bool {
	oids := make(map[string]bool, len(indexes))
	for _, index := range indexes {
		oids[fmt.Sprintf("%s.%d", oid, index)] = true
	}
	return oids
}

// mergeOIDs unions instance OID sets.
func mergeOIDs(sets ...map[string]bool) map[string]bool {
	merged := make(map[string]bool)
	for _, set := range sets {
		for name := range set {
			merged[name] = true
		}
	}
	return merged
}

// TestEstimateSeriesExactPath verifies that a MIB already flowing from the
// device is counted, not guessed: 2 columns over 3 rows plus 1 scalar is 7
// series, and the answer is marked exact.
func TestEstimateSeriesExactPath(t *testing.T) {
	ev := seriesEvidence{
		objects: []*mib.OIDMetadata{
			scalar(sysUpTimeOID, "sysUpTime"),
			column(ifInOctets, "ifInOctets"),
			column(ifOutOctets, "ifOutOctets"),
		},
		deviceInstanceOIDs: mergeOIDs(
			instanceOIDs(ifInOctets, 1, 2, 3),
			instanceOIDs(ifOutOctets, 1, 2, 3),
			map[string]bool{sysUpTimeOID + ".0": true},
			// Another MIB's series must not be counted.
			map[string]bool{"1.3.6.1.4.1.9.9.109.1.1.1.1.8.1": true},
		),
		// Learned tables that disagree must not override a measured count.
		learned: map[string]*metrics.LearnedTableInfo{
			ifEntryOID: {EntryOID: ifEntryOID, ColumnOIDs: []string{ifInOctets, ifOutOctets}, SampleCount: 9999},
		},
	}

	est := estimateSeries("core-01", "IF-MIB", ev)

	if !est.Exact {
		t.Error("a MIB already reporting series must produce an exact answer")
	}
	if est.Series != 7 {
		t.Errorf("Series = %d, want 7", est.Series)
	}
	if est.Scalars != 1 || est.Columns != 2 {
		t.Errorf("Scalars/Columns = %d/%d, want 1/2", est.Scalars, est.Columns)
	}
	if est.Instances != 3 {
		t.Errorf("Instances = %d, want 3 observed rows", est.Instances)
	}
	if !strings.Contains(est.Explanation, "exact count from Prometheus") {
		t.Errorf("Explanation = %q, want it to say the count is exact", est.Explanation)
	}
}

// TestEstimateSeriesFromObservedMedian covers the headline case: a MIB not yet
// enabled here, priced from the row counts the device already shows for other
// tables. Rows of 24, 24 and 4 give a median of 24.
func TestEstimateSeriesFromObservedMedian(t *testing.T) {
	const (
		bgpPeerState = "1.3.6.1.2.1.15.3.1.2"
		cpuLoad      = "1.3.6.1.4.1.9.9.109.1.1.1.1.8"
	)

	ev := seriesEvidence{
		// The MIB being priced: 1 scalar and 2 columns, nothing polled yet.
		objects: []*mib.OIDMetadata{
			scalar(sysDescrOID, "sysDescr"),
			column("1.3.6.1.4.1.9.9.999.1.1.1.2", "cbQosCMPrePolicyPkt"),
			column("1.3.6.1.4.1.9.9.999.1.1.1.3", "cbQosCMPostPolicyByte"),
		},
		deviceInstanceOIDs: mergeOIDs(
			instanceOIDs(ifInOctets, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24),
			instanceOIDs(ifOutOctets, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24),
			instanceOIDs(bgpPeerState, 1, 2, 3, 4),
			instanceOIDs(cpuLoad, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24),
		),
		learned: map[string]*metrics.LearnedTableInfo{
			ifEntryOID:              {EntryOID: ifEntryOID, ColumnOIDs: []string{ifInOctets, ifOutOctets}},
			"1.3.6.1.2.1.15.3.1":    {EntryOID: "1.3.6.1.2.1.15.3.1", ColumnOIDs: []string{bgpPeerState}},
			"1.3.6.1.4.1.9.9.109.1": {EntryOID: "1.3.6.1.4.1.9.9.109.1", ColumnOIDs: []string{cpuLoad}},
		},
	}

	est := estimateSeries("core-01", "CISCO-CLASS-BASED-QOS-MIB", ev)

	if est.Exact {
		t.Error("a MIB with no series of its own cannot be exact")
	}
	if est.Instances != 24 {
		t.Errorf("Instances = %d, want the median row count 24", est.Instances)
	}
	if want := 1 + 2*24; est.Series != want {
		t.Errorf("Series = %d, want %d (1 scalar + 2 columns x 24 rows)", est.Series, want)
	}
	if !strings.Contains(est.Explanation, "24 instances") || !strings.Contains(est.Explanation, "3 tables") {
		t.Errorf("Explanation = %q, want the median and the table count", est.Explanation)
	}
}

// TestEstimateSeriesWorkedExample prices the case the estimator exists for: 22
// pollable columns over 24 interfaces is 528 series, against an object count of
// 22 that would suggest the MIB is free.
func TestEstimateSeriesWorkedExample(t *testing.T) {
	objects := make([]*mib.OIDMetadata, 0, 22)
	for i := 1; i <= 22; i++ {
		objects = append(objects, column(fmt.Sprintf("1.3.6.1.2.1.31.1.1.1.%d", i), fmt.Sprintf("ifXCol%d", i)))
	}

	indexes := make([]int, 24)
	for i := range indexes {
		indexes[i] = i + 1
	}

	ev := seriesEvidence{
		objects:            objects,
		deviceInstanceOIDs: instanceOIDs(ifInOctets, indexes...),
		learned: map[string]*metrics.LearnedTableInfo{
			ifEntryOID: {EntryOID: ifEntryOID, ColumnOIDs: []string{ifInOctets}},
		},
	}

	est := estimateSeries("core-01", "IF-MIB", ev)

	if est.Series != 528 {
		t.Errorf("Series = %d, want 528 (22 columns x 24 interfaces)", est.Series)
	}
	if est.Columns != 22 || est.Instances != 24 {
		t.Errorf("Columns/Instances = %d/%d, want 22/24", est.Columns, est.Instances)
	}
}

// TestEstimateSeriesLearnerRatioFallback verifies the second source of instance
// evidence: with no series for the device, the learner's own counters are used,
// SampleCount over the number of columns.
func TestEstimateSeriesLearnerRatioFallback(t *testing.T) {
	ev := seriesEvidence{
		objects: []*mib.OIDMetadata{
			column(ifInOctets, "ifInOctets"),
			column(ifOutOctets, "ifOutOctets"),
		},
		learned: map[string]*metrics.LearnedTableInfo{
			// 20 samples over 2 columns is 10 rows.
			ifEntryOID: {EntryOID: ifEntryOID, ColumnOIDs: []string{ifInOctets, ifOutOctets}, SampleCount: 20},
			// 12 samples over 2 columns is 6 rows; median of 10 and 6 is 8.
			"1.3.6.1.2.1.15.3.1": {
				EntryOID:    "1.3.6.1.2.1.15.3.1",
				ColumnOIDs:  []string{"1.3.6.1.2.1.15.3.1.2", "1.3.6.1.2.1.15.3.1.3"},
				SampleCount: 12,
			},
		},
	}

	est := estimateSeries("core-01", "IF-MIB", ev)

	if est.Instances != 8 {
		t.Errorf("Instances = %d, want the median ratio 8", est.Instances)
	}
	if est.Series != 16 {
		t.Errorf("Series = %d, want 16 (2 columns x 8 rows)", est.Series)
	}
	if !strings.Contains(est.Explanation, "poller has learned") {
		t.Errorf("Explanation = %q, want it to name the learner as the source", est.Explanation)
	}
}

// TestEstimateSeriesNoLearnedTables covers a device nothing is known about. The
// answer must warn that a table could cost far more than the floor of one row.
func TestEstimateSeriesNoLearnedTables(t *testing.T) {
	ev := seriesEvidence{
		objects: []*mib.OIDMetadata{
			scalar(sysUpTimeOID, "sysUpTime"),
			column(ifInOctets, "ifInOctets"),
			column(ifOutOctets, "ifOutOctets"),
		},
	}

	est := estimateSeries("new-device", "IF-MIB", ev)

	if est.Instances != 1 {
		t.Errorf("Instances = %d, want the floor of 1", est.Instances)
	}
	if est.Series != 3 {
		t.Errorf("Series = %d, want 3 (1 scalar + 2 columns x 1 row)", est.Series)
	}
	if est.Exact {
		t.Error("a guessed answer must not claim to be exact")
	}
	if !strings.Contains(est.Explanation, "no tables observed yet") ||
		!strings.Contains(est.Explanation, "may cost far more") {
		t.Errorf("Explanation = %q, want the warning that the floor is optimistic", est.Explanation)
	}
}

// TestEstimateSeriesDivideByZeroGuard feeds the learner ratios a table with no
// columns recorded and a table with fewer samples than columns. Neither may
// panic or contribute, so the estimate falls back to the floor.
func TestEstimateSeriesDivideByZeroGuard(t *testing.T) {
	ev := seriesEvidence{
		objects: []*mib.OIDMetadata{column(ifInOctets, "ifInOctets")},
		learned: map[string]*metrics.LearnedTableInfo{
			"empty":  {EntryOID: "empty", ColumnOIDs: nil, SampleCount: 500},
			"sparse": {EntryOID: "sparse", ColumnOIDs: []string{ifInOctets, ifOutOctets}, SampleCount: 1},
		},
	}

	est := estimateSeries("core-01", "IF-MIB", ev)

	if est.Instances != 1 {
		t.Errorf("Instances = %d, want the floor of 1 when no table yields a usable ratio", est.Instances)
	}
	if est.Series != 1 {
		t.Errorf("Series = %d, want 1", est.Series)
	}
	if !strings.Contains(est.Explanation, "no tables observed yet") {
		t.Errorf("Explanation = %q, want the no-evidence explanation", est.Explanation)
	}
}

// TestEstimateSeriesScalarsOnly verifies that a MIB with no table columns is
// priced at one series per scalar, with no multiplier to guess at.
func TestEstimateSeriesScalarsOnly(t *testing.T) {
	ev := seriesEvidence{
		objects: []*mib.OIDMetadata{
			scalar(sysUpTimeOID, "sysUpTime"),
			scalar(sysDescrOID, "sysDescr"),
			scalar("1.3.6.1.2.1.1.5", "sysName"),
		},
		learned: map[string]*metrics.LearnedTableInfo{
			ifEntryOID: {EntryOID: ifEntryOID, ColumnOIDs: []string{ifInOctets}, SampleCount: 480},
		},
	}

	est := estimateSeries("core-01", "SNMPv2-MIB", ev)

	if est.Series != 3 {
		t.Errorf("Series = %d, want 3, one per scalar", est.Series)
	}
	if est.Instances != 0 {
		t.Errorf("Instances = %d, want 0 when the MIB has no table columns", est.Instances)
	}
	if !strings.Contains(est.Explanation, "no pollable table columns") {
		t.Errorf("Explanation = %q, want it to say there is no multiplier", est.Explanation)
	}
}

// TestEstimateSeriesNoPollableObjects covers the objects that cost nothing:
// table and row nodes, not-accessible index columns, and deprecated objects.
// A MIB made only of those is free, and that is an answer, not an error.
func TestEstimateSeriesNoPollableObjects(t *testing.T) {
	tests := []struct {
		name   string
		object *mib.OIDMetadata
	}{
		{
			name: "not-accessible index column becomes a label, not a series",
			object: &mib.OIDMetadata{
				OID: ifIndexOID, Name: "ifIndex", Module: "IF-MIB",
				Access: "not-accessible", Status: "current",
				IsColumn: true, IsIndex: true, EntryOID: ifEntryOID,
			},
		},
		{
			name: "table node describes structure and is never fetched",
			object: &mib.OIDMetadata{
				OID: "1.3.6.1.2.1.2.2", Name: "ifTable", Module: "IF-MIB",
				Access: "not-accessible", Status: "current", IsTable: true,
			},
		},
		{
			name: "row node describes structure and is never fetched",
			object: &mib.OIDMetadata{
				OID: ifEntryOID, Name: "ifEntry", Module: "IF-MIB",
				Access: "not-accessible", Status: "current", IsTableEntry: true,
			},
		},
		{
			name: "deprecated object is not polled",
			object: &mib.OIDMetadata{
				OID: ifInOctets, Name: "ifInOctets", Module: "IF-MIB",
				Access: "read-only", Status: "deprecated", IsColumn: true, EntryOID: ifEntryOID,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			est := estimateSeries("core-01", "IF-MIB", seriesEvidence{
				objects: []*mib.OIDMetadata{tt.object},
			})

			if est.Series != 0 || est.Scalars != 0 || est.Columns != 0 {
				t.Errorf("got Series=%d Scalars=%d Columns=%d, want all zero",
					est.Series, est.Scalars, est.Columns)
			}
			if !strings.Contains(est.Explanation, "no pollable objects") {
				t.Errorf("Explanation = %q, want it to say the MIB is free", est.Explanation)
			}
		})
	}
}

// TestEstimateSeriesAccessibleIndexColumnCosts pins behavior that is easy to
// get wrong: an index column the MIB marks read-only, such as ifIndex, is
// walked by the poller and does cost a series per row. Only not-accessible
// index columns are free.
func TestEstimateSeriesAccessibleIndexColumnCosts(t *testing.T) {
	ifIndex := &mib.OIDMetadata{
		OID: ifIndexOID, Name: "ifIndex", Module: "IF-MIB",
		Type: "Integer32", Access: "read-only", Status: "current",
		IsColumn: true, IsIndex: true, Pollable: true, EntryOID: ifEntryOID,
	}

	est := estimateSeries("core-01", "IF-MIB", seriesEvidence{
		objects:            []*mib.OIDMetadata{ifIndex},
		deviceInstanceOIDs: instanceOIDs(ifInOctets, 1, 2, 3, 4),
		learned: map[string]*metrics.LearnedTableInfo{
			ifEntryOID: {EntryOID: ifEntryOID, ColumnOIDs: []string{ifInOctets}},
		},
	})

	if est.Columns != 1 || est.Series != 4 {
		t.Errorf("got Columns=%d Series=%d, want 1 and 4; an accessible index column is polled like any other",
			est.Columns, est.Series)
	}
}

// TestLongestPrefixMatch checks how an instance OID is traced back to the
// object it came from, including instances that belong to nothing.
func TestLongestPrefixMatch(t *testing.T) {
	scalars := map[string]bool{"1.3.6.1.2.1.1.3": true, "1.3.6.1.2.1.1.1.0": true}
	columns := map[string]bool{"1.3.6.1.2.1.2.2.1.10": true}

	tests := []struct {
		name string
		want string
	}{
		{".1.3.6.1.2.1.1.3.0", "1.3.6.1.2.1.1.3"},                       // scalar plus its .0 instance
		{".1.3.6.1.2.1.1.1.0", "1.3.6.1.2.1.1.1.0"},                     // scalar stored with .0 already
		{".1.3.6.1.2.1.2.2.1.10.24", "1.3.6.1.2.1.2.2.1.10"},            // single-part row index
		{".1.3.6.1.2.1.2.2.1.10.203.0.113.218", "1.3.6.1.2.1.2.2.1.10"}, // multi-part row index
		{".1.3.6.1.4.1.9.9.109.1.1.1.1.8.1", ""},                        // another MIB
		{"1.3.6.1.2.1.1.3.0", "1.3.6.1.2.1.1.3"},                        // OID written without a leading dot
		{"", ""},                                                        // no instance at all
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := longestPrefixMatch(tt.name, scalars, columns); got != tt.want {
				t.Errorf("longestPrefixMatch(%q) = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}

// TestMedian covers the middle value for odd and even counts, and the empty
// case that means "no evidence".
func TestMedian(t *testing.T) {
	tests := []struct {
		name   string
		counts []int
		want   int
	}{
		{"odd count takes the middle", []int{4, 24, 24}, 24},
		{"even count averages the middle pair", []int{4, 8, 24, 24}, 16},
		{"unsorted input", []int{24, 4, 8}, 8},
		{"single value", []int{7}, 7},
		{"no counts", nil, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := median(tt.counts); got != tt.want {
				t.Errorf("median(%v) = %d, want %d", tt.counts, got, tt.want)
			}
		})
	}
}

// TestMedianDoesNotMutateInput guards a subtle bug: sorting the caller's slice
// would reorder the learned-table counts it shares with other callers.
func TestMedianDoesNotMutateInput(t *testing.T) {
	counts := []int{24, 4, 8}
	median(counts)

	if counts[0] != 24 || counts[1] != 4 || counts[2] != 8 {
		t.Errorf("median reordered its input: %v", counts)
	}
}

// TestOIDKey pins OID normalization, with and without the leading dot the
// poller emits.
func TestOIDKey(t *testing.T) {
	tests := []struct {
		oid  string
		want string
	}{
		{"1.3.6.1.2.1.1.3", "1.3.6.1.2.1.1.3"},
		{".1.3.6.1.2.1.1.3", "1.3.6.1.2.1.1.3"},
		{" .1.3.6.1.2.1.1.3 ", "1.3.6.1.2.1.1.3"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.oid, func(t *testing.T) {
			if got := oidKey(tt.oid); got != tt.want {
				t.Errorf("oidKey(%q) = %q, want %q", tt.oid, got, tt.want)
			}
		})
	}
}

// TestLongestPrefixMatchWalksSubIdentifiers checks that a column cannot claim
// an instance of a longer-numbered sibling: 1.3.6.1.2.1.2.2.1.1 must not match
// an instance of 1.3.6.1.2.1.2.2.1.10.
func TestLongestPrefixMatchWalksSubIdentifiers(t *testing.T) {
	columns := map[string]bool{ifIndexOID: true}

	if got := longestPrefixMatch(ifInOctets+".5", columns); got != "" {
		t.Errorf("longestPrefixMatch matched %q; 1.3.6.1.2.1.2.2.1.1 is not a prefix of ...1.10.5", got)
	}
	if got := longestPrefixMatch(ifIndexOID+".5", columns); got != ifIndexOID {
		t.Errorf("longestPrefixMatch = %q, want %q", got, ifIndexOID)
	}
}

// newEstimateServer builds a server backed by real on-disk stores holding one
// device and one MIB, with no Prometheus and no poller reachable.
func newEstimateServer(t *testing.T) *Server {
	t.Helper()

	webuiStore, err := webuistorage.New(t.TempDir())
	if err != nil {
		t.Fatalf("create webui storage: %v", err)
	}
	t.Cleanup(func() { webuiStore.Close() })

	mibStore, err := mibstorage.New(t.TempDir())
	if err != nil {
		t.Fatalf("create MIB storage: %v", err)
	}
	t.Cleanup(func() { mibStore.Close() })

	ctx := context.Background()
	if err := webuiStore.CreateDevice(ctx, &webuistorage.Device{
		Name: "core-01", Address: "192.0.2.1", SNMPVersion: "2c", Community: "public", Port: 161,
	}); err != nil {
		t.Fatalf("create device: %v", err)
	}
	if err := webuiStore.CreateMIB(ctx, &webuistorage.MIB{
		Name: "IF-MIB", FilePath: "/mibs/IF-MIB.txt", ObjectCount: 4,
	}); err != nil {
		t.Fatalf("create MIB: %v", err)
	}

	objects := []*mib.OIDMetadata{
		scalar(sysUpTimeOID, "sysUpTime"),
		column(ifInOctets, "ifInOctets"),
		column(ifOutOctets, "ifOutOctets"),
		{
			OID: ifEntryOID, Name: "ifEntry", Module: "IF-MIB",
			Access: "not-accessible", Status: "current", IsTableEntry: true,
		},
	}
	for _, object := range objects {
		if err := mibStore.StoreOID(object); err != nil {
			t.Fatalf("store OID %s: %v", object.Name, err)
		}
	}

	return &Server{
		router:    mux.NewRouter(),
		storage:   webuiStore,
		mibLoader: &mockMIBLoaderForDashboard{storage: mibStore},
	}
}

// TestHandleEstimateMIBSeries checks the endpoint end to end for a MIB that is
// not assigned to the device — the case the endpoint exists for.
func TestHandleEstimateMIBSeries(t *testing.T) {
	server := newEstimateServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/devices/1/mibs/1/estimate", nil)
	req = mux.SetURLVars(req, map[string]string{"id": "1", "mibId": "1"})
	rec := httptest.NewRecorder()

	server.handleEstimateMIBSeries(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	var est MIBSeriesEstimate
	if err := json.NewDecoder(rec.Body).Decode(&est); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if est.Device != "core-01" || est.MIB != "IF-MIB" {
		t.Errorf("got device %q MIB %q, want core-01 and IF-MIB", est.Device, est.MIB)
	}
	// One scalar, two columns, no evidence of instances: 1 + 2x1.
	if est.Scalars != 1 || est.Columns != 2 || est.Series != 3 {
		t.Errorf("got Scalars=%d Columns=%d Series=%d, want 1, 2 and 3",
			est.Scalars, est.Columns, est.Series)
	}
	if est.Exact {
		t.Error("no Prometheus is configured, so the answer cannot be exact")
	}
	if est.Explanation == "" {
		t.Error("Explanation must never be empty; it is the actionable part")
	}
}

// TestHandleEstimateMIBSeriesErrors covers the request shapes that must not
// produce an estimate.
func TestHandleEstimateMIBSeriesErrors(t *testing.T) {
	tests := []struct {
		name     string
		vars     map[string]string
		wantCode int
	}{
		{"unknown device", map[string]string{"id": "404", "mibId": "1"}, http.StatusNotFound},
		{"unknown MIB", map[string]string{"id": "1", "mibId": "404"}, http.StatusNotFound},
		{"non-numeric device ID", map[string]string{"id": "abc", "mibId": "1"}, http.StatusBadRequest},
		{"non-numeric MIB ID", map[string]string{"id": "1", "mibId": "abc"}, http.StatusBadRequest},
		{"missing IDs", map[string]string{}, http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newEstimateServer(t)

			req := httptest.NewRequest(http.MethodGet, "/api/devices/1/mibs/1/estimate", nil)
			req = mux.SetURLVars(req, tt.vars)
			rec := httptest.NewRecorder()

			server.handleEstimateMIBSeries(rec, req)

			if rec.Code != tt.wantCode {
				t.Errorf("status = %d, want %d: %s", rec.Code, tt.wantCode, rec.Body.String())
			}
		})
	}
}

// TestHandleEstimateMIBSeriesWithoutMIBStorage verifies that a server with no
// MIB metadata reports a server error rather than a confident zero.
func TestHandleEstimateMIBSeriesWithoutMIBStorage(t *testing.T) {
	server := newEstimateServer(t)
	server.mibLoader = nil

	req := httptest.NewRequest(http.MethodGet, "/api/devices/1/mibs/1/estimate", nil)
	req = mux.SetURLVars(req, map[string]string{"id": "1", "mibId": "1"})
	rec := httptest.NewRecorder()

	server.handleEstimateMIBSeries(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "mib storage") {
		t.Errorf("body = %q, want it to name the missing MIB storage", rec.Body.String())
	}
}

// TestEstimateMIBSeriesRoute verifies the endpoint is reachable at the
// documented path and method.
func TestEstimateMIBSeriesRoute(t *testing.T) {
	server := newEstimateServer(t)
	server.routes()

	tests := []struct {
		name     string
		method   string
		path     string
		wantCode int
	}{
		{"estimate", http.MethodGet, "/api/devices/1/mibs/1/estimate", http.StatusOK},
		{"unknown device", http.MethodGet, "/api/devices/99/mibs/1/estimate", http.StatusNotFound},
		{"unknown MIB", http.MethodGet, "/api/devices/1/mibs/99/estimate", http.StatusNotFound},
		// Estimating is a read; a write to the same path falls through to the
		// static file handler, which is a miss.
		{"wrong method", http.MethodPost, "/api/devices/1/mibs/1/estimate", http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			rec := httptest.NewRecorder()

			server.router.ServeHTTP(rec, req)

			if rec.Code != tt.wantCode {
				t.Errorf("%s %s = %d, want %d: %s", tt.method, tt.path, rec.Code, tt.wantCode, rec.Body.String())
			}
		})
	}
}

// TestLearnedTables checks the wire contract with the poller's OID learner. The
// column_oids and sample_count fields are what price a table, so a rename on
// either side must fail here rather than silently produce a floor estimate.
func TestLearnedTables(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body       string
		wantTables int
		wantNil    bool
	}{
		{
			name:   "tables with the fields the estimate needs",
			status: http.StatusOK,
			body: `{"tables":{"1.3.6.1.2.1.2.2.1":{"entry_oid":"1.3.6.1.2.1.2.2.1",
				"column_oids":["1.3.6.1.2.1.2.2.1.10","1.3.6.1.2.1.2.2.1.16"],"sample_count":48}}}`,
			wantTables: 1,
		},
		{
			name:       "poller knows of no tables",
			status:     http.StatusOK,
			body:       `{"tables":{}}`,
			wantTables: 0,
		},
		{
			name:    "poller reports an error",
			status:  http.StatusInternalServerError,
			body:    `{"error":"learner unavailable"}`,
			wantNil: true,
		},
		{
			name:    "malformed response",
			status:  http.StatusOK,
			body:    `{"tables":`,
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			poller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/oid-learner/tables" {
					t.Errorf("requested %q, want /api/oid-learner/tables", r.URL.Path)
				}
				w.WriteHeader(tt.status)
				w.Write([]byte(tt.body))
			}))
			defer poller.Close()

			server := &Server{goNetmonBaseURL: poller.URL}
			tables := server.learnedTables(context.Background())

			if tt.wantNil {
				if tables != nil {
					t.Fatalf("got %d tables, want nil when the answer is unusable", len(tables))
				}
				return
			}
			if tables == nil {
				t.Fatal("a successful fetch must return a map, even an empty one")
			}
			if len(tables) != tt.wantTables {
				t.Fatalf("got %d tables, want %d", len(tables), tt.wantTables)
			}

			if table := tables[ifEntryOID]; table != nil {
				if len(table.ColumnOIDs) != 2 || table.SampleCount != 48 {
					t.Errorf("got %d columns and %d samples, want 2 and 48",
						len(table.ColumnOIDs), table.SampleCount)
				}
			}
		})
	}
}

// TestLearnedTablesWithoutPoller verifies that no poller URL means no request
// and no table evidence, rather than a stalled request.
func TestLearnedTablesWithoutPoller(t *testing.T) {
	server := &Server{}

	if tables := server.learnedTables(context.Background()); tables != nil {
		t.Errorf("got %d tables, want nil when no poller is configured", len(tables))
	}
}

// TestLearnedTablesCarryTheEntryOIDAndItsIndexes pins what the module filter
// reads out of the poller's answer.
//
// The dashboard used to consume these too, through an adapter that reshaped
// them for a MIB-parsing generator. That generator is gone -- panels come from
// the catalog now -- and the only remaining reader is the "does this module
// have data?" filter, which needs the entry OID and nothing else. The adapter
// went with it rather than being kept for one caller.
func TestLearnedTablesCarryTheEntryOIDAndItsIndexes(t *testing.T) {
	poller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"tables":{"1.3.6.1.2.1.2.2.1":{"entry_oid":"1.3.6.1.2.1.2.2.1",
			"index_names":["ifIndex"],"column_oids":["1.3.6.1.2.1.2.2.1.10"],"sample_count":24}}}`))
	}))
	defer poller.Close()

	tables := (&Server{goNetmonBaseURL: poller.URL}).learnedTables(context.Background())

	table := tables[ifEntryOID]
	if table == nil {
		t.Fatalf("got %v, want the ifEntry table", tables)
	}
	if table.EntryOID != ifEntryOID || len(table.IndexNames) != 1 || table.IndexNames[0] != "ifIndex" {
		t.Errorf("got %+v, want the entry OID and its ifIndex index", table)
	}
}

// TestMIBStoreErrors covers the ways the MIB metadata store can be unusable. An
// estimate built on a missing store would report a confident zero, so each case
// must be an error instead.
func TestMIBStoreErrors(t *testing.T) {
	tests := []struct {
		name       string
		loader     MIBLoader
		wantReason string
	}{
		{"no loader", nil, "MIB loader not available"},
		{"loader without storage", &mockMIBLoaderForDashboard{storage: nil}, "MIB storage not available"},
		{"loader with the wrong storage", &mockMIBLoaderForDashboard{storage: "not a store"}, "unexpected MIB storage type"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := (&Server{mibLoader: tt.loader}).moduleOIDs("IF-MIB")

			storageErr, ok := err.(*MIBStorageError)
			if !ok {
				t.Fatalf("err = %v (%T), want *MIBStorageError", err, err)
			}
			if storageErr.Reason != tt.wantReason {
				t.Errorf("Reason = %q, want %q", storageErr.Reason, tt.wantReason)
			}
		})
	}
}
