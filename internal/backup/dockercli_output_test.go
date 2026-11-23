package backup

import (
	"context"
	"errors"
	"testing"
)

// What VolumeBytes does when docker answers with nothing.
//
// The failure this covers is not hypothetical: "docker run ... du -sb" exiting
// 0 with empty stdout took down a CI run, and the panic was one line ABOVE the
// DockerOutputError meant to report it -- so the typed error was unreachable
// for exactly the case its Reason names.
func TestVolumeBytesTreatsAnUnanswerableReplyAsAnError(t *testing.T) {
	cases := []struct {
		name string
		out  string
	}{
		{"empty", ""},
		{"spaces", "   "},
		{"newline", "\n"},
		{"tab", "\t"},
		{"tab then path, no count", "\t/src"},
		{"not a number", "abc\t/src"},
		// Measured against the old idiom: strings.Fields(field+" ")[0] took
		// the first whitespace-separated token and returned 12 with a nil
		// error, silently discarding the rest. "du -sb" does not emit this,
		// so it was never a live defect -- but a length check on the old form
		// would have preserved the silent acceptance, and deleting Fields
		// closes it. That is the strongest form of the argument for removing
		// the idiom rather than guarding it.
		{"count with an embedded space", "12 34\t/src"},
	}

	// Counted OUTSIDE the subtest, which is load-bearing rather than stylistic.
	// Two tests in this package call t.Parallel(), so it is the local habit; a
	// t.Parallel() added here with the increment inside the closure would make
	// t.Run return before the body ran, so the check below would read 0 and
	// fail a passing suite -- and the increment would race under -race.
	//
	// This does NOT catch a panic: a panic takes the whole binary down and
	// leaves no parent to assert. It catches the quieter version, a row that
	// never runs because a later edit filters the table. Worth stating because
	// the panic taught the lesson -- it aborted on row one, so the five rows
	// after it never executed and the output read as one FAIL rather than
	// "five cases unexecuted".
	executed := 0

	for _, tc := range cases {
		executed++
		t.Run(tc.name, func(t *testing.T) {
			c := cliWith(t, &fakeExec{stdout: map[string]string{"du": tc.out}})

			size, err := c.VolumeBytes(context.Background(), "vol")

			var outErr *DockerOutputError
			if !errors.As(err, &outErr) {
				t.Fatalf("error = %T (%v), want *DockerOutputError", err, err)
			}
			if outErr.Reason != "expected a byte count" {
				t.Errorf("reason = %q, want the byte-count reason", outErr.Reason)
			}
			// The output is carried so an operator can see what docker said;
			// for the empty cases that is the point -- "it said nothing" is
			// the diagnosis.
			if outErr.Output != tc.out {
				t.Errorf("output = %q, want the reply verbatim %q", outErr.Output, tc.out)
			}
			if size != 0 {
				t.Errorf("size = %d, want 0 alongside an error", size)
			}
		})
	}

	if executed != len(cases) {
		t.Errorf("%d of %d cases ran; the rest were skipped silently", executed, len(cases))
	}
}

// The output SHAPES a real helper produces, which the table above does not
// reach.
//
// The plain "<count>\t<path>" case and the argument vector belong to
// TestVolumeBytesReadsTheHelpersAnswer and are deliberately not repeated here.
// An earlier version of this comment claimed the rows below were what stopped
// the error table being satisfied by a VolumeBytes that always errors -- that
// was already untrue when written, because the vector test covers the happy
// path. These rows earn their place on the variants it does not have.
func TestVolumeBytesParsesARealAnswer(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want int64
	}{
		{"trailing newline", "4096\t/src\n", 4096},
		{"zero", "0\t/src", 0},
		{"no path", "512", 512},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := cliWith(t, &fakeExec{stdout: map[string]string{"du": tc.out}})

			size, err := c.VolumeBytes(context.Background(), "vol")
			if err != nil {
				t.Fatalf("VolumeBytes(%q): %v", tc.out, err)
			}
			if size != tc.want {
				t.Errorf("size = %d, want %d", size, tc.want)
			}
		})
	}
}
