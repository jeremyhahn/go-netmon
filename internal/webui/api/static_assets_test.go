package api

import (
	"context"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-netmon/internal/webui"
	ws "github.com/jeremyhahn/go-netmon/internal/webui/websocket"
)

// newStaticTestServer builds a server serving the assets compiled into the
// binary, which is what cmd/webui does.
func newStaticTestServer(t *testing.T) *Server {
	t.Helper()

	assets, err := webui.Static()
	if err != nil {
		t.Fatalf("webui.Static(): %v", err)
	}

	s, err := NewServer(Config{
		DBPath: filepath.Join(t.TempDir(), "webui.db"),
		Assets: assets,
		QueryFunc: func(context.Context, string) ([]ws.MetricValue, error) {
			return nil, nil
		},
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// TestServerServesTheUIFromAnyWorkingDirectory is the regression this change
// exists for.
//
// The assets used to be served from the relative path
// "internal/webui/static", so the UI worked only when the process happened to
// be running inside a source checkout. A clean install puts the binary in
// /usr/local/bin, ships no assets and sets no WorkingDirectory, so CWD is "/"
// and every non-API route returned 404 while /api/* kept answering -- which is
// exactly the shape that passes a post-deploy smoke test and fails for the
// operator.
//
// The test runs from an empty directory, so it fails against any
// implementation that reads the assets from disk.
func TestServerServesTheUIFromAnyWorkingDirectory(t *testing.T) {
	t.Chdir(t.TempDir())

	s := newStaticTestServer(t)

	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", http.NoBody))

	if rec.Code != http.StatusOK {
		t.Fatalf("GET / = %d, want 200 (working directory holds no assets)", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(strings.ToLower(body), "<html") {
		t.Errorf("GET / did not return the landing page: %.80q", body)
	}

	// http.FileServer canonicalizes an explicit /index.html to the directory
	// it indexes, so the redirect is the correct answer rather than a fault.
	// It is asserted so that a future change of handler cannot quietly turn
	// a bookmarked /index.html into a 404.
	rec = httptest.NewRecorder()
	s.router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/index.html", http.NoBody))
	if rec.Code != http.StatusMovedPermanently {
		t.Errorf("GET /index.html = %d, want 301 to /", rec.Code)
	}
	if location := rec.Header().Get("Location"); location != "./" && location != "/" {
		t.Errorf("GET /index.html redirected to %q, want the site root", location)
	}
}

// TestServerServesNestedAssetsFromAnyWorkingDirectory checks the same for the
// CSS and JS the page pulls in. A landing page that loads without its assets
// is a blank screen, which is no better than the 404 it replaced.
func TestServerServesNestedAssetsFromAnyWorkingDirectory(t *testing.T) {
	t.Chdir(t.TempDir())

	s := newStaticTestServer(t)
	assets, err := webui.Static()
	if err != nil {
		t.Fatalf("webui.Static(): %v", err)
	}

	// Files at the top of the tree, which the page references directly.
	// favicon.svg is the one a browser fetches without being told to, so it
	// is the asset most likely to be missed by a test that only walks the
	// directories the page links.
	for _, name := range []string{"favicon.svg"} {
		path := "/" + name
		rec := httptest.NewRecorder()
		s.router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, http.NoBody))
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, rec.Code)
		}
		if rec.Body.Len() == 0 {
			t.Errorf("GET %s returned an empty body", path)
		}
	}

	// Ask for whatever the tree actually contains rather than hard-coding
	// filenames a rebuild would change.
	for _, dir := range []string{"css", "js"} {
		entries, err := fsReadDir(assets, dir)
		if err != nil {
			t.Fatalf("reading %s/: %v", dir, err)
		}
		if len(entries) == 0 {
			t.Fatalf("%s/ is empty; the test is not checking anything", dir)
		}

		for _, name := range entries {
			path := "/" + dir + "/" + name
			rec := httptest.NewRecorder()
			s.router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, http.NoBody))
			if rec.Code != http.StatusOK {
				t.Errorf("GET %s = %d, want 200", path, rec.Code)
			}
			if rec.Body.Len() == 0 {
				t.Errorf("GET %s returned an empty body", path)
			}
		}
	}
}

// TestServerWithoutAssetsRegistersNoCatchAll checks that a server built with
// no assets answers 404 rather than falling back to a path on disk.
//
// The fallback is the bug: any default path reintroduces the dependency on
// where the process is running. A server with no assets is a test-only
// configuration, and it should behave like a server with no UI.
func TestServerWithoutAssetsRegistersNoCatchAll(t *testing.T) {
	s, err := NewServer(Config{
		DBPath: filepath.Join(t.TempDir(), "webui.db"),
		QueryFunc: func(context.Context, string) ([]ws.MetricValue, error) {
			return nil, nil
		},
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/index.html", http.NoBody))
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET /index.html = %d with no assets configured, want 404", rec.Code)
	}
}

// TestServerAPIRoutesAreUnaffectedByTheStaticHandler checks that the catch-all
// still sits behind the API routes.
//
// This is the half that never broke and therefore never got noticed: /api/*
// kept answering throughout the outage, which is why every post-deploy check
// passed while the page a person opens returned 404.
func TestServerAPIRoutesAreUnaffectedByTheStaticHandler(t *testing.T) {
	t.Chdir(t.TempDir())

	s := newStaticTestServer(t)

	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/devices", http.NoBody))
	if rec.Code != http.StatusOK {
		t.Errorf("GET /api/devices = %d, want 200", rec.Code)
	}
	if strings.Contains(strings.ToLower(rec.Body.String()), "<html") {
		t.Error("an API route was answered by the static handler")
	}
}

// fsReadDir returns the names of the files directly inside dir.
func fsReadDir(fsys fs.FS, dir string) ([]string, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, err
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	return names, nil
}
