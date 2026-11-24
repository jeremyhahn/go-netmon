package webui

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

// TestStaticServesTheLandingPage checks the one file the UI cannot start
// without.
func TestStaticServesTheLandingPage(t *testing.T) {
	assets, err := Static()
	if err != nil {
		t.Fatalf("Static() returned error: %v", err)
	}

	page, err := fs.ReadFile(assets, "index.html")
	if err != nil {
		t.Fatalf("index.html is not in the binary: %v", err)
	}
	if len(page) == 0 {
		t.Error("index.html is embedded but empty")
	}
}

// TestStaticIsRootedAtTheAssetTree checks index.html is at the root of Static.
func TestStaticIsRootedAtTheAssetTree(t *testing.T) {
	assets, err := Static()
	if err != nil {
		t.Fatalf("Static() returned error: %v", err)
	}

	if _, err := fs.Stat(assets, "static"); err == nil {
		t.Error("the tree still has a \"static\" prefix; fs.Sub was not applied")
	}
	for _, dir := range []string{"css", "js"} {
		if _, err := fs.Stat(assets, dir); err != nil {
			t.Errorf("%s/ missing from the rooted tree: %v", dir, err)
		}
	}
}

// TestStaticEmbedsEveryFileOnDisk checks every source asset file is embedded.
func TestStaticEmbedsEveryFileOnDisk(t *testing.T) {
	assets, err := Static()
	if err != nil {
		t.Fatalf("Static() returned error: %v", err)
	}

	// go test runs with the working directory set to the package source
	// directory, so "static" is the tree the embed directive read.
	const sourceTree = "static"
	onDisk := 0
	walkErr := filepath.WalkDir(sourceTree, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		onDisk++

		rel, relErr := filepath.Rel(sourceTree, path)
		if relErr != nil {
			return relErr
		}
		embedded := filepath.ToSlash(rel)
		if _, statErr := fs.Stat(assets, embedded); statErr != nil {
			t.Errorf("%s is on disk but not in the binary: %v", embedded, statErr)
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walking %s: %v", sourceTree, walkErr)
	}
	if onDisk == 0 {
		t.Fatal("no files found on disk; the test is not checking anything")
	}

	embeddedCount := 0
	if err := fs.WalkDir(assets, ".", func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			embeddedCount++
		}
		return err
	}); err != nil {
		t.Fatalf("walking the embedded tree: %v", err)
	}
	if embeddedCount != onDisk {
		t.Errorf("binary holds %d files, disk holds %d", embeddedCount, onDisk)
	}
}

// TestValidateAssetsRejectsATreeThatCannotServeTheUI checks synthetic broken trees are rejected.
func TestValidateAssetsRejectsATreeThatCannotServeTheUI(t *testing.T) {
	tests := []struct {
		name       string
		assets     fs.FS
		wantReason string
	}{
		{
			name:       "empty tree",
			assets:     fstest.MapFS{},
			wantReason: "is missing",
		},
		{
			name: "other files present but no landing page",
			assets: fstest.MapFS{
				"css/style.css": {Data: []byte("body{}")},
				"js/app.js":     {Data: []byte("//")},
				"favicon.svg":   {Data: []byte("<svg/>")},
			},
			wantReason: "is missing",
		},
		{
			name: "landing page present but empty",
			assets: fstest.MapFS{
				"index.html":    {Data: []byte{}},
				"css/style.css": {Data: []byte("body{}")},
			},
			wantReason: "is empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateAssets(tt.assets)
			if err == nil {
				t.Fatal("validateAssets() accepted a tree that cannot serve the UI")
			}

			var assetErr *AssetError
			if !errors.As(err, &assetErr) {
				t.Fatalf("error = %T (%v), want *AssetError", err, err)
			}
			if assetErr.Path != "index.html" {
				t.Errorf("AssetError.Path = %q, want %q", assetErr.Path, "index.html")
			}
			if assetErr.Reason != tt.wantReason {
				t.Errorf("AssetError.Reason = %q, want %q", assetErr.Reason, tt.wantReason)
			}
			if !strings.Contains(err.Error(), "index.html") {
				t.Errorf("Error() = %q, does not name the missing asset", err.Error())
			}
		})
	}
}

// TestValidateAssetsAcceptsAServiceableTree checks the other side, so the
// validation cannot be satisfied by rejecting everything.
func TestValidateAssetsAcceptsAServiceableTree(t *testing.T) {
	assets := fstest.MapFS{
		"index.html": {Data: []byte("<html></html>")},
	}
	if err := validateAssets(assets); err != nil {
		t.Errorf("validateAssets() rejected a serviceable tree: %v", err)
	}
}

// TestStaticValidatesTheRealTree checks that the embedded assets pass their own
// check, so the validation is wired into Static and not merely defined.
func TestStaticValidatesTheRealTree(t *testing.T) {
	if _, err := Static(); err != nil {
		t.Fatalf("Static() rejected the embedded tree: %v", err)
	}
}
