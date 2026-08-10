package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"
)

// renderFunc produces one frame of a command's output.
type renderFunc func(ctx context.Context, out io.Writer) error

// runWithRefresh prints a command's output once, or repeatedly when --refresh
// is set, clearing the screen between frames. It mirrors the refresh behavior
// of the sysmon CLI so the two tools feel the same.
//
// The interval is measured from the start of each frame, so a slow API call
// does not make the effective period drift.
func (e *Env) runWithRefresh(render renderFunc) error {
	if e.Opts.Refresh <= 0 {
		ctx, cancel := context.WithTimeout(context.Background(), e.Opts.Timeout)
		defer cancel()
		return render(ctx, e.Out)
	}

	interval := time.Duration(e.Opts.Refresh * float64(time.Second))
	if interval < minRefreshInterval {
		interval = minRefreshInterval
	}

	// The signal context is indirected through a package variable so a test can
	// drive the loop to completion instead of leaking a goroutine that keeps
	// writing after the assertions run.
	ctx, stop := contextWithSignals()
	defer stop()

	// Under --json the frames are a stream a program reads, so the screen
	// control and the footer are suppressed: one JSON document per frame, and
	// errors go to stderr where they cannot corrupt it.
	stream := e.Opts.JSON

	for {
		start := time.Now()

		if !stream {
			// Clear the screen and home the cursor. A write failure means the
			// output stream is gone, so stop rather than spin on a dead pipe.
			if _, err := io.WriteString(e.Out, "\033[H\033[2J"); err != nil {
				return err
			}
		}

		frameCtx, cancel := context.WithTimeout(ctx, e.Opts.Timeout)
		err := render(frameCtx, e.Out)
		cancel()
		if err != nil {
			// A refreshing view should survive a transient API blip rather
			// than exiting and losing the session.
			if stream {
				fmt.Fprintf(e.ErrOut, "error: %v\n", err)
			} else {
				fmt.Fprintf(e.Out, "\n error: %v\n", err)
			}
		}
		if !stream {
			fmt.Fprintf(e.Out, "\n refreshing every %s — press Ctrl-C to stop\n", interval)
		}

		remaining := interval - time.Since(start)
		if remaining <= 0 {
			remaining = time.Millisecond
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(remaining):
		}
	}
}

// minRefreshInterval keeps a stray "--refresh 0.001" from hammering the API.
const minRefreshInterval = 100 * time.Millisecond

// writeJSON emits v as indented JSON.
func writeJSON(out io.Writer, v any) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// table accumulates rows and writes them as aligned columns.
type table struct {
	writer *tabwriter.Writer
}

// newTable starts a table with the given column headers.
func newTable(out io.Writer, headers ...string) *table {
	t := &table{writer: tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)}
	fmt.Fprintln(t.writer, strings.Join(headers, "\t"))
	return t
}

// Row appends a row. Values are rendered with %v.
func (t *table) Row(values ...any) {
	cells := make([]string, len(values))
	for i, v := range values {
		cells[i] = fmt.Sprintf("%v", v)
	}
	fmt.Fprintln(t.writer, strings.Join(cells, "\t"))
}

// Flush writes the accumulated rows.
func (t *table) Flush() error {
	return t.writer.Flush()
}

// keyValues prints a set of labeled fields, aligned on the labels.
func keyValues(out io.Writer, pairs ...[2]string) error {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	for _, p := range pairs {
		fmt.Fprintf(w, "%s:\t%s\n", p[0], p[1])
	}
	return w.Flush()
}

// truncate shortens s for table display, marking that it was cut.
func truncate(s string, width int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.TrimSpace(s)
	if width <= 3 || len(s) <= width {
		return s
	}
	return s[:width-3] + "..."
}

// yesNo renders a boolean for a table cell.
func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// dash returns s, or "-" when s is empty, so table columns never look blank.
func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// untilTime renders how long there is to wait until t, which is what a
// scheduled retry needs: relativeTime would render a time in the future as a
// negative age. The result is a bare value, suited to a table cell.
func untilTime(t time.Time) string {
	if t.IsZero() {
		return "unscheduled"
	}
	d := time.Until(t)
	if d <= 0 {
		return "now"
	}
	return d.Truncate(time.Second).String()
}

// untilPhrase renders the same wait as a fragment that reads correctly inside a
// sentence. A caller writing "retrying " + untilTime(t) produces "retrying in
// now" once the retry is due, because only the duration case wants the
// preposition.
func untilPhrase(t time.Time) string {
	if t.IsZero() {
		return "at the next poll"
	}
	if time.Until(t) <= 0 {
		return "now"
	}
	return "in " + untilTime(t)
}

// relativeTime renders a timestamp as an age, which is what an operator
// scanning a status table actually wants to know.

func relativeTime(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
