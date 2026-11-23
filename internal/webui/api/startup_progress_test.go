package api

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-netmon/internal/webui/storage"
)

// storedMIBServer builds a server whose database already holds the named MIBs,
// which is the state a restart finds: the imports are recorded, and reloading
// them into the parser's store is the whole of the cold start.
func storedMIBServer(t *testing.T, names ...string) (*Server, *mockMIBLoaderWithTracking) {
	t.Helper()

	store, err := storage.New(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	for _, name := range names {
		record := storage.MIB{Name: name, FilePath: "/var/lib/go-netmon/webui/mibs/" + name + ".my"}
		if err := store.CreateMIB(ctx, &record); err != nil {
			t.Fatalf("create MIB %s: %v", name, err)
		}
	}

	loader := &mockMIBLoaderWithTracking{loadErrors: make(map[string]error)}
	return &Server{storage: store, mibLoader: loader}, loader
}

// The stored-MIB load is minutes long and runs before the listener exists. What
// it reports is the only thing that separates "still working" from "wedged".
func TestLoadStoredMIBsReportsProgress(t *testing.T) {
	names := []string{"IF-MIB", "SNMPv2-MIB", "UDP-MIB"}
	server, _ := storedMIBServer(t, names...)

	type step struct {
		done, total int
		module      string
	}
	var steps []step
	server.startupProgress = func(done, total int, module string) {
		steps = append(steps, step{done, total, module})
	}

	if err := server.LoadStoredMIBs(context.Background()); err != nil {
		t.Fatalf("LoadStoredMIBs: %v", err)
	}

	if len(steps) != len(names) {
		t.Fatalf("progress reported %d times, want %d", len(steps), len(names))
	}
	for i, got := range steps {
		if got.done != i+1 || got.total != len(names) {
			t.Errorf("step %d reported %d/%d, want %d/%d", i, got.done, got.total, i+1, len(names))
		}
		if got.module == "" {
			t.Errorf("step %d named no module", i)
		}
	}
}

// A module that fails to load is still a module the loop is finished with.
// Progress that stalls on one broken import out of 176 reads exactly like the
// wedge this reporting exists to rule out.
func TestLoadStoredMIBsReportsProgressThroughAFailure(t *testing.T) {
	server, loader := storedMIBServer(t, "IF-MIB", "BROKEN-MIB")
	loader.loadErrors["BROKEN-MIB"] = &MIBStorageError{Reason: "will not parse"}

	reported := 0
	server.startupProgress = func(int, int, string) { reported++ }

	if err := server.LoadStoredMIBs(context.Background()); err != nil {
		t.Fatalf("LoadStoredMIBs: %v", err)
	}
	if reported != 2 {
		t.Errorf("progress reported %d times, want 2 — the failing module counts", reported)
	}
}

// No reporter is the default, and it must be silence rather than a nil call.
func TestLoadStoredMIBsWithoutAProgressReporter(t *testing.T) {
	server, _ := storedMIBServer(t, "IF-MIB")

	if err := server.LoadStoredMIBs(context.Background()); err != nil {
		t.Fatalf("LoadStoredMIBs: %v", err)
	}
}
