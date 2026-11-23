package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// withCancellableRefresh replaces the loop's signal context with one the test
// controls, and returns the cancel func that stops the loop. Without this a
// refresh test leaks a goroutine that keeps writing to the buffer the
// assertions read, which the race detector correctly flags.
func withCancellableRefresh(t *testing.T) context.CancelFunc {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	original := contextWithSignals
	contextWithSignals = func() (context.Context, context.CancelFunc) {
		return ctx, func() {}
	}
	t.Cleanup(func() {
		contextWithSignals = original
		cancel()
	})
	return cancel
}

// runFrames drives a refreshing render until it has produced n frames, then
// stops the loop and waits for it to return.
func runFrames(t *testing.T, env *Env, n int, render func(io.Writer) error) {
	t.Helper()

	cancel := withCancellableRefresh(t)

	frames := make(chan struct{}, n+8)
	done := make(chan error, 1)
	go func() {
		done <- env.runWithRefresh(func(_ context.Context, w io.Writer) error {
			err := render(w)
			select {
			case frames <- struct{}{}:
			default:
			}
			return err
		})
	}()

	for i := 0; i < n; i++ {
		select {
		case <-frames:
		case <-time.After(5 * time.Second):
			cancel()
			t.Fatalf("only %d of %d frames rendered", i, n)
		}
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runWithRefresh: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("refresh loop did not stop when its context was canceled")
	}
}

// TestRunWithRefreshOneShot verifies the default path renders exactly once and
// adds no refresh chrome.
func TestRunWithRefreshOneShot(t *testing.T) {
	out := &bytes.Buffer{}
	env := &Env{Opts: &Options{Timeout: time.Second}, Out: out, ErrOut: io.Discard}

	calls := 0
	err := env.runWithRefresh(func(_ context.Context, w io.Writer) error {
		calls++
		_, writeErr := io.WriteString(w, "frame\n")
		return writeErr
	})
	if err != nil {
		t.Fatalf("runWithRefresh: %v", err)
	}

	if calls != 1 {
		t.Errorf("rendered %d times, want 1", calls)
	}
	if got := out.String(); got != "frame\n" {
		t.Errorf("output = %q, want just the frame", got)
	}
}

func TestRunWithRefreshPropagatesError(t *testing.T) {
	env := &Env{Opts: &Options{Timeout: time.Second}, Out: io.Discard, ErrOut: io.Discard}
	want := errors.New("api unreachable")

	err := env.runWithRefresh(func(_ context.Context, _ io.Writer) error { return want })
	if !errors.Is(err, want) {
		t.Errorf("got %v, want the render error", err)
	}
}

// TestRunWithRefreshJSONStreamIsParseable is the regression for a stream a
// program could not read: with --refresh the loop wrote a screen-clear escape
// and a "refreshing every Ns" footer between documents, so piping --json
// --refresh into a parser produced garbage.
func TestRunWithRefreshJSONStreamIsParseable(t *testing.T) {
	out := &bytes.Buffer{}
	env := &Env{
		Opts:   &Options{Timeout: time.Second, JSON: true, Refresh: 0.1},
		Out:    out,
		ErrOut: io.Discard,
	}

	frame := 0
	runFrames(t, env, 3, func(w io.Writer) error {
		frame++
		return writeJSON(w, map[string]int{"frame": frame})
	})

	got := out.String()
	if strings.Contains(got, "\033[") {
		t.Error("JSON stream contains a terminal escape sequence")
	}
	if strings.Contains(got, "refreshing every") {
		t.Error("JSON stream contains the human-facing refresh footer")
	}

	// Every frame must decode as its own document.
	dec := json.NewDecoder(strings.NewReader(got))
	decoded := 0
	for {
		var f map[string]int
		if err := dec.Decode(&f); err != nil {
			if err == io.EOF {
				break
			}
			t.Fatalf("frame %d did not decode: %v", decoded+1, err)
		}
		decoded++
	}
	if decoded < 3 {
		t.Errorf("decoded %d documents, want at least 3", decoded)
	}
}

// TestRunWithRefreshJSONErrorsGoToStderr keeps a transient failure from
// appearing inside the document stream.
func TestRunWithRefreshJSONErrorsGoToStderr(t *testing.T) {
	out := &bytes.Buffer{}
	errOut := &bytes.Buffer{}
	env := &Env{
		Opts:   &Options{Timeout: time.Second, JSON: true, Refresh: 0.1},
		Out:    out,
		ErrOut: errOut,
	}

	runFrames(t, env, 2, func(_ io.Writer) error {
		return errors.New("api unreachable")
	})

	if strings.Contains(out.String(), "api unreachable") {
		t.Errorf("error leaked into the JSON stream: %q", out.String())
	}
	if !strings.Contains(errOut.String(), "api unreachable") {
		t.Errorf("error was not reported on stderr: %q", errOut.String())
	}
}

// TestRunWithRefreshHumanKeepsItsChrome guards the interactive behavior the
// JSON change must not disturb.
func TestRunWithRefreshHumanKeepsItsChrome(t *testing.T) {
	out := &bytes.Buffer{}
	env := &Env{
		Opts:   &Options{Timeout: time.Second, Refresh: 0.1},
		Out:    out,
		ErrOut: io.Discard,
	}

	runFrames(t, env, 2, func(w io.Writer) error {
		_, err := io.WriteString(w, "frame\n")
		return err
	})

	got := out.String()
	if !strings.Contains(got, "\033[H\033[2J") {
		t.Error("interactive refresh should clear the screen between frames")
	}
	if !strings.Contains(got, "refreshing every") {
		t.Error("interactive refresh should tell the user how to stop")
	}
}
