package storage

import (
	"context"
	"encoding/json"

	"github.com/cockroachdb/pebble"
)

// MIBAssignment describes how one MIB is polled for the device or profile it is
// assigned to.
//
// It is stored in the value of the existing dev-mib:/prof-mib: join rows. Rows
// written before this type existed hold a single sentinel byte, which decodes to
// the default below — enabled with no filters — so existing assignments keep
// polling exactly as they did.
type MIBAssignment struct {
	// Enabled reports whether this MIB is polled at all. A disabled assignment
	// stays attached, so pausing a MIB does not lose its filters.
	Enabled bool `json:"enabled"`

	// Include and exclude patterns are regular expressions applied to an
	// object's OID or name. Excludes win over includes. Empty include lists
	// mean "everything that is pollable".
	IncludeOIDs  []string `json:"include_oids,omitempty"`
	ExcludeOIDs  []string `json:"exclude_oids,omitempty"`
	IncludeNames []string `json:"include_names,omitempty"`
	ExcludeNames []string `json:"exclude_names,omitempty"`

	// MaxOIDs caps how many objects this assignment contributes, 0 for no cap.
	MaxOIDs int `json:"max_oids,omitempty"`
}

// DefaultMIBAssignment is what an assignment means when nothing has been
// configured: poll everything pollable in the module.
func DefaultMIBAssignment() MIBAssignment {
	return MIBAssignment{Enabled: true}
}

// HasFilters reports whether the assignment narrows the module at all.
func (a MIBAssignment) HasFilters() bool {
	return len(a.IncludeOIDs) > 0 || len(a.ExcludeOIDs) > 0 ||
		len(a.IncludeNames) > 0 || len(a.ExcludeNames) > 0 || a.MaxOIDs > 0
}

// encode serializes an assignment for storage.
func (a MIBAssignment) encode() ([]byte, error) {
	return json.Marshal(a)
}

// decodeMIBAssignment reads an assignment from a join row value, treating any
// value that is not assignment JSON as the pre-existing "enabled, unfiltered"
// form.
func decodeMIBAssignment(value []byte) MIBAssignment {
	if len(value) == 0 || value[0] != '{' {
		return DefaultMIBAssignment()
	}

	var a MIBAssignment
	if err := json.Unmarshal(value, &a); err != nil {
		return DefaultMIBAssignment()
	}
	return a
}

// DeviceMIBAssignment returns how a directly assigned MIB is polled for a device.
func (s *Storage) DeviceMIBAssignment(ctx context.Context, deviceID, mibID int64) (MIBAssignment, error) {
	return s.assignment(PrefixDevMIB, deviceID, mibID, "device")
}

// SetDeviceMIBAssignment replaces how a directly assigned MIB is polled.
func (s *Storage) SetDeviceMIBAssignment(ctx context.Context, deviceID, mibID int64, a MIBAssignment) error {
	return s.setAssignment(PrefixDevMIB, deviceID, mibID, "device", a)
}

// ProfileMIBAssignment returns how a MIB in a profile is polled.
func (s *Storage) ProfileMIBAssignment(ctx context.Context, profileID, mibID int64) (MIBAssignment, error) {
	return s.assignment(PrefixProfMIB, profileID, mibID, "profile")
}

// SetProfileMIBAssignment replaces how a MIB in a profile is polled.
func (s *Storage) SetProfileMIBAssignment(ctx context.Context, profileID, mibID int64, a MIBAssignment) error {
	return s.setAssignment(PrefixProfMIB, profileID, mibID, "profile", a)
}

// assignment reads one join row.
func (s *Storage) assignment(prefix string, ownerID, mibID int64, kind string) (MIBAssignment, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	key := []byte(prefix + formatID(ownerID) + ":" + formatID(mibID))
	value, closer, err := s.db.Get(key)
	if err != nil {
		if err == pebble.ErrNotFound {
			return MIBAssignment{}, &AssignmentNotFoundError{Kind: kind, OwnerID: ownerID, MIBID: mibID}
		}
		return MIBAssignment{}, err
	}
	// closer only unpins the value, which has already been copied out.
	defer func() { _ = closer.Close() }()

	return decodeMIBAssignment(value), nil
}

// setAssignment rewrites one join row, refusing to create an assignment that
// does not already exist so that configuring a MIB cannot silently attach it.
func (s *Storage) setAssignment(prefix string, ownerID, mibID int64, kind string, a MIBAssignment) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := []byte(prefix + formatID(ownerID) + ":" + formatID(mibID))
	exists, err := s.keyExists(key)
	if err != nil {
		return err
	}
	if !exists {
		return &AssignmentNotFoundError{Kind: kind, OwnerID: ownerID, MIBID: mibID}
	}

	encoded, err := a.encode()
	if err != nil {
		return err
	}
	return s.db.Set(key, encoded, pebble.Sync)
}

// DeviceMIBAssignments returns every direct assignment for a device, keyed by
// MIB ID, in one scan.
func (s *Storage) DeviceMIBAssignments(ctx context.Context, deviceID int64) (map[int64]MIBAssignment, error) {
	return s.assignments(PrefixDevMIB, deviceID)
}

// ProfileMIBAssignments returns every assignment in a profile, keyed by MIB ID.
func (s *Storage) ProfileMIBAssignments(ctx context.Context, profileID int64) (map[int64]MIBAssignment, error) {
	return s.assignments(PrefixProfMIB, profileID)
}

// assignments scans the join rows belonging to one owner.
func (s *Storage) assignments(prefix string, ownerID int64) (map[int64]MIBAssignment, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	scanPrefix := prefix + formatID(ownerID) + ":"
	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte(scanPrefix),
		UpperBound: []byte(scanPrefix + "\xff"),
	})
	if err != nil {
		return nil, err
	}
	// Iteration has finished; a close error cannot change the rows already read.
	defer func() { _ = iter.Close() }()

	result := make(map[int64]MIBAssignment)
	for iter.First(); iter.Valid(); iter.Next() {
		key := string(iter.Key())
		mibID, err := mibIDFromJoinKey(key, scanPrefix)
		if err != nil {
			continue
		}
		result[mibID] = decodeMIBAssignment(iter.Value())
	}
	if err := iter.Error(); err != nil {
		return nil, err
	}
	return result, nil
}

// mibIDFromJoinKey extracts the MIB ID from a join row key.
func mibIDFromJoinKey(key, scanPrefix string) (int64, error) {
	if len(key) <= len(scanPrefix) {
		return 0, &AssignmentKeyError{Key: key}
	}
	return parseID(key[len(scanPrefix):])
}
