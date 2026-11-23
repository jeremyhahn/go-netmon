package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/cockroachdb/pebble"
)

// DeviceProfile represents a device profile
type DeviceProfile struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
}

// DeviceProfileWithMIBs represents a profile with its associated MIBs
type DeviceProfileWithMIBs struct {
	DeviceProfile
	MIBs []MIB `json:"mibs"`
}

// CreateDeviceProfile creates a new device profile
func (s *Storage) CreateDeviceProfile(ctx context.Context, p *DeviceProfile) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Check if name already exists
	nameKey := []byte(PrefixNameIndex + "profile:" + p.Name)
	exists, err := s.keyExists(nameKey)
	if err != nil {
		return fmt.Errorf("failed to check profile name: %w", err)
	}
	if exists {
		return fmt.Errorf("profile with name %s already exists", p.Name)
	}

	// Generate new ID
	id, err := s.nextID(CounterProfile)
	if err != nil {
		return fmt.Errorf("failed to generate profile ID: %w", err)
	}

	p.ID = id
	p.CreatedAt = time.Now()

	// Store profile
	key := []byte(PrefixProfile + formatID(id))
	if err := s.storeJSON(key, p); err != nil {
		return fmt.Errorf("failed to store profile: %w", err)
	}

	// Store name index
	if err := s.db.Set(nameKey, []byte(formatID(id)), pebble.Sync); err != nil {
		return fmt.Errorf("failed to store profile name index: %w", err)
	}

	return nil
}

// GetDeviceProfile retrieves a device profile by ID
func (s *Storage) GetDeviceProfile(ctx context.Context, id int64) (*DeviceProfile, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	key := []byte(PrefixProfile + formatID(id))
	var p DeviceProfile
	if err := s.getJSON(key, &p); err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil, fmt.Errorf("device profile not found")
		}
		return nil, fmt.Errorf("failed to get device profile: %w", err)
	}

	return &p, nil
}

// GetDeviceProfileByName retrieves a device profile by name
func (s *Storage) GetDeviceProfileByName(ctx context.Context, name string) (*DeviceProfile, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Lookup ID by name
	nameKey := []byte(PrefixNameIndex + "profile:" + name)
	idData, closer, err := s.db.Get(nameKey)
	if err == pebble.ErrNotFound {
		return nil, fmt.Errorf("device profile not found")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get profile name index: %w", err)
	}
	// closer only unpins the value, which has already been copied out.
	defer func() { _ = closer.Close() }()

	idStr := string(idData)
	id, err := parseID(idStr)
	if err != nil {
		return nil, fmt.Errorf("failed to parse profile ID: %w", err)
	}

	// Get profile by ID (release lock first to avoid deadlock)
	s.mu.RUnlock()
	profile, err := s.GetDeviceProfile(ctx, id)
	s.mu.RLock()
	return profile, err
}

// ListDeviceProfiles returns all device profiles
func (s *Storage) ListDeviceProfiles(ctx context.Context) ([]DeviceProfile, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var profiles []DeviceProfile
	prefix := []byte(PrefixProfile)

	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: []byte(PrefixProfile + "~"),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create iterator: %w", err)
	}
	// Iteration has finished; a close error cannot change the rows already read.
	defer func() { _ = iter.Close() }()

	for iter.First(); iter.Valid(); iter.Next() {
		var p DeviceProfile
		if err := json.Unmarshal(iter.Value(), &p); err != nil {
			continue // Skip invalid entries
		}
		profiles = append(profiles, p)
	}

	if err := iter.Error(); err != nil {
		return nil, fmt.Errorf("iterator error: %w", err)
	}

	sortByName(profiles, profileName)
	return profiles, nil
}

// UpdateDeviceProfile updates an existing device profile
func (s *Storage) UpdateDeviceProfile(ctx context.Context, p *DeviceProfile) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Check if profile exists
	key := []byte(PrefixProfile + formatID(p.ID))
	var existing DeviceProfile
	if err := s.getJSON(key, &existing); err != nil {
		return fmt.Errorf("device profile not found")
	}

	// If name changed, update name index
	if existing.Name != p.Name {
		// Check if new name already exists
		nameKey := []byte(PrefixNameIndex + "profile:" + p.Name)
		exists, err := s.keyExists(nameKey)
		if err != nil {
			return fmt.Errorf("failed to check profile name: %w", err)
		}
		if exists {
			return fmt.Errorf("profile with name %s already exists", p.Name)
		}

		// Delete old name index
		oldNameKey := []byte(PrefixNameIndex + "profile:" + existing.Name)
		if err := s.deleteKey(oldNameKey); err != nil {
			return fmt.Errorf("failed to delete old name index: %w", err)
		}

		// Store new name index
		if err := s.db.Set(nameKey, []byte(formatID(p.ID)), pebble.Sync); err != nil {
			return fmt.Errorf("failed to store new name index: %w", err)
		}
	}

	// Keep original creation time
	p.CreatedAt = existing.CreatedAt

	// Update profile
	if err := s.storeJSON(key, p); err != nil {
		return fmt.Errorf("failed to update device profile: %w", err)
	}

	return nil
}

