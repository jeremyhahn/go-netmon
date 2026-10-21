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

// UnresolvedImportsError says a module parsed, but not completely: some of
// the modules its IMPORTS name could not be found, and the objects that take
// their type from one of them came out with no type.
//
// It is a warning, not a load failure. The module's objects are still
// returned and still pollable by OID; what they lose is everything read from
// the type -- Type, Units, DisplayHint and EnumValues -- so an enumerated
// status polls as a bare integer. Getting this as a value, rather than a
// module that silently looks fine, is the point (#62).
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

// missingImports names the modules module imports from that are not loaded.
//
// gosmi loads a module's imports with it and carries on when one cannot be
// found, so a module that is absent from every search path is simply not
// there afterwards. Each name appears once, sorted.
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
