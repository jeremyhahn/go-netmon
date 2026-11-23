package storage

import (
	"slices"
	"strings"
)

// sortByName orders records by name.
//
// Every list this package returns goes through it, for two reasons. The first
// is correctness: the device and profile MIB lists are assembled through a map,
// so without it they come back in Go's randomized map order — four consecutive
// requests gave four different orders, and the web UI builds its MIB tabs
// straight from that array, so the dashboard's tabs and the tab that opens by
// default changed on every page load. The second is consistency: the lists that
// come off a Pebble prefix scan are stable but ordered by record ID, which is
// import order, and an operator scanning a catalog of 169 modules for one name
// should not have to know that.
//
// Names are unique per record kind — the store keeps a name index to enforce it
// — so this is a total order and no tie-break is needed.
func sortByName[T any](records []T, name func(T) string) {
	slices.SortFunc(records, func(a, b T) int {
		return strings.Compare(name(a), name(b))
	})
}

// mibName, deviceName and profileName name each record kind for sortByName.
func mibName(m MIB) string { return m.Name }

func deviceName(d Device) string { return d.Name }

func profileName(p DeviceProfile) string { return p.Name }
