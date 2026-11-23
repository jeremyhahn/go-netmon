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

// runVersionFlag runs "netmon-backup --version" and returns what it printed.
func runVersionFlag(t *testing.T) string {
	t.Helper()
	root := newRootCommand()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"--version"})
	if err := root.Execute(); err != nil {
		t.Fatalf("--version: %v", err)
	}
	return out.String()
}

func TestVersionFlagPrintsStampedValues(t *testing.T) {
	stampBuild(t, "1.4.0", "9f3c1ab", "2026-08-08T10:04:00Z")

	got := runVersionFlag(t)

	want := "1.4.0 (commit 9f3c1ab, built 2026-08-08T10:04:00Z)"
	if !strings.Contains(got, want) {
		t.Errorf("--version = %q, want it to contain %q", got, want)
	}
}

func TestVersionFlagUnstampedDefaults(t *testing.T) {
	got := runVersionFlag(t)

	want := "dev (commit unknown, built unknown)"
	if !strings.Contains(got, want) {
		t.Errorf("--version = %q, want it to contain %q", got, want)
	}
}