// DeleteDeviceProfile deletes a device profile
func (s *Storage) DeleteDeviceProfile(ctx context.Context, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Get profile to find name for index deletion
	key := []byte(PrefixProfile + formatID(id))
	var p DeviceProfile
	if err := s.getJSON(key, &p); err != nil {
		return fmt.Errorf("device profile not found")
	}

	// Delete profile
	if err := s.deleteKey(key); err != nil {
		return fmt.Errorf("failed to delete device profile: %w", err)
	}

	// Delete name index
	nameKey := []byte(PrefixNameIndex + "profile:" + p.Name)
	if err := s.deleteKey(nameKey); err != nil {
		return fmt.Errorf("failed to delete name index: %w", err)
	}

	// Delete all profile-MIB associations
	profMIBPrefix := PrefixProfMIB + formatID(id) + ":"
	if err := s.deletePrefix(profMIBPrefix); err != nil {
		return fmt.Errorf("failed to delete profile-MIB associations: %w", err)
	}

	// Delete all device-profile associations
	// Note: We need to iterate through all device-profile associations to find ones with this profile
	devProfPrefix := []byte(PrefixDevProf)
	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: devProfPrefix,
		UpperBound: []byte(PrefixDevProf + "~"),
	})
	if err != nil {
		return fmt.Errorf("failed to create iterator: %w", err)
	}

	var keysToDelete []string
	suffix := ":" + formatID(id)
	for iter.First(); iter.Valid(); iter.Next() {
		keyStr := string(iter.Key())
		if strings.HasSuffix(keyStr, suffix) {
			keysToDelete = append(keysToDelete, keyStr)
		}
	}
	_ = iter.Close() // Iteration has finished; a close error cannot change the rows already read.

	// Delete all matching associations
	for _, k := range keysToDelete {
		if err := s.deleteKey([]byte(k)); err != nil {
			return fmt.Errorf("failed to delete device-profile association: %w", err)
		}
	}

	return nil
}

// GetProfileMIBs returns all MIBs associated with a profile
func (s *Storage) GetProfileMIBs(ctx context.Context, profileID int64) ([]MIB, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var mibs []MIB
	profMIBPrefix := []byte(PrefixProfMIB + formatID(profileID) + ":")

	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: profMIBPrefix,
		UpperBound: []byte(PrefixProfMIB + formatID(profileID) + ":~"),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create iterator: %w", err)
	}
	// Iteration has finished; a close error cannot change the rows already read.
	defer func() { _ = iter.Close() }()

	for iter.First(); iter.Valid(); iter.Next() {
		// Key format: prof-mib:PROFILEID:MIBID
		parts := strings.Split(string(iter.Key()), ":")
		if len(parts) == 3 {
			mibID, err := parseID(parts[2])
			if err != nil {
				continue
			}

			var m MIB
			mibKey := []byte(PrefixMIB + formatID(mibID))
			if err := s.getJSON(mibKey, &m); err == nil {
				mibs = append(mibs, m)
			}
		}
	}

	if err := iter.Error(); err != nil {
		return nil, fmt.Errorf("iterator error: %w", err)
	}

	sortByName(mibs, mibName)
	return mibs, nil
}

// AssignMIBToProfile assigns a MIB to a device profile
func (s *Storage) AssignMIBToProfile(ctx context.Context, profileID, mibID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Check if profile exists
	profileKey := []byte(PrefixProfile + formatID(profileID))
	exists, err := s.keyExists(profileKey)
	if err != nil {
		return fmt.Errorf("failed to check profile: %w", err)
	}
	if !exists {
		return fmt.Errorf("profile not found")
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
	key := []byte(PrefixProfMIB + formatID(profileID) + ":" + formatID(mibID))
	assigned, err := s.keyExists(key)
	if err != nil {
		return fmt.Errorf("failed to check existing assignment: %w", err)
	}
	if assigned {
		return nil
	}

	return s.db.Set(key, []byte{1}, pebble.Sync)
}

// RemoveMIBFromProfile removes a MIB from a device profile
func (s *Storage) RemoveMIBFromProfile(ctx context.Context, profileID, mibID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := []byte(PrefixProfMIB + formatID(profileID) + ":" + formatID(mibID))
	exists, err := s.keyExists(key)
	if err != nil {
		return fmt.Errorf("failed to check association: %w", err)
	}
	if !exists {
		return fmt.Errorf("MIB not assigned to profile")
	}

	return s.deleteKey(key)
}

// GetDevicesByProfile returns all devices that use a specific profile
func (s *Storage) GetDevicesByProfile(ctx context.Context, profileID int64) ([]Device, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var devices []Device
	devProfPrefix := []byte(PrefixDevProf)

	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: devProfPrefix,
		UpperBound: []byte(PrefixDevProf + "~"),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create iterator: %w", err)
	}
	// Iteration has finished; a close error cannot change the rows already read.
	defer func() { _ = iter.Close() }()

	// Look for keys with the pattern: dev-prof:DEVICEID:PROFILEID
	suffix := ":" + formatID(profileID)
	for iter.First(); iter.Valid(); iter.Next() {
		keyStr := string(iter.Key())
		if strings.HasSuffix(keyStr, suffix) {
			// Extract device ID from key
			parts := strings.Split(keyStr, ":")
			if len(parts) == 3 {
				deviceID, err := parseID(parts[1])
				if err != nil {
					continue
				}

				// Get device details
				var device Device
				deviceKey := []byte(PrefixDevice + formatID(deviceID))
				if err := s.getJSON(deviceKey, &device); err == nil {
					devices = append(devices, device)
				}
			}
		}
	}

	if err := iter.Error(); err != nil {
		return nil, fmt.Errorf("iterator error: %w", err)
	}

	sortByName(devices, deviceName)
	return devices, nil
}
