package backup

import (
	"context"
	"errors"
	"testing"
)

// VolumeBytes must return a typed error, not panic, when docker answers with nothing.
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
		// Trailing tokens after the count must be rejected, not discarded.
		{"count with an embedded space", "12 34\t/src"},
	}

	// Counted outside the subtest so a later t.Parallel() cannot make the check read 0; catches rows that never run.
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

// Output shapes a real helper produces that the table above does not cover.
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
