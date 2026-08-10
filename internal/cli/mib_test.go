package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-netmon/pkg/client"
)

// testMIBs is the module list the fake API serves for listing and resolution.
func testMIBs() []client.MIB {
	return []client.MIB{
		{
			ID:          7,
			Name:        "IF-MIB",
			ObjectCount: 120,
			Tags:        []string{"standard", "interfaces"},
			ImportedAt:  time.Now().Add(-3 * time.Hour),
		},
		{
			ID:          8,
			Name:        "CISCO-ENVMON-MIB",
			ObjectCount: 40,
			Tags:        []string{"cisco"},
			ImportedAt:  time.Now().Add(-30 * time.Minute),
		},
	}
}

// testMIBDetail builds a module with count objects, half of them pollable, so
// filtering and limiting have something to bite on.
func testMIBDetail(count int) client.MIBDetail {
	objects := make([]client.MIBObject, 0, count)
	for i := 0; i < count; i++ {
		objects = append(objects, client.MIBObject{
			OID:         fmt.Sprintf("1.3.6.1.2.1.2.2.1.%d", i),
			Name:        fmt.Sprintf("ifObject%d", i),
			Type:        "Counter32",
			Description: fmt.Sprintf("counter number %d", i),
			Unit:        "octets",
			Access:      "read-only",
			Pollable:    i%2 == 0,
		})
	}
	return client.MIBDetail{
		ID:          7,
		Name:        "IF-MIB",
		Description: "Interfaces group",
		FilePath:    "/var/lib/netmon/mibs/IF-MIB.txt",
		ObjectCount: count,
		ImportedAt:  time.Now().Add(-3 * time.Hour),
		Objects:     objects,
	}
}

// mibListHandler serves the module list with the API's narrowing applied,
// recording the query it was asked for.
func mibListHandler(t *testing.T, query *string) http.HandlerFunc {
	return catalogHandler(t, testMIBs(), query)
}

func TestMIBListShowsTagsAndHonorsAvailable(t *testing.T) {
	var query string

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/mibs", mibListHandler(t, &query))

	env, out := newFakeEnv(t, mux)
	if err := runCommand(t, newMIBCommand(env), "list"); err != nil {
		t.Fatalf("list: %v", err)
	}

	text := out.String()
	for _, want := range []string{"OBJECTS", "TAGS", "IMPORTED", "IF-MIB", "standard,interfaces"} {
		if !strings.Contains(text, want) {
			t.Errorf("listing missing %q:\n%s", want, text)
		}
	}
	// The cap is the server's work now: a default listing asks for fifty
	// modules rather than downloading the catalog to print fifty of it.
	if query != "limit="+strconv.Itoa(defaultMIBLimit) {
		t.Errorf("query = %q, want the default cap sent to the server", query)
	}

	out.Reset()
	if err := runCommand(t, newMIBCommand(env), "list", "--available"); err != nil {
		t.Fatalf("list --available: %v", err)
	}
	if !strings.Contains(query, "filter=available") {
		t.Errorf("query = %q, want filter=available", query)
	}
}

func TestMIBListFiltersByTag(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/mibs", mibListHandler(t, nil))

	env, out := newFakeEnv(t, mux)
	if err := runCommand(t, newMIBCommand(env), "list", "--tag", "CISCO"); err != nil {
		t.Fatalf("list --tag: %v", err)
	}

	text := out.String()
	if !strings.Contains(text, "CISCO-ENVMON-MIB") {
		t.Errorf("tag match dropped:\n%s", text)
	}
	if strings.Contains(text, "IF-MIB") {
		t.Errorf("untagged module survived the filter:\n%s", text)
	}

	out.Reset()
	if err := runCommand(t, newMIBCommand(env), "list", "--tag", "nonexistent"); err != nil {
		t.Fatalf("list --tag nonexistent: %v", err)
	}
	if !strings.Contains(out.String(), "No MIBs match") {
		t.Errorf("expected an empty-result notice, got: %q", out.String())
	}
}

