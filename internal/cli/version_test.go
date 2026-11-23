package cli

import (
	"net/http"
	"strings"
	"testing"
)

// testBuildInfo stands in for the values the linker stamps in.
func testBuildInfo() buildInfo {
	return newBuildInfo("1.4.0", "9f3c1ab", "2026-08-08T10:04:00Z")
}

func TestVersionCommandPrintsBuildMetadata(t *testing.T) {
	env, out := newFakeEnv(t, http.NotFoundHandler())

	if err := runCommand(t, newVersionCommand(env, testBuildInfo())); err != nil {
		t.Fatalf("version: %v", err)
	}

	text := out.String()
	for _, want := range []string{"1.4.0", "9f3c1ab", "2026-08-08T10:04:00Z", "go1."} {
		if !strings.Contains(text, want) {
			t.Errorf("output missing %q:\n%s", want, text)
		}
	}
}

func TestVersionCommandJSON(t *testing.T) {
	env, out := newFakeEnv(t, http.NotFoundHandler())
	env.Opts.JSON = true

	if err := runCommand(t, newVersionCommand(env, testBuildInfo())); err != nil {
		t.Fatalf("version --json: %v", err)
	}

	got := decodeJSON[buildInfo](t, out.String())
	if got.Version != "1.4.0" || got.Commit != "9f3c1ab" || got.BuildDate != "2026-08-08T10:04:00Z" {
		t.Errorf("build info = %+v, want the stamped values", got)
	}
	if got.Go == "" || got.Platform == "" {
		t.Errorf("build info = %+v, want the toolchain and platform filled in", got)
	}
}

// TestVersionCommandRejectsArguments checks the guard against a typo like
// "netmon version device", which must not silently print the version.
func TestVersionCommandRejectsArguments(t *testing.T) {
	env, out := newFakeEnv(t, http.NotFoundHandler())

	if err := runCommand(t, newVersionCommand(env, testBuildInfo()), "extra"); err == nil {
		t.Fatalf("expected an error for an unexpected argument, got output:\n%s", out.String())
	}
}

// TestRootCommandRegistersTopLevelCommands guards the wiring: a command that is
// built but never added is invisible, and cobra's own completion command must
// not shadow netmon's.
func TestRootCommandRegistersTopLevelCommands(t *testing.T) {
	root := NewRootCommand("1.4.0", "9f3c1ab", "2026-08-08T10:04:00Z")

	seen := map[string]int{}
	for _, cmd := range root.Commands() {
		seen[cmd.Name()]++
	}

	for _, want := range []string{
		"device", "profile", "mib", "metrics", "monitor", "poller",
		"health", "version", "completion",
	} {
		if seen[want] != 1 {
			t.Errorf("%q is registered %d times, want exactly once", want, seen[want])
		}
	}

	if !strings.Contains(root.Version, "1.4.0") {
		t.Errorf("--version reports %q, want the stamped version", root.Version)
	}
}
