package backup

import (
	"strings"
	"testing"
)

// The ownership check is what stands between this tool and someone else's
// containers, and it is built entirely out of reading docker's output. Both
// tests here cover ways that reading was wrong against a real deployment while
// failing in a way that looked like something else.

// TestConfigFilesLabelIsTheOneComposeSets pins the label name.
//
// Compose writes "com.docker.compose.project.config_files". The tool asked for
// "com.docker.compose.config-files", which does not exist; docker answers an
// absent label with an empty string rather than an error, so every container in
// the project reported no compose file and the ownership check was deciding on
// data it never actually had.
func TestConfigFilesLabelIsTheOneComposeSets(t *testing.T) {
	t.Parallel()

	const want = "com.docker.compose.project.config_files"
	if ConfigFilesLabel != want {
		t.Errorf("ConfigFilesLabel = %q, want %q", ConfigFilesLabel, want)
	}
	if strings.Contains(ConfigFilesLabel, "config-files") {
		t.Error("the hyphenated spelling is not a label compose sets; it always reads back empty")
	}
}

// TestRowsKeepsAnEmptyTrailingField covers the parse that turned a container
// with no config-files label into unparseable output.
//
// docker prints "name\t" for a row whose last field is empty. lines trims each
// line, which removes that tab, and the two-field split then failed with
// "cannot interpret the output of docker ps" on output that was perfectly well
// formed.
func TestRowsKeepsAnEmptyTrailingField(t *testing.T) {
	t.Parallel()

	out := "go-netmon\t\ngrafana\t/srv/other.yml\n"

	got := rows(out)
	if len(got) != 2 {
		t.Fatalf("rows returned %d rows, want 2: %q", len(got), got)
	}

	name, configFiles, ok := strings.Cut(got[0], "\t")
	if !ok {
		t.Fatalf("the row for a container with no label lost its separator: %q", got[0])
	}
	if name != "go-netmon" || configFiles != "" {
		t.Errorf("row 0 = (%q, %q), want (%q, %q)", name, configFiles, "go-netmon", "")
	}

	name, configFiles, ok = strings.Cut(got[1], "\t")
	if !ok || name != "grafana" || configFiles != "/srv/other.yml" {
		t.Errorf("row 1 = (%q, %q, %v), want (%q, %q, true)",
			name, configFiles, ok, "grafana", "/srv/other.yml")
	}
}

// TestRowsIgnoresBlankLinesAndCarriageReturns covers the shapes real command
// output arrives in: a trailing newline, and \r\n line endings.
func TestRowsIgnoresBlankLinesAndCarriageReturns(t *testing.T) {
	t.Parallel()

	if got := rows(""); len(got) != 0 {
		t.Errorf("rows(\"\") = %q, want no rows", got)
	}
	if got := rows("\n\n  \n"); len(got) != 0 {
		t.Errorf("rows of only blank lines = %q, want no rows", got)
	}

	got := rows("a\tb\r\nc\t\r\n")
	if len(got) != 2 {
		t.Fatalf("rows = %q, want 2 rows", got)
	}
	if got[0] != "a\tb" {
		t.Errorf("row 0 = %q, want %q", got[0], "a\tb")
	}
	if got[1] != "c\t" {
		t.Errorf("row 1 = %q, want the tab kept: %q", got[1], "c\t")
	}
}
