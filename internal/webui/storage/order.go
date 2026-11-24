package storage

import (
	"slices"
	"strings"
)

// sortByName orders records by their unique name; every list this package returns goes through it.
func sortByName[T any](records []T, name func(T) string) {
	slices.SortFunc(records, func(a, b T) int {
		return strings.Compare(name(a), name(b))
	})
}

// mibName, deviceName and profileName name each record kind for sortByName.
func mibName(m MIB) string { return m.Name }

func deviceName(d Device) string { return d.Name }

func profileName(p DeviceProfile) string { return p.Name }