func TestMIBListServerError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/mibs", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})

	env, _ := newFakeEnv(t, mux)
	err := runCommand(t, newMIBCommand(env), "list")

	var apiErr *client.APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected a 500 APIError, got %v", err)
	}
}

func TestMIBShowReportsTruncationByLimit(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/mibs/7", mibDetailHandler(t, testMIBDetail(120), nil))

	env, out := newFakeEnv(t, mux)
	if err := runCommand(t, newMIBCommand(env), "show", "7", "--limit", "10"); err != nil {
		t.Fatalf("show: %v", err)
	}

	text := out.String()
	if !strings.Contains(text, "showing 10 of 120 objects") {
		t.Errorf("truncation notice missing:\n%s", text)
	}
	if !strings.Contains(text, "use --limit 0 for all") {
		t.Errorf("truncation notice must say how to see everything:\n%s", text)
	}
	if !strings.Contains(text, "ifObject9") {
		t.Errorf("first ten objects should be printed:\n%s", text)
	}
	if strings.Contains(text, "ifObject10 ") {
		t.Errorf("limit was not applied:\n%s", text)
	}
}

func TestMIBShowUnlimitedHasNoNotice(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/mibs/7", mibDetailHandler(t, testMIBDetail(80), nil))

	env, out := newFakeEnv(t, mux)
	if err := runCommand(t, newMIBCommand(env), "show", "7", "--limit", "0"); err != nil {
		t.Fatalf("show --limit 0: %v", err)
	}

	text := out.String()
	if strings.Contains(text, "showing") {
		t.Errorf("--limit 0 must print everything without a notice:\n%s", text)
	}
	if !strings.Contains(text, "ifObject79") {
		t.Errorf("last object missing:\n%s", text)
	}
	if !strings.Contains(text, "Interfaces group") {
		t.Errorf("module summary missing:\n%s", text)
	}
}

func TestMIBShowSearchAndPollableOnly(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/mibs/7", mibDetailHandler(t, testMIBDetail(10), nil))

	env, out := newFakeEnv(t, mux)
	if err := runCommand(t, newMIBCommand(env), "show", "7", "--search", "IFOBJECT3"); err != nil {
		t.Fatalf("show --search: %v", err)
	}
	text := out.String()
	if !strings.Contains(text, "ifObject3") {
		t.Errorf("search should match case-insensitively:\n%s", text)
	}
	if strings.Contains(text, "ifObject4") {
		t.Errorf("search matched too much:\n%s", text)
	}

	out.Reset()
	if err := runCommand(t, newMIBCommand(env), "show", "7", "--pollable-only", "--limit", "0"); err != nil {
		t.Fatalf("show --pollable-only: %v", err)
	}
	text = out.String()
	if !strings.Contains(text, "ifObject2") {
		t.Errorf("pollable object missing:\n%s", text)
	}
	if strings.Contains(text, "ifObject3") {
		t.Errorf("non-pollable object survived --pollable-only:\n%s", text)
	}
}

func TestMIBShowNoMatchingObjects(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/mibs/7", mibDetailHandler(t, testMIBDetail(5), nil))

	env, out := newFakeEnv(t, mux)
	if err := runCommand(t, newMIBCommand(env), "show", "7", "--search", "nothing-here"); err != nil {
		t.Fatalf("show: %v", err)
	}
	// A search that matched nothing has to name the search and say how much
	// was searched, so the reader knows the objects exist and the query missed.
	printed := out.String()
	for _, want := range []string{"No objects match", "nothing-here", "searched 5"} {
		if !strings.Contains(printed, want) {
			t.Errorf("expected an explicit empty result containing %q:\n%s", want, printed)
		}
	}
}

