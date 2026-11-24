package metrics

import (
	"sync"
	"sync/atomic"
)

// The friendly-name cache holds each device's last poll: a poll replaces the names of the tables it walked, ForgetDevice drops a retired device; per-device atomic snapshots.

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
