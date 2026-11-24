// Package search defines what searching MIB records means, once.
//
// The CLI documents --search as matching an object's name, OID or description,
// and a module's name, description or tags. The API now accepts the same search
// as a query parameter so a client does not have to download a module to filter
// it. Those are the same search, and the moment they are written twice they
// start to differ: one side trims, the other folds a different way, and an
// operator who searched from the CLI and from the browser gets two answers about
// the same store.
//
// This package is that single definition. Both sides call it, and
// internal/cli/search_contract_test.go pins the CLI's filtering to it, so a
// change here moves both or fails the build.
package search

import "strings"

// Fold normalizes a raw search term for matching.
//
// It is the one place case-insensitivity is decided. It deliberately does not
// trim: a term made of spaces is a search for a space, and silently turning it
// into "match everything" would hide rows rather than reveal them. A folded
// term that is empty selects everything, which is how "no --search" is spelled.
func Fold(term string) string {
	return strings.ToLower(term)
}

// Contains reports whether any of values contains needle, which the caller has
// already passed through Fold. An empty needle matches everything, including a
// record with no text at all.
func Contains(needle string, values ...string) bool {
	if needle == "" {
		return true
	}
	for _, v := range values {
		if strings.Contains(strings.ToLower(v), needle) {
			return true
		}
	}
	return false
}

// Object reports whether a MIB object matches needle: its name, OID or
// description contains it, ignoring case. needle must come from Fold.
//
// The fields are taken one by one rather than as a struct because the two
// callers hold an object in two representations — the server's stored
// mib.OIDMetadata and the client's wire MIBObject — and neither should have to
// build the other's just to run a search.
func Object(needle, name, oid, description string) bool {
	return Contains(needle, name, oid, description)
}

// Module reports whether a MIB module matches needle: its name, description or
// any of its tags contains it, ignoring case. needle must come from Fold.
//
// Tags are searched because tagging is how modules are grouped by vendor, and
// "find me the Cisco modules" is the search an operator actually runs.
func Module(needle, name, description string, tags []string) bool {
	return Contains(needle, name, description) || Contains(needle, tags...)
}

// HasTag reports whether tags carries tag, ignoring case. An empty tag matches
// every module, which is how "no tag filter" is spelled.
func HasTag(tags []string, tag string) bool {
	if tag == "" {
		return true
	}
	for _, t := range tags {
		if strings.EqualFold(t, tag) {
			return true
		}
	}
	return false
}