func TestMIBShowUnknownModule(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/mibs", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, map[string]any{"mibs": testMIBs()})
	})

	env, _ := newFakeEnv(t, mux)
	err := runCommand(t, newMIBCommand(env), "show", "NO-SUCH-MIB")

	var notFound *client.NotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("expected *client.NotFoundError, got %v", err)
	}
	if notFound.Kind != "MIB" {
		t.Errorf("kind = %q, want MIB", notFound.Kind)
	}
}

func TestMIBObjectsOmitsSummary(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/mibs/7", mibDetailHandler(t, testMIBDetail(3), nil))

	env, out := newFakeEnv(t, mux)
	if err := runCommand(t, newMIBCommand(env), "objects", "7"); err != nil {
		t.Fatalf("objects: %v", err)
	}

	text := out.String()
	if strings.Contains(text, "Imported:") || strings.Contains(text, "Interfaces group") {
		t.Errorf("objects must print the table only, for piping:\n%s", text)
	}
	if !strings.HasPrefix(text, "OID") {
		t.Errorf("output should start with the table header:\n%s", text)
	}
	if !strings.Contains(text, "ifObject2") {
		t.Errorf("objects missing:\n%s", text)
	}
}

func TestMIBObjectsServerError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/mibs/7", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	})

	env, _ := newFakeEnv(t, mux)
	err := runCommand(t, newMIBCommand(env), "objects", "7")

	var apiErr *client.APIError
	if !errors.As(err, &apiErr) || !apiErr.NotFound() {
		t.Fatalf("expected a 404 APIError, got %v", err)
	}
}

func TestMIBTables(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/mibs/7/tables", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, map[string]any{"tables": []client.Table{{
			TableOID:    "1.3.6.1.2.1.2.2",
			TableName:   "ifTable",
			EntryOID:    "1.3.6.1.2.1.2.2.1",
			EntryName:   "ifEntry",
			IndexColumn: "ifIndex",
			Columns: []client.MIBObject{
				{OID: "1.3.6.1.2.1.2.2.1.1", Name: "ifIndex", Type: "Integer32", Access: "read-only"},
				{OID: "1.3.6.1.2.1.2.2.1.10", Name: "ifInOctets", Type: "Counter32", Pollable: true},
			},
		}}})
	})

	env, out := newFakeEnv(t, mux)
	if err := runCommand(t, newMIBCommand(env), "tables", "7"); err != nil {
		t.Fatalf("tables: %v", err)
	}

	text := out.String()
	for _, want := range []string{"ifTable", "1.3.6.1.2.1.2.2", "ifEntry", "ifIndex", "ifInOctets"} {
		if !strings.Contains(text, want) {
			t.Errorf("table output missing %q:\n%s", want, text)
		}
	}
}

func TestMIBTablesEmptyAndError(t *testing.T) {
	t.Run("none defined", func(t *testing.T) {
		mux := http.NewServeMux()
		mux.HandleFunc("GET /api/mibs/7/tables", func(w http.ResponseWriter, _ *http.Request) {
			respondJSON(t, w, map[string]any{"tables": []client.Table{}})
		})

		env, out := newFakeEnv(t, mux)
		if err := runCommand(t, newMIBCommand(env), "tables", "7"); err != nil {
			t.Fatalf("tables: %v", err)
		}
		if !strings.Contains(out.String(), "no tables") {
			t.Errorf("expected an explicit notice, got: %q", out.String())
		}
	})

	t.Run("server error", func(t *testing.T) {
		mux := http.NewServeMux()
		mux.HandleFunc("GET /api/mibs/7/tables", func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "boom", http.StatusInternalServerError)
		})

		env, _ := newFakeEnv(t, mux)
		err := runCommand(t, newMIBCommand(env), "tables", "7")

		var apiErr *client.APIError
		if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusInternalServerError {
			t.Fatalf("expected a 500 APIError, got %v", err)
		}
	})
}

