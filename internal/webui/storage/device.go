package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/cockroachdb/pebble"
)

// Device represents a monitored device
type Device struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Address     string    `json:"address"`
	SNMPVersion string    `json:"snmp_version"`
	Community   string    `json:"community,omitempty"`
	Port        int       `json:"port"`
	CreatedAt   time.Time `json:"created_at"`

	// PollInterval is how often this device is polled, in seconds. It is
	// absent on a device that inherits global.default_poll_interval, which is
	// every device created before the field existed and every device an
	// operator has not given a schedule of its own.
	//
	// It is a pointer rather than a count because "inherit the default" and
	// "explicitly the same number the default happens to be today" are
	// different configurations: the first tracks the default when it changes,
	// the second does not. A magic zero could not tell them apart, and zero is
	// not a schedule anyway — see ValidatePollInterval.
	PollInterval *int `json:"poll_interval,omitempty"`

	// Enabled reports whether this device is polled at all. It is absent on
	// every device created before the field existed, and absent means
	// enabled: a store written by an earlier version must not wake up with its
	// whole fleet switched off. Read it through EnabledOf.
	//
	// A pointer for the same reason as PollInterval: a plain bool's zero value
	// is false, so a record that never mentioned the field would decode as a
	// device an operator had disabled.
	Enabled *bool `json:"enabled,omitempty"`

	// SNMPv3 credentials. Empty on a v1 or v2c device, which authenticates
	// with Community instead.
	//
	// These are stored because the poller cannot ask for them: it reads its
	// whole device list from this service, and a v3 device without a username
	// reaches the agent and is refused with "unknown username". Selecting v3
	// while these could not be recorded produced exactly that — a device the
	// UI showed as configured and the poller could never authenticate.
	//
	// They are as sensitive as Community and are held the same way, which is
	// to say in clear text in this store. See
	// docs/operations/security-posture.md.
	Username     string `json:"username,omitempty"`
	AuthProtocol string `json:"auth_protocol,omitempty"`
	AuthPassword string `json:"auth_password,omitempty"`
	PrivProtocol string `json:"priv_protocol,omitempty"`
	PrivPassword string `json:"priv_password,omitempty"`
}

// DeviceWithMIBs represents a device with its associated MIBs
type DeviceWithMIBs struct {
	Device
	MIBs     []MIB   `json:"mibs"`
	Profiles []int64 `json:"profiles"`
}

// CreateDevice creates a new device
func (s *Storage) CreateDevice(ctx context.Context, d *Device) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if d.Port == 0 {
		d.Port = DefaultDevicePort
	}

	if err := ValidatePollInterval(d.Name, d.PollInterval); err != nil {
		return err
	}

	// Check if name already exists
	nameKey := []byte(PrefixNameIndex + "device:" + d.Name)
	exists, err := s.keyExists(nameKey)
	if err != nil {
		return fmt.Errorf("failed to check device name: %w", err)
	}
	if exists {
		return fmt.Errorf("device with name %s already exists", d.Name)
	}

	// Generate new ID
	id, err := s.nextID(CounterDevice)
	if err != nil {
		return fmt.Errorf("failed to generate device ID: %w", err)
	}

	d.ID = id
	d.CreatedAt = time.Now()

	// Store device
	key := []byte(PrefixDevice + formatID(id))
	if err := s.storeJSON(key, d); err != nil {
		return fmt.Errorf("failed to store device: %w", err)
	}

	// Store name index
	if err := s.db.Set(nameKey, []byte(formatID(id)), pebble.Sync); err != nil {
		return fmt.Errorf("failed to store device name index: %w", err)
	}

	return nil
}

// GetDevice retrieves a device by ID
func (s *Storage) GetDevice(ctx context.Context, id int64) (*Device, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	key := []byte(PrefixDevice + formatID(id))
	var d Device
	if err := s.getJSON(key, &d); err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil, fmt.Errorf("device not found")
		}
		return nil, fmt.Errorf("failed to get device: %w", err)
	}

	return &d, nil
}

