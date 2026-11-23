package main

import (
	"bytes"
	"strings"
	"testing"
)

// stampBuild sets the linker-stamped variables for one test, as
// "-ldflags -X main.version=..." would, and restores them afterwards.
func stampBuild(t *testing.T, v, commit, date string) {
	t.Helper()
	oldVersion, oldCommit, oldDate := version, gitCommit, buildDate
	version, gitCommit, buildDate = v, commit, date
	t.Cleanup(func() { version, gitCommit, buildDate = oldVersion, oldCommit, oldDate })
}

func TestWriteVersionPrintsStampedValues(t *testing.T) {
	stampBuild(t, "1.4.0", "9f3c1ab", "2026-08-08T10:04:00Z")

	var out bytes.Buffer
	if err := writeVersion(&out); err != nil {
		t.Fatalf("writeVersion: %v", err)
	}

	want := "mib-importer 1.4.0 (commit 9f3c1ab, built 2026-08-08T10:04:00Z)\n"
	if out.String() != want {
		t.Errorf("writeVersion = %q, want %q", out.String(), want)
	}
}

func TestWriteVersionUnstampedDefaults(t *testing.T) {
	var out bytes.Buffer
	if err := writeVersion(&out); err != nil {
		t.Fatalf("writeVersion: %v", err)
	}

	for _, want := range []string{"dev", "commit unknown", "built unknown"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("writeVersion = %q, want it to contain %q", out.String(), want)
		}
	}
}