// importHandler accepts uploads, rejecting any file whose name contains "bad"
// so a batch can be made to fail part way through.
func importHandler(t *testing.T, seen *[]string, tags *string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		file, header, err := r.FormFile("file")
		if err != nil {
			t.Fatalf("read upload: %v", err)
		}
		defer file.Close()

		*seen = append(*seen, header.Filename)
		if tags != nil {
			*tags = r.FormValue("tags")
		}
		if strings.Contains(header.Filename, "bad") {
			http.Error(w, "parse error: unexpected token", http.StatusBadRequest)
			return
		}

		// The real server refuses an already-stored module unless the caller
		// opted in, and reports a refresh as a replacement rather than a
		// create. "STORED-MIB" stands in for a module that is already there.
		name := strings.TrimSuffix(header.Filename, ".mib")
		replace := r.URL.Query().Get("replace") == "true"
		if name == "STORED-MIB" && !replace {
			http.Error(w, "MIB STORED-MIB is already imported as id 9. "+
				"To refresh its objects in place ... use ?replace=true",
				http.StatusConflict)
			return
		}

		respondJSON(t, w, client.MIBImportResult{
			MIB:      client.MIB{ID: 9, Name: name, ObjectCount: 12},
			Replaced: replace,
		})
	}
}

// writeMIBFiles creates placeholder MIB files and returns their paths.
func writeMIBFiles(t *testing.T, names ...string) []string {
	t.Helper()

	dir := t.TempDir()
	paths := make([]string, 0, len(names))
	for _, name := range names {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("-- placeholder MIB\n"), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		paths = append(paths, path)
	}
	return paths
}

func TestMIBImportAppliesTags(t *testing.T) {
	var (
		seen []string
		tags string
	)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/mibs/import", importHandler(t, &seen, &tags))

	env, out := newFakeEnv(t, mux)
	paths := writeMIBFiles(t, "IF-MIB.mib", "BRIDGE-MIB.mib")

	args := append([]string{"import", "--tags", "cisco,switch"}, paths...)
	if err := runCommand(t, newMIBCommand(env), args...); err != nil {
		t.Fatalf("import: %v", err)
	}

	if len(seen) != 2 {
		t.Fatalf("server saw %v, want both files", seen)
	}
	if tags != "cisco,switch" {
		t.Errorf("tags = %q, want cisco,switch", tags)
	}
	if !strings.Contains(out.String(), "2 imported, 0 updated, 0 failed") {
		t.Errorf("summary missing:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "imported "+paths[0]+" as IF-MIB (id 9, 12 objects)") {
		t.Errorf("per-file line missing:\n%s", out.String())
	}
}

func TestMIBImportContinuesPastFailure(t *testing.T) {
	var seen []string

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/mibs/import", importHandler(t, &seen, nil))

	env, out := newFakeEnv(t, mux)
	paths := writeMIBFiles(t, "bad-one.mib", "GOOD-MIB.mib")
	// A path that does not exist must be reported like any other failure
	// rather than aborting the batch before it starts.
	paths = append(paths, filepath.Join(t.TempDir(), "missing.mib"))

	args := append([]string{"import"}, paths...)
	err := runCommand(t, newMIBCommand(env), args...)

	var importErr *ImportError
	if !errors.As(err, &importErr) {
		t.Fatalf("expected *ImportError, got %v", err)
	}
	if importErr.Failed != 2 || importErr.Total != 3 {
		t.Errorf("tally = %+v, want 2 of 3 failed", importErr)
	}

	if len(seen) != 2 || seen[0] != "bad-one.mib" || seen[1] != "GOOD-MIB.mib" {
		t.Fatalf("server saw %v; the good file must still be attempted", seen)
	}

	text := out.String()
	if !strings.Contains(text, "FAILED   "+paths[0]) {
		t.Errorf("failing file not reported:\n%s", text)
	}
	if !strings.Contains(text, "imported "+paths[1]) {
		t.Errorf("good file not imported after a failure:\n%s", text)
	}
	if !strings.Contains(text, "1 imported, 0 updated, 2 failed") {
		t.Errorf("summary missing:\n%s", text)
	}
}

func TestMIBSetTags(t *testing.T) {
	var received map[string][]string

	mux := http.NewServeMux()
	mux.HandleFunc("PUT /api/mibs/8/tags", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	})

	env, out := newFakeEnv(t, mux)
	err := runCommand(t, newMIBCommand(env), "tags", "8", "--set", "cisco,environment")
	if err != nil {
		t.Fatalf("tags: %v", err)
	}

	want := []string{"cisco", "environment"}
	if len(received["tags"]) != len(want) {
		t.Fatalf("server received %v, want %v", received["tags"], want)
	}
	for i, tag := range want {
		if received["tags"][i] != tag {
			t.Errorf("tag %d = %q, want %q", i, received["tags"][i], tag)
		}
	}
	if !strings.Contains(out.String(), "Tagged 8: cisco,environment") {
		t.Errorf("unexpected output: %q", out.String())
	}
}