// GetDeviceByName retrieves a device by name
func (s *Storage) GetDeviceByName(ctx context.Context, name string) (*Device, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Lookup ID by name
	nameKey := []byte(PrefixNameIndex + "device:" + name)
	idData, closer, err := s.db.Get(nameKey)
	if err == pebble.ErrNotFound {
		return nil, fmt.Errorf("device not found")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get device name index: %w", err)
	}
	// closer only unpins the value, which has already been copied out.
	defer func() { _ = closer.Close() }()

	idStr := string(idData)
	id, err := parseID(idStr)
	if err != nil {
		return nil, fmt.Errorf("failed to parse device ID: %w", err)
	}

	// Get device by ID (release lock first to avoid deadlock)
	s.mu.RUnlock()
	device, err := s.GetDevice(ctx, id)
	s.mu.RLock()
	return device, err
}

// ListDevices returns all devices
func (s *Storage) ListDevices(ctx context.Context) ([]Device, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var devices []Device
	prefix := []byte(PrefixDevice)

	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: []byte(PrefixDevice + "~"),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create iterator: %w", err)
	}
	// Iteration has finished; a close error cannot change the rows already read.
	defer func() { _ = iter.Close() }()

	for iter.First(); iter.Valid(); iter.Next() {
		var d Device
		if err := json.Unmarshal(iter.Value(), &d); err != nil {
			continue // Skip invalid entries
		}
		devices = append(devices, d)
	}

	if err := iter.Error(); err != nil {
		return nil, fmt.Errorf("iterator error: %w", err)
	}

	sortByName(devices, deviceName)
	return devices, nil
}

// UpdateDevice updates an existing device
func (s *Storage) UpdateDevice(ctx context.Context, d *Device) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := ValidatePollInterval(d.Name, d.PollInterval); err != nil {
		return err
	}

	// Check if device exists
	key := []byte(PrefixDevice + formatID(d.ID))
	var existing Device
	if err := s.getJSON(key, &existing); err != nil {
		return fmt.Errorf("device not found")
	}

	// If name changed, update name index
	if existing.Name != d.Name {
		// Check if new name already exists
		nameKey := []byte(PrefixNameIndex + "device:" + d.Name)
		exists, err := s.keyExists(nameKey)
		if err != nil {
			return fmt.Errorf("failed to check device name: %w", err)
		}
		if exists {
			return fmt.Errorf("device with name %s already exists", d.Name)
		}

		// Delete old name index
		oldNameKey := []byte(PrefixNameIndex + "device:" + existing.Name)
		if err := s.deleteKey(oldNameKey); err != nil {
			return fmt.Errorf("failed to delete old name index: %w", err)
		}

		// Store new name index
		if err := s.db.Set(nameKey, []byte(formatID(d.ID)), pebble.Sync); err != nil {
			return fmt.Errorf("failed to store new name index: %w", err)
		}
	}

	// Keep original creation time
	d.CreatedAt = existing.CreatedAt

	// Update device
	if err := s.storeJSON(key, d); err != nil {
		return fmt.Errorf("failed to update device: %w", err)
	}

	return nil
}

// DeleteDevice deletes a device
func (s *Storage) DeleteDevice(ctx context.Context, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Get device to find name for index deletion
	key := []byte(PrefixDevice + formatID(id))
	var d Device
	if err := s.getJSON(key, &d); err != nil {
		return fmt.Errorf("device not found")
	}

	// Delete device
	if err := s.deleteKey(key); err != nil {
		return fmt.Errorf("failed to delete device: %w", err)
	}

	// Delete name index
	nameKey := []byte(PrefixNameIndex + "device:" + d.Name)
	if err := s.deleteKey(nameKey); err != nil {
		return fmt.Errorf("failed to delete name index: %w", err)
	}

	// Delete all device-MIB associations
	devMIBPrefix := PrefixDevMIB + formatID(id) + ":"
	if err := s.deletePrefix(devMIBPrefix); err != nil {
		return fmt.Errorf("failed to delete device-MIB associations: %w", err)
	}

	// Delete all device-profile associations
	devProfPrefix := PrefixDevProf + formatID(id) + ":"
	if err := s.deletePrefix(devProfPrefix); err != nil {
		return fmt.Errorf("failed to delete device-profile associations: %w", err)
	}

	return nil
}

