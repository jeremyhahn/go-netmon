package metrics

import "testing"

// ifTable columns, two of them, walked across three interfaces. The entry OID
// is 1.3.6.1.2.1.2.2.1, the column follows it, and the rest is the row index.
var ifTableWalk = []string{
	"1.3.6.1.2.1.2.2.1.10.1", "1.3.6.1.2.1.2.2.1.16.1",
	"1.3.6.1.2.1.2.2.1.10.2", "1.3.6.1.2.1.2.2.1.16.2",
	"1.3.6.1.2.1.2.2.1.10.3", "1.3.6.1.2.1.2.2.1.16.3",
}

const ifEntryOID = "1.3.6.1.2.1.2.2.1"

// TestOIDLearner_SampleCountIsInstancesNotPolls verifies the sample count
// describes the table rather than the poll history: walking the same table ten
// times must not multiply it by ten. Consumers divide SampleCount by the number
// of columns to get a row count, which a cumulative counter made meaningless.
func TestOIDLearner_SampleCountIsInstancesNotPolls(t *testing.T) {
	learner := NewOIDLearner(nil)

	for range 10 {
		learner.LearnFromResponses(ifTableWalk)
	}

	table := learner.GetLearnedTable(ifEntryOID)
	if table == nil {
		t.Fatalf("table %s was not discovered", ifEntryOID)
	}
	if len(table.ColumnOIDs) != 2 {
		t.Fatalf("got %d columns, want 2", len(table.ColumnOIDs))
	}
	if table.SampleCount != 6 {
		t.Errorf("got SampleCount %d after 10 polls of a 3-row, 2-column table, want 6", table.SampleCount)
	}
	if rows := table.SampleCount / len(table.ColumnOIDs); rows != 3 {
		t.Errorf("derived %d rows, want 3", rows)
	}
}

// TestOIDLearner_SampleCountFollowsTableGrowth verifies new rows and new columns
// are still counted: the count is distinct, not frozen.
func TestOIDLearner_SampleCountFollowsTableGrowth(t *testing.T) {
	learner := NewOIDLearner(nil)
	learner.LearnFromResponses(ifTableWalk)

	// A fourth interface appears.
	learner.LearnFromResponses([]string{
		"1.3.6.1.2.1.2.2.1.10.4", "1.3.6.1.2.1.2.2.1.16.4",
	})

	table := learner.GetLearnedTable(ifEntryOID)
	if table == nil {
		t.Fatalf("table %s was not discovered", ifEntryOID)
	}
	if table.SampleCount != 8 {
		t.Errorf("got SampleCount %d for a 4-row, 2-column table, want 8", table.SampleCount)
	}
	if rows := table.SampleCount / len(table.ColumnOIDs); rows != 4 {
		t.Errorf("derived %d rows, want 4", rows)
	}
}

// TestOIDLearner_MultiPartIndexCountsOneRow verifies a compound index counts as
// a single row, not as one row per sub-identifier.
func TestOIDLearner_MultiPartIndexCountsOneRow(t *testing.T) {
	learner := NewOIDLearner(nil)

	// A BGP-style table indexed by peer address: entry 1.3.6.1.2.1.15.3.1,
	// two columns, two peers.
	learner.LearnFromResponses([]string{
		"1.3.6.1.2.1.15.3.1.2.198.51.100.101", "1.3.6.1.2.1.15.3.1.3.198.51.100.101",
		"1.3.6.1.2.1.15.3.1.2.198.51.100.102", "1.3.6.1.2.1.15.3.1.3.198.51.100.102",
	})

	table := learner.GetLearnedTable("1.3.6.1.2.1.15.3.1")
	if table == nil {
		t.Fatal("table 1.3.6.1.2.1.15.3.1 was not discovered")
	}
	if rows := table.SampleCount / len(table.ColumnOIDs); rows != 2 {
		t.Errorf("derived %d rows from a 2-peer table, want 2", rows)
	}
}

// TestOIDLearner_RejectsUnusableOIDs verifies malformed and too-short input is
// ignored rather than turned into bogus tables or a panic. SNMP walks return
// scalars, empty varbinds and vendor OIDs of every shape.
func TestOIDLearner_RejectsUnusableOIDs(t *testing.T) {
	learner := NewOIDLearner(nil)

	learner.LearnFromResponses([]string{
		"",                     // empty varbind
		".",                    // nothing but a separator
		"1.3.6.1.2.1.1.1.0",    // scalar, too short to be a table
		"not.an.oid.at.all",    // non-numeric
		"1.3.6.1.2.1.2.2.1.10", // a column with no instance
	})

	if tables := learner.GetAllLearnedTables(); len(tables) != 0 {
		t.Errorf("discovered %d tables from unusable input, want 0", len(tables))
	}
	if got := learner.GetLearnedTable("1.3.6.1.2.1.1.1"); got != nil {
		t.Errorf("got table %+v for a scalar OID, want nil", got)
	}
}

// TestOIDLearner_SingleColumnIsNotATable verifies one column, however many rows,
// is not reported as a table: the row count would be indistinguishable from a
// list of scalars.
func TestOIDLearner_SingleColumnIsNotATable(t *testing.T) {
	learner := NewOIDLearner(nil)

	learner.LearnFromResponses([]string{
		"1.3.6.1.2.1.2.2.1.10.1",
		"1.3.6.1.2.1.2.2.1.10.2",
		"1.3.6.1.2.1.2.2.1.10.3",
	})

	if got := learner.GetLearnedTable(ifEntryOID); got != nil {
		t.Errorf("got table %+v for a single column, want nil", got)
	}
}
