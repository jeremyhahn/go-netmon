package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"

	"github.com/jeremyhahn/go-netmon/internal/mib"
	mibstorage "github.com/jeremyhahn/go-netmon/internal/mib/storage"
	webuiStorage "github.com/jeremyhahn/go-netmon/internal/webui/storage"
)

// twoModuleServer builds a server whose MIB store holds an interface table in
// TEST-MIB and an identically shaped one in OTHER-MIB. Handlers that answer for
// one MIB must ignore the other, whatever the size of the store.
func twoModuleServer(t *testing.T) *Server {
	t.Helper()

	webuiStore, err := webuiStorage.New(t.TempDir())
	if err != nil {
		t.Fatalf("create webui storage: %v", err)
	}
	t.Cleanup(func() { webuiStore.Close() })

	mibStore, err := mibstorage.New(t.TempDir())
	if err != nil {
		t.Fatalf("create MIB storage: %v", err)
	}
	t.Cleanup(func() { mibStore.Close() })

	if err := webuiStore.CreateMIB(context.Background(), &webuiStorage.MIB{
		Name:        "TEST-MIB",
		FilePath:    "/test/TEST-MIB.my",
		Description: "Test MIB",
		ObjectCount: 3,
	}); err != nil {
		t.Fatalf("create MIB record: %v", err)
	}

	oids := []*mib.OIDMetadata{
		{OID: "1.3.6.1.2.1.1.1.0", Name: "sysDescr", Module: "TEST-MIB", Type: "DisplayString", ObjectClass: mib.ObjectClassScalar, Pollable: true},
		{OID: "1.3.6.1.2.1.2.2", Name: "ifTable", Module: "TEST-MIB", ObjectClass: mib.ObjectClassTableNode, IsTable: true},
		{OID: "1.3.6.1.2.1.2.2.1", Name: "ifEntry", Module: "TEST-MIB", ObjectClass: mib.ObjectClassEntryNode, IsTableEntry: true, Index: []string{"ifIndex"}},
		{OID: "1.3.6.1.2.1.2.2.1.1", Name: "ifIndex", Module: "TEST-MIB", Type: "Integer32", ObjectClass: mib.ObjectClassIndexColumn, IsColumn: true, Pollable: true, TableOID: "1.3.6.1.2.1.2.2", EntryOID: "1.3.6.1.2.1.2.2.1"},
		{OID: "1.3.6.1.2.1.2.2.1.10", Name: "ifInOctets", Module: "TEST-MIB", Type: "Counter32", ObjectClass: mib.ObjectClassMetricColumn, IsColumn: true, Pollable: true, TableOID: "1.3.6.1.2.1.2.2", EntryOID: "1.3.6.1.2.1.2.2.1"},

		// A second module with the same table shape under a different subtree.
		{OID: "1.3.6.1.4.1.9.9.109.1.1.1", Name: "cpmCPUTotalTable", Module: "OTHER-MIB", ObjectClass: mib.ObjectClassTableNode, IsTable: true},
		{OID: "1.3.6.1.4.1.9.9.109.1.1.1.1", Name: "cpmCPUTotalEntry", Module: "OTHER-MIB", ObjectClass: mib.ObjectClassEntryNode, IsTableEntry: true},
		{OID: "1.3.6.1.4.1.9.9.109.1.1.1.1.8", Name: "cpmCPUTotal5minRev", Module: "OTHER-MIB", Type: "Gauge32", ObjectClass: mib.ObjectClassMetricColumn, IsColumn: true, Pollable: true, TableOID: "1.3.6.1.4.1.9.9.109.1.1.1", EntryOID: "1.3.6.1.4.1.9.9.109.1.1.1.1"},
	}
	for _, oid := range oids {
		if err := mibStore.StoreOID(oid); err != nil {
			t.Fatalf("store %s: %v", oid.Name, err)
		}
	}

	return &Server{
		router:    mux.NewRouter(),
		storage:   webuiStore,
		mibLoader: &mockMIBLoaderForDashboard{storage: mibStore},
	}
}

// getMIB issues a request for MIB id 1 against the given handler.
func getMIB(t *testing.T, handler http.HandlerFunc, id string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, "/api/mibs/"+id, nil)
	req = mux.SetURLVars(req, map[string]string{"id": id})

	w := httptest.NewRecorder()
	handler(w, req)
	return w
}

// TestHandleGetStoredMIB_ScopedToModule verifies the endpoint returns the
// requested MIB's objects and nothing else. It used to read every OID in the
// store and filter in Go, which at ~29,000 OIDs meant deserializing the whole
// store to answer for one MIB.
func TestHandleGetStoredMIB_ScopedToModule(t *testing.T) {
	server := twoModuleServer(t)

	w := getMIB(t, server.handleGetStoredMIB, "1")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d, body: %s", w.Code, w.Body.String())
	}

	var got struct {
		Name    string      `json:"name"`
		Objects []MIBObject `json:"objects"`
	}
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if got.Name != "TEST-MIB" {
		t.Errorf("name = %q, want TEST-MIB", got.Name)
	}
	if len(got.Objects) != 5 {
		t.Fatalf("got %d objects, want the 5 in TEST-MIB", len(got.Objects))
	}
	for _, object := range got.Objects {
		if object.Name == "cpmCPUTotal5minRev" {
			t.Error("returned an object belonging to OTHER-MIB")
		}
	}
}

// TestHandleGetStoredMIB_UnknownID verifies a MIB that does not exist is a 404,
// not an empty success.
func TestHandleGetStoredMIB_UnknownID(t *testing.T) {
	server := twoModuleServer(t)

	if w := getMIB(t, server.handleGetStoredMIB, "999"); w.Code != http.StatusNotFound {
		t.Errorf("status %d, want 404", w.Code)
	}
	if w := getMIB(t, server.handleGetStoredMIB, "not-a-number"); w.Code != http.StatusBadRequest {
		t.Errorf("status %d for a non-numeric id, want 400", w.Code)
	}
}

// TestHandleGetMIBTables_ScopedToModule verifies table detection sees only the
// requested module's objects, so another module's table cannot appear.
func TestHandleGetMIBTables_ScopedToModule(t *testing.T) {
	server := twoModuleServer(t)

	w := getMIB(t, server.handleGetMIBTables, "1")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d, body: %s", w.Code, w.Body.String())
	}

	var got struct {
		Tables []TableMetadata `json:"tables"`
	}
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if len(got.Tables) != 1 {
		t.Fatalf("got %d tables, want 1", len(got.Tables))
	}
	if got.Tables[0].TableName != "ifTable" {
		t.Errorf("table = %q, want ifTable", got.Tables[0].TableName)
	}
	if len(got.Tables[0].Columns) != 2 {
		t.Errorf("got %d columns, want 2", len(got.Tables[0].Columns))
	}
}

// TestHandleGetMIBTables_NoMIBLoader verifies the endpoint reports a missing MIB
// loader rather than panicking on a nil store.
func TestHandleGetMIBTables_NoMIBLoader(t *testing.T) {
	server := twoModuleServer(t)
	server.mibLoader = nil

	if w := getMIB(t, server.handleGetMIBTables, "1"); w.Code != http.StatusInternalServerError {
		t.Errorf("status %d, want 500", w.Code)
	}
}