// GetDeviceMIBs returns all MIBs associated with a device (both direct and via profiles)
func (s *Storage) GetDeviceMIBs(ctx context.Context, deviceID int64) ([]MIB, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	mibIDs := make(map[int64]bool)

	// Get direct MIB assignments
	devMIBPrefix := []byte(PrefixDevMIB + formatID(deviceID) + ":")
	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: devMIBPrefix,
		UpperBound: []byte(PrefixDevMIB + formatID(deviceID) + ":~"),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create iterator: %w", err)
	}

	for iter.First(); iter.Valid(); iter.Next() {
		// Key format: dev-mib:DEVICEID:MIBID
		parts := strings.Split(string(iter.Key()), ":")
		if len(parts) == 3 {
			mibID, parseErr := parseID(parts[2])
			if parseErr == nil {
				mibIDs[mibID] = true
			}
		}
	}
	_ = iter.Close() // Iteration has finished; a close error cannot change the rows already read.

	// Get MIBs from assigned profiles
	devProfPrefix := []byte(PrefixDevProf + formatID(deviceID) + ":")
	iter, err = s.db.NewIter(&pebble.IterOptions{
		LowerBound: devProfPrefix,
		UpperBound: []byte(PrefixDevProf + formatID(deviceID) + ":~"),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create iterator: %w", err)
	}

	var profileIDs []int64
	for iter.First(); iter.Valid(); iter.Next() {
		// Key format: dev-prof:DEVICEID:PROFILEID
		parts := strings.Split(string(iter.Key()), ":")
		if len(parts) == 3 {
			profileID, err := parseID(parts[2])
			if err == nil {
				profileIDs = append(profileIDs, profileID)
			}
		}
	}
	_ = iter.Close() // Iteration has finished; a close error cannot change the rows already read.

	// For each profile, get its MIBs
	for _, profileID := range profileIDs {
		profMIBPrefix := []byte(PrefixProfMIB + formatID(profileID) + ":")
		iter, err := s.db.NewIter(&pebble.IterOptions{
			LowerBound: profMIBPrefix,
			UpperBound: []byte(PrefixProfMIB + formatID(profileID) + ":~"),
		})
		if err != nil {
			return nil, fmt.Errorf("failed to create iterator: %w", err)
		}

		for iter.First(); iter.Valid(); iter.Next() {
			// Key format: prof-mib:PROFILEID:MIBID
			parts := strings.Split(string(iter.Key()), ":")
			if len(parts) == 3 {
				mibID, err := parseID(parts[2])
				if err == nil {
					mibIDs[mibID] = true
				}
			}
		}
		_ = iter.Close() // Iteration has finished; a close error cannot change the rows already read.
	}

	// Retrieve all unique MIBs. The IDs come out of a map, so the result is
	// ordered by name rather than by however the runtime walked it.
	var mibs []MIB
	for mibID := range mibIDs {
		var m MIB
		key := []byte(PrefixMIB + formatID(mibID))
		if err := s.getJSON(key, &m); err == nil {
			mibs = append(mibs, m)
		}
	}

	sortByName(mibs, mibName)
	return mibs, nil
}

