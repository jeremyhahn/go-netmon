package parser

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/sleepinggenius2/gosmi"
)

// ErrUnresolvedImports is matched by every *UnresolvedImportsError, so a
// caller that only needs to know a module loaded degraded can use errors.Is.
var ErrUnresolvedImports = errors.New("mib module has unresolved imports")

// UnresolvedImportsError warns that some IMPORTS were not found, leaving dependent objects untyped; the module still loads.
type UnresolvedImportsError struct {
	// Module is the module that was parsed.
	Module string

	// Missing names the imported modules that could not be loaded.
	Missing []string

	// Untyped names the objects whose type could not be resolved.
	Untyped []string
}

// Error names the module, what it could not import and what that cost.
func (e *UnresolvedImportsError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "mib module %s loaded degraded", e.Module)
	if len(e.Missing) > 0 {
		fmt.Fprintf(&b, ": unresolved imports %s", strings.Join(e.Missing, ", "))
	}
	if len(e.Untyped) > 0 {
		fmt.Fprintf(&b, "; %d objects have no type", len(e.Untyped))
	}
	return b.String()
}

// Is lets errors.Is match ErrUnresolvedImports.
func (e *UnresolvedImportsError) Is(target error) bool {
	return target == ErrUnresolvedImports
}

// Err reports a module that parsed degraded as an *UnresolvedImportsError,
// and nil for one that resolved completely. It is never a reason to refuse
// the module; it is the reason to say it is not whole.
func (r *ParseReport) Err() error {
	if r == nil || (len(r.MissingImports) == 0 && len(r.Untyped) == 0) {
		return nil
	}
	return &UnresolvedImportsError{
		Module:  r.Module,
		Missing: r.MissingImports,
		Untyped: r.Untyped,
	}
}

// missingImports names, sorted and once each, the modules module imports from that are not loaded.
func missingImports(module gosmi.SmiModule) []string {
	seen := make(map[string]bool)
	var missing []string
	for _, imp := range module.GetImports() {
		if imp.Module == "" || seen[imp.Module] {
			continue
		}
		seen[imp.Module] = true
		if _, err := gosmi.GetModule(imp.Module); err != nil {
			missing = append(missing, imp.Module)
		}
	}
	sort.Strings(missing)
	return missing
}