func TestMIBSetTagsRequiresSet(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /api/mibs/8/tags", func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("server must not be called without --set")
	})

	env, _ := newFakeEnv(t, mux)
	if err := runCommand(t, newMIBCommand(env), "tags", "8"); err == nil {
		t.Fatal("expected an error when --set is omitted")
	}
}

func TestMIBDeleteRequiresForce(t *testing.T) {
	deleted := false

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/mibs/7", mibDetailHandler(t, testMIBDetail(2), nil))
	mux.HandleFunc("DELETE /api/mibs/7", func(w http.ResponseWriter, _ *http.Request) {
		deleted = true
		w.WriteHeader(http.StatusNoContent)
	})

	env, out := newFakeEnv(t, mux)

	err := runCommand(t, newMIBCommand(env), "delete", "7")
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("expected a refusal mentioning --force, got %v", err)
	}
	if deleted {
		t.Fatal("MIB was deleted without --force")
	}

	if err := runCommand(t, newMIBCommand(env), "delete", "7", "--force"); err != nil {
		t.Fatalf("forced delete: %v", err)
	}
	if !deleted {
		t.Fatal("forced delete did not reach the server")
	}
	if !strings.Contains(out.String(), "Deleted MIB IF-MIB (id 7)") {
		t.Errorf("unexpected output: %q", out.String())
	}
}

func TestMIBShowJSONCarriesFilteredObjects(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/mibs/7", mibDetailHandler(t, testMIBDetail(10), nil))

	env, out := newFakeEnv(t, mux)
	env.Opts.JSON = true
	if err := runCommand(t, newMIBCommand(env), "show", "7", "--pollable-only"); err != nil {
		t.Fatalf("show --json: %v", err)
	}

	var detail client.MIBDetail
	if err := json.Unmarshal(out.Bytes(), &detail); err != nil {
		t.Fatalf("output was not JSON: %v (%q)", err, out.String())
	}
	if len(detail.Objects) != 5 {
		t.Errorf("objects = %d, want the 5 pollable ones", len(detail.Objects))
	}
	for _, o := range detail.Objects {
		if !o.Pollable {
			t.Errorf("non-pollable object %s survived the filter", o.Name)
		}
	}
}

func TestImportErrorMessage(t *testing.T) {
	err := &ImportError{Failed: 2, Total: 160}
	if got := err.Error(); got != "2 of 160 MIB file(s) failed to import" {
		t.Errorf("Error() = %q", got)
	}
}

func TestMIBTablesJSON(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/mibs/7/tables", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, map[string]any{"tables": []client.Table{{
			TableOID:  "1.3.6.1.2.1.2.2",
			TableName: "ifTable",
		}}})
	})

	env, out := newFakeEnv(t, mux)
	env.Opts.JSON = true
	if err := runCommand(t, newMIBCommand(env), "tables", "7"); err != nil {
		t.Fatalf("tables --json: %v", err)
	}

	page := decodePage[client.Table](t, out.Bytes())
	if len(page.Items) != 1 || page.Items[0].TableName != "ifTable" {
		t.Errorf("unexpected JSON payload: %+v", page.Items)
	}
}