// GetDeviceMIBsSeparated returns direct MIBs and all MIBs (including from profiles) separately
func (s *Storage) GetDeviceMIBsSeparated(ctx context.Context, deviceID int64) (directMIBs, allMIBs []MIB, err error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	directMIBIDs := make(map[int64]bool)
	allMIBIDs := make(map[int64]bool)

	// Get direct MIB assignments
	devMIBPrefix := []byte(PrefixDevMIB + formatID(deviceID) + ":")
	iter, iterErr := s.db.NewIter(&pebble.IterOptions{
		LowerBound: devMIBPrefix,
		UpperBound: []byte(PrefixDevMIB + formatID(deviceID) + ":~"),
	})
	if iterErr != nil {
		return nil, nil, fmt.Errorf("failed to create iterator: %w", iterErr)
	}

	for iter.First(); iter.Valid(); iter.Next() {
		// Key format: dev-mib:DEVICEID:MIBID
		parts := strings.Split(string(iter.Key()), ":")
		if len(parts) == 3 {
			mibID, parseErr := parseID(parts[2])
			if parseErr == nil {
				directMIBIDs[mibID] = true
				allMIBIDs[mibID] = true
			}
		}
	}
	_ = iter.Close() // Iteration has finished; a close error cannot change the rows already read.

	// Get MIBs from assigned profiles
	devProfPrefix := []byte(PrefixDevProf + formatID(deviceID) + ":")
	iter, iterErr = s.db.NewIter(&pebble.IterOptions{
		LowerBound: devProfPrefix,
		UpperBound: []byte(PrefixDevProf + formatID(deviceID) + ":~"),
	})
	if iterErr != nil {
		return nil, nil, fmt.Errorf("failed to create iterator: %w", iterErr)
	}

	var profileIDs []int64
	for iter.First(); iter.Valid(); iter.Next() {
		// Key format: dev-prof:DEVICEID:PROFILEID
		parts := strings.Split(string(iter.Key()), ":")
		if len(parts) == 3 {
			profileID, parseErr := parseID(parts[2])
			if parseErr == nil {
				profileIDs = append(profileIDs, profileID)
			}
		}
	}
	_ = iter.Close() // Iteration has finished; a close error cannot change the rows already read.

	// For each profile, get its MIBs
	for _, profileID := range profileIDs {
		profMIBPrefix := []byte(PrefixProfMIB + formatID(profileID) + ":")
		iter, iterErr := s.db.NewIter(&pebble.IterOptions{
			LowerBound: profMIBPrefix,
			UpperBound: []byte(PrefixProfMIB + formatID(profileID) + ":~"),
		})
		if iterErr != nil {
			return nil, nil, fmt.Errorf("failed to create iterator: %w", iterErr)
		}

		for iter.First(); iter.Valid(); iter.Next() {
			// Key format: prof-mib:PROFILEID:MIBID
			parts := strings.Split(string(iter.Key()), ":")
			if len(parts) == 3 {
				mibID, parseErr := parseID(parts[2])
				if parseErr == nil {
					allMIBIDs[mibID] = true
				}
			}
		}
		_ = iter.Close() // Iteration has finished; a close error cannot change the rows already read.
	}

	// Retrieve direct MIBs
	for mibID := range directMIBIDs {
		var m MIB
		key := []byte(PrefixMIB + formatID(mibID))
		if getErr := s.getJSON(key, &m); getErr == nil {
			directMIBs = append(directMIBs, m)
		}
	}

	// Retrieve all unique MIBs
	for mibID := range allMIBIDs {
		var m MIB
		key := []byte(PrefixMIB + formatID(mibID))
		if getErr := s.getJSON(key, &m); getErr == nil {
			allMIBs = append(allMIBs, m)
		}
	}

	// Both lists are assembled through a map, so both are ordered here.
	sortByName(directMIBs, mibName)
	sortByName(allMIBs, mibName)
	return directMIBs, allMIBs, nil
}

