// Package search is the single definition of MIB record search, shared by the CLI and the API.
package search

import "strings"

// Fold lowercases a search term without trimming; an empty result matches everything.
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

// Object reports whether a MIB object's name, OID or description contains needle (from Fold).
func Object(needle, name, oid, description string) bool {
	return Contains(needle, name, oid, description)
}

// Module reports whether a MIB module's name, description or tags contain needle (from Fold).
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