func TestMIBTableWithoutColumns(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/mibs/7/tables", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, map[string]any{"tables": []client.Table{
			{TableOID: "1.3.6.1.2.1.2.2", TableName: "ifTable", IndexColumn: "ifIndex"},
			{TableOID: "1.3.6.1.2.1.4.20", TableName: "ipAddrTable"},
		}})
	})

	env, out := newFakeEnv(t, mux)
	if err := runCommand(t, newMIBCommand(env), "tables", "7"); err != nil {
		t.Fatalf("tables: %v", err)
	}

	text := out.String()
	if strings.Count(text, "(no matching objects)") != 2 {
		t.Errorf("a column-less table must say so:\n%s", text)
	}
	if !strings.Contains(text, "ipAddrTable") {
		t.Errorf("second table missing:\n%s", text)
	}
}

func TestMIBObjectsJSON(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/mibs/7", mibDetailHandler(t, testMIBDetail(4), nil))

	env, out := newFakeEnv(t, mux)
	env.Opts.JSON = true
	if err := runCommand(t, newMIBCommand(env), "objects", "7", "--search", "ifObject1"); err != nil {
		t.Fatalf("objects --json: %v", err)
	}

	page := decodePage[client.MIBObject](t, out.Bytes())
	if len(page.Items) != 1 || page.Items[0].Name != "ifObject1" {
		t.Errorf("unexpected JSON payload: %+v", page.Items)
	}
	// The document has to keep the totals honest: one of four objects
	// survived --search, and the consumer can see both numbers.
	if page.Total != 4 || page.Matched != 1 || page.Search != "ifObject1" {
		t.Errorf("counts = %+v, want 1 of 4 matched by --search", page.listCounts)
	}
}

func TestMIBSetTagsServerError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /api/mibs/8/tags", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unknown mib", http.StatusNotFound)
	})

	env, _ := newFakeEnv(t, mux)
	err := runCommand(t, newMIBCommand(env), "tags", "8", "--set", "cisco")

	var apiErr *client.APIError
	if !errors.As(err, &apiErr) || !apiErr.NotFound() {
		t.Fatalf("expected a 404 APIError, got %v", err)
	}
}

func TestMIBDeleteServerError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/mibs/7", mibDetailHandler(t, testMIBDetail(1), nil))
	mux.HandleFunc("DELETE /api/mibs/7", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "mib in use", http.StatusConflict)
	})

	env, _ := newFakeEnv(t, mux)
	err := runCommand(t, newMIBCommand(env), "delete", "7", "--force")

	var apiErr *client.APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusConflict {
		t.Fatalf("expected a 409 APIError, got %v", err)
	}
}

func TestMIBListJSONAppliesTagFilter(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/mibs", mibListHandler(t, nil))

	env, out := newFakeEnv(t, mux)
	env.Opts.JSON = true
	if err := runCommand(t, newMIBCommand(env), "list", "--tag", "cisco"); err != nil {
		t.Fatalf("list --json --tag: %v", err)
	}

	page := decodePage[client.MIB](t, out.Bytes())
	if len(page.Items) != 1 || page.Items[0].Name != "CISCO-ENVMON-MIB" {
		t.Errorf("JSON output must honor --tag, got %+v", page.Items)
	}
}

