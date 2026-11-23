package storage

// EnabledOf reports whether d is to be polled. A device that does not say is
// enabled, which is every device stored before the field existed; a nil device
// is not.
func EnabledOf(d *Device) bool {
	if d == nil {
		return false
	}
	return d.Enabled == nil || *d.Enabled
}
