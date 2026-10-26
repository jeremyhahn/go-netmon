package metrics

import (
	"sync"
	"sync/atomic"
)

// The friendly-name cache holds current inventory, not history (#54).
//
// A status column -- ifName, ifDescr, entPhysicalName -- names the rows of
// its table, and the other columns of a row are labeled with that name. The
// names are cached because a sample of one column is converted without the
// others in hand. What the cache must not do is outlive what it caches: it
// used to be one map for every device, written on every poll and never
// deleted from, so it held every device and row index ever seen, and a row
// whose index was reused by a different interface kept the old interface's
// name until something overwrote it -- a label that lied.
//
// So the cache now holds exactly what each device's last poll said:
//
//   - a poll replaces, for its device, the names of every table it walked. A
//     row that disappeared from a table loses its name, and a reused index
//     carries only the name the new row reported;
//   - a device the poller retires loses its names (ForgetDevice, called by
//     the poller's RemoveDevice once nothing more can be published for it).
//
// State is per device. A device's names are one immutable value published by
// atomic pointer, so a reader never blocks; writers of one device serialize
// on that device's own mutex, so a poll of one device never waits for a poll
// of another, and there is no retry loop to bound.

// friendlyRow is one status column of one row: the row's index value
// ("5", "1.3.6") and the column's label name ("ifname").
type friendlyRow struct {
	index string
	label string
}

// friendlyNames is one device's names, by table entry OID then row. It is
// immutable once published.
type friendlyNames map[string]map[friendlyRow]string

// deviceFriendlyNames is one device's slot in the cache.
type deviceFriendlyNames struct {
	// writeMu serializes polls of this device, so two of its polls cannot
	// each publish a copy missing the other's tables.
	writeMu sync.Mutex
	names   atomic.Pointer[friendlyNames]
}

// deviceNames returns the device's slot, creating it on its first poll.
func (c *TableIndexCache) deviceNames(deviceName string) *deviceFriendlyNames {
	if slot, ok := c.friendly.Load(deviceName); ok {
		return slot.(*deviceFriendlyNames)
	}
	slot, _ := c.friendly.LoadOrStore(deviceName, &deviceFriendlyNames{})
	return slot.(*deviceFriendlyNames)
}

// publishFriendlyNames replaces the device's names for every table in polled
// with the poll's names, keeping the names of tables the poll did not walk.
func (c *TableIndexCache) publishFriendlyNames(deviceName string, polled friendlyNames) {
	if len(polled) == 0 {
		return
	}
	slot := c.deviceNames(deviceName)
	slot.writeMu.Lock()
	defer slot.writeMu.Unlock()

	next := make(friendlyNames, len(polled))
	if current := slot.names.Load(); current != nil {
		for entryOID, rows := range *current {
			next[entryOID] = rows
		}
	}
	for entryOID, rows := range polled {
		next[entryOID] = rows
	}
	slot.names.Store(&next)
}

// currentFriendlyNames returns the names the device's last poll published,
// or nil if it has none. The result must not be modified.
func (c *TableIndexCache) currentFriendlyNames(deviceName string) friendlyNames {
	slot, ok := c.friendly.Load(deviceName)
	if !ok {
		return nil
	}
	names := slot.(*deviceFriendlyNames).names.Load()
	if names == nil {
		return nil
	}
	return *names
}

// friendlyName returns one cached name.
func (c *TableIndexCache) friendlyName(deviceName, entryOID, index, label string) (string, bool) {
	name, ok := c.currentFriendlyNames(deviceName)[entryOID][friendlyRow{index: index, label: label}]
	return name, ok
}

// ForgetDevice drops every name cached for the device. The poller calls it
// when it retires the device, after the device's last commit, so no poll can
// republish what this removes.
func (c *TableIndexCache) ForgetDevice(deviceName string) {
	c.friendly.Delete(deviceName)
}