// TestMIBImportRefusesStoredModuleWithoutUpdate checks that re-importing a
// module that is already stored fails with the server's actionable conflict
// rather than silently overwriting it, and that the batch still reports a
// tally instead of aborting.
func TestMIBImportRefusesStoredModuleWithoutUpdate(t *testing.T) {
	var seen []string

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/mibs/import", importHandler(t, &seen, nil))

	env, out := newFakeEnv(t, mux)
	paths := writeMIBFiles(t, "STORED-MIB.mib", "NEW-MIB.mib")

	args := append([]string{"import"}, paths...)
	err := runCommand(t, newMIBCommand(env), args...)

	var importErr *ImportError
	if !errors.As(err, &importErr) {
		t.Fatalf("expected *ImportError, got %v", err)
	}
	if importErr.Failed != 1 || importErr.Total != 2 {
		t.Errorf("tally = %+v, want 1 of 2 failed", importErr)
	}

	text := out.String()
	if !strings.Contains(text, "409") {
		t.Errorf("conflict status not surfaced:\n%s", text)
	}
	if !strings.Contains(text, "already imported as id 9") {
		t.Errorf("conflict message not surfaced:\n%s", text)
	}
	if !strings.Contains(text, "replace=true") {
		t.Errorf("conflict does not point at the update path:\n%s", text)
	}
	// The module that was not stored still had to be attempted.
	if !strings.Contains(text, "imported "+paths[1]+" as NEW-MIB") {
		t.Errorf("second file not imported after the conflict:\n%s", text)
	}
	if !strings.Contains(text, "1 imported, 0 updated, 1 failed") {
		t.Errorf("summary missing:\n%s", text)
	}
}

// TestMIBImportUpdateRefreshesStoredModule checks --update opts into the
// in-place refresh: the upload carries replace=true and the result is reported
// as an update, not a fresh import.
func TestMIBImportUpdateRefreshesStoredModule(t *testing.T) {
	var (
		seen     []string
		replaced []string
	)

	mux := http.NewServeMux()
	handler := importHandler(t, &seen, nil)
	mux.HandleFunc("POST /api/mibs/import", func(w http.ResponseWriter, r *http.Request) {
		replaced = append(replaced, r.URL.Query().Get("replace"))
		handler(w, r)
	})

	env, out := newFakeEnv(t, mux)
	paths := writeMIBFiles(t, "STORED-MIB.mib")

	args := append([]string{"import", "--update"}, paths...)
	if err := runCommand(t, newMIBCommand(env), args...); err != nil {
		t.Fatalf("import --update: %v", err)
	}

	if len(replaced) != 1 || replaced[0] != "true" {
		t.Fatalf("replace query = %v, want [true]", replaced)
	}

	text := out.String()
	if !strings.Contains(text, "updated  "+paths[0]+" as STORED-MIB (id 9, 12 objects)") {
		t.Errorf("refresh not reported as an update:\n%s", text)
	}
	if !strings.Contains(text, "0 imported, 1 updated, 0 failed") {
		t.Errorf("summary missing:\n%s", text)
	}
}

// TestMIBImportJSONReportsPerFileAction checks the machine-readable report
// names what happened to every file, which is how a refresh across a few
// hundred modules is audited.
func TestMIBImportJSONReportsPerFileAction(t *testing.T) {
	var seen []string

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/mibs/import", importHandler(t, &seen, nil))

	env, out := newFakeEnv(t, mux)
	env.Opts.JSON = true
	paths := writeMIBFiles(t, "STORED-MIB.mib", "bad-one.mib")

	args := append([]string{"import", "--update"}, paths...)
	if err := runCommand(t, newMIBCommand(env), args...); err == nil {
		t.Fatal("expected the failing file to set an error exit")
	}

	var report []importReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("decode report: %v\n%s", err, out.String())
	}
	if len(report) != 2 {
		t.Fatalf("got %d entries, want one per file", len(report))
	}
	if report[0].Action != "updated" || report[0].MIB == nil || report[0].MIB.Name != "STORED-MIB" {
		t.Errorf("first entry = %+v, want an updated STORED-MIB", report[0])
	}
	if report[1].Action != "" || report[1].Error == "" {
		t.Errorf("failing entry = %+v, want no action and an error", report[1])
	}
}
