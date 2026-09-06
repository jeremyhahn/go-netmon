package loader

import (
	"testing"

	"github.com/jeremyhahn/go-netmon/internal/mib"
)

// A load with no reporter installed is the default and must stay silent rather
// than panic on a nil function — every caller but the service binary is in that
// case.
func TestLoadWithoutAProgressReporter(t *testing.T) {
	l, _ := newLoader(t, []string{"SNMPv2-MIB"})

	if err := l.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
}

// The reporter is what an operator sees while the service cannot answer, so
// what matters is that it is told about every module, in order, with a total
// that lets "42" mean something.
func TestLoadReportsProgressForEveryModule(t *testing.T) {
	autoLoad := []string{"SNMPv2-MIB", "IF-MIB"}
	l, _ := newLoader(t, autoLoad)

	type step struct {
		done, total int
		module      string
	}
	var steps []step
	l.SetProgress(func(done, total int, module string) {
		steps = append(steps, step{done, total, module})
	})

	if err := l.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	if len(steps) != len(autoLoad) {
		t.Fatalf("progress reported %d times, want %d", len(steps), len(autoLoad))
	}
	for i, got := range steps {
		if got.done != i+1 {
			t.Errorf("step %d reported done=%d, want %d", i, got.done, i+1)
		}
		if got.total != len(autoLoad) {
			t.Errorf("step %d reported total=%d, want %d", i, got.total, len(autoLoad))
		}
		if got.module != autoLoad[i] {
			t.Errorf("step %d reported module %q, want %q", i, got.module, autoLoad[i])
		}
	}
}

// A module that will not parse must still be counted. A progress line that
// stalls on the one broken module in a set of 176 reads exactly like the wedge
// this reporting exists to rule out.
func TestLoadReportsProgressThroughAModuleThatFails(t *testing.T) {
	autoLoad := []string{"NO-SUCH-MIB", "SNMPv2-MIB"}
	l, _ := newLoader(t, autoLoad)

	var reported []string
	l.SetProgress(func(_, _ int, module string) {
		reported = append(reported, module)
	})

	if err := l.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	if len(reported) != 2 {
		t.Fatalf("progress reported %v, want both modules including the one that failed", reported)
	}
	if reported[0] != "NO-SUCH-MIB" {
		t.Errorf("the failing module was not reported: got %q", reported[0])
	}
}

// SetProgress(nil) puts the loader back to reporting nowhere.
func TestSetProgressNilSilencesReporting(t *testing.T) {
	l, _ := newLoader(t, []string{"SNMPv2-MIB"})

	called := false
	l.SetProgress(func(int, int, string) { called = true })
	l.SetProgress(mib.LoadProgress(nil))

	if err := l.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if called {
		t.Error("a cleared reporter was still called")
	}
}