// AssignMIBToDevice assigns a MIB directly to a device
func (s *Storage) AssignMIBToDevice(ctx context.Context, deviceID, mibID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Check if device exists
	deviceKey := []byte(PrefixDevice + formatID(deviceID))
	exists, err := s.keyExists(deviceKey)
	if err != nil {
		return fmt.Errorf("failed to check device: %w", err)
	}
	if !exists {
		return fmt.Errorf("device not found")
	}

	// Check if MIB exists
	mibKey := []byte(PrefixMIB + formatID(mibID))
	exists, err = s.keyExists(mibKey)
	if err != nil {
		return fmt.Errorf("failed to check MIB: %w", err)
	}
	if !exists {
		return fmt.Errorf("MIB not found")
	}

	// Create association. Assigning a MIB that is already assigned must leave
	// the existing row alone: its value carries the enable state and the
	// include/exclude filters, and overwriting it with the legacy sentinel
	// would silently reset a configured assignment to "enabled, unfiltered".
	key := []byte(PrefixDevMIB + formatID(deviceID) + ":" + formatID(mibID))
	assigned, err := s.keyExists(key)
	if err != nil {
		return fmt.Errorf("failed to check existing assignment: %w", err)
	}
	if assigned {
		return nil
	}

	return s.db.Set(key, []byte{1}, pebble.Sync)
}

// RemoveMIBFromDevice removes a direct MIB assignment from a device
func (s *Storage) RemoveMIBFromDevice(ctx context.Context, deviceID, mibID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := []byte(PrefixDevMIB + formatID(deviceID) + ":" + formatID(mibID))
	exists, err := s.keyExists(key)
	if err != nil {
		return fmt.Errorf("failed to check association: %w", err)
	}
	if !exists {
		return fmt.Errorf("MIB not assigned to device")
	}

	return s.deleteKey(key)
}

// AssignProfileToDevice assigns a profile to a device
func (s *Storage) AssignProfileToDevice(ctx context.Context, deviceID, profileID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Check if device exists
	deviceKey := []byte(PrefixDevice + formatID(deviceID))
	exists, err := s.keyExists(deviceKey)
	if err != nil {
		return fmt.Errorf("failed to check device: %w", err)
	}
	if !exists {
		return fmt.Errorf("device not found")
	}

	// Check if profile exists
	profileKey := []byte(PrefixProfile + formatID(profileID))
	exists, err = s.keyExists(profileKey)
	if err != nil {
		return fmt.Errorf("failed to check profile: %w", err)
	}
	if !exists {
		return fmt.Errorf("profile not found")
	}

	// Create association
	key := []byte(PrefixDevProf + formatID(deviceID) + ":" + formatID(profileID))
	return s.db.Set(key, []byte{1}, pebble.Sync)
}

// RemoveProfileFromDevice removes a profile assignment from a device
func (s *Storage) RemoveProfileFromDevice(ctx context.Context, deviceID, profileID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := []byte(PrefixDevProf + formatID(deviceID) + ":" + formatID(profileID))
	exists, err := s.keyExists(key)
	if err != nil {
		return fmt.Errorf("failed to check association: %w", err)
	}
	if !exists {
		return fmt.Errorf("profile not assigned to device")
	}

	return s.deleteKey(key)
}

// GetDeviceProfiles returns all profiles assigned to a device
func (s *Storage) GetDeviceProfiles(ctx context.Context, deviceID int64) ([]DeviceProfile, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var profiles []DeviceProfile
	devProfPrefix := []byte(PrefixDevProf + formatID(deviceID) + ":")

	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: devProfPrefix,
		UpperBound: []byte(PrefixDevProf + formatID(deviceID) + ":~"),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create iterator: %w", err)
	}
	// Iteration has finished; a close error cannot change the rows already read.
	defer func() { _ = iter.Close() }()

	for iter.First(); iter.Valid(); iter.Next() {
		// Key format: dev-prof:DEVICEID:PROFILEID
		parts := strings.Split(string(iter.Key()), ":")
		if len(parts) == 3 {
			profileID, err := parseID(parts[2])
			if err != nil {
				continue
			}

			var p DeviceProfile
			profileKey := []byte(PrefixProfile + formatID(profileID))
			if err := s.getJSON(profileKey, &p); err == nil {
				profiles = append(profiles, p)
			}
		}
	}

	if err := iter.Error(); err != nil {
		return nil, fmt.Errorf("iterator error: %w", err)
	}

	sortByName(profiles, profileName)
	return profiles, nil
}
