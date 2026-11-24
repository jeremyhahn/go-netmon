package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/cockroachdb/pebble"
)

// MIB represents a MIB module
type MIB struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	FilePath    string    `json:"file_path"`
	Description string    `json:"description"`
	ObjectCount int       `json:"object_count"`
	Tags        []string  `json:"tags,omitempty"`
	ImportedAt  time.Time `json:"imported_at"`

	// RefreshedAt records when the module's parsed metadata was last replaced
	// in place by a re-import. It stays absent until the first refresh, which
	// is what makes it possible to tell, across a few hundred modules, which
	// ones a re-import actually reached.
	RefreshedAt *time.Time `json:"refreshed_at,omitempty"`
}

// MIBContent is the part of a MIB record that is derived from parsing the
// module file.
//
// Everything else on a MIB — its ID, its name, its tags, and the device and
// profile assignments that are keyed by its ID — describes how the operator
// uses the module rather than what the file says, so re-parsing a file must
// not disturb any of it.
type MIBContent struct {
	FilePath    string
	Description string
	ObjectCount int
}

// MIBExistsError reports an attempt to import a module that is already stored.
//
// It carries the existing record's ID because the useful next step — refreshing
// that record in place rather than deleting it — needs to name it.
type MIBExistsError struct {
	Name string
	ID   int64
}

func (e *MIBExistsError) Error() string {
	return "storage: MIB " + strconv.Quote(e.Name) + " already exists as id " +
		strconv.FormatInt(e.ID, 10)
}

// MIBNotFoundError reports that no module is stored under the given name.
type MIBNotFoundError struct {
	Name string
}

func (e *MIBNotFoundError) Error() string {
	return "storage: MIB not found: " + strconv.Quote(e.Name)
}

// MIBStoreError reports a MIB record that could not be read or written.
type MIBStoreError struct {
	Op   string
	Name string
	Err  error
}

func (e *MIBStoreError) Error() string {
	return "storage: " + e.Op + " MIB " + strconv.Quote(e.Name) + ": " + e.Err.Error()
}

func (e *MIBStoreError) Unwrap() error { return e.Err }

// CreateMIB creates a new MIB record
func (s *Storage) CreateMIB(ctx context.Context, m *MIB) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// A module name is imported once. Report the record that already holds it
	// so the caller can offer to refresh it instead.
	existingID, err := s.mibIDLocked(m.Name)
	if err == nil {
		return &MIBExistsError{Name: m.Name, ID: existingID}
	}
	var notFound *MIBNotFoundError
	if !errors.As(err, &notFound) {
		return err
	}

	// Generate new ID
	id, err := s.nextID(CounterMIB)
	if err != nil {
		return fmt.Errorf("failed to generate MIB ID: %w", err)
	}

	m.ID = id
	m.ImportedAt = time.Now()

	// Store MIB
	key := []byte(PrefixMIB + formatID(id))
	if err := s.storeJSON(key, m); err != nil {
		return fmt.Errorf("failed to store MIB: %w", err)
	}

	// Store name index
	nameKey := []byte(PrefixNameIndex + "mib:" + m.Name)
	if err := s.db.Set(nameKey, []byte(formatID(id)), pebble.Sync); err != nil {
		return fmt.Errorf("failed to store MIB name index: %w", err)
	}

	return nil
}

// GetMIB retrieves a MIB by ID
func (s *Storage) GetMIB(ctx context.Context, id int64) (*MIB, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	key := []byte(PrefixMIB + formatID(id))
	var m MIB
	if err := s.getJSON(key, &m); err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil, fmt.Errorf("MIB not found")
		}
		return nil, fmt.Errorf("failed to get MIB: %w", err)
	}

	return &m, nil
}

// GetMIBByName retrieves a MIB by name. A module that is not stored yields a
// *MIBNotFoundError, which callers use to tell "not imported yet" apart from a
// store that could not be read.
func (s *Storage) GetMIBByName(ctx context.Context, name string) (*MIB, error) {
	s.mu.RLock()
	id, err := s.mibIDLocked(name)
	s.mu.RUnlock()
	if err != nil {
		return nil, err
	}

	return s.GetMIB(ctx, id)
}

// mibIDLocked resolves a module name to its record ID through the name index.
// The caller holds s.mu.
func (s *Storage) mibIDLocked(name string) (int64, error) {
	nameKey := []byte(PrefixNameIndex + "mib:" + name)
	idData, closer, err := s.db.Get(nameKey)
	if err == pebble.ErrNotFound {
		return 0, &MIBNotFoundError{Name: name}
	}
	if err != nil {
		return 0, &MIBStoreError{Op: "resolve", Name: name, Err: err}
	}
	idStr := string(idData)
	// closer only unpins the value, which has already been copied out.
	_ = closer.Close()

	id, err := parseID(idStr)
	if err != nil {
		return 0, &MIBStoreError{Op: "resolve", Name: name, Err: err}
	}
	return id, nil
}

// ListMIBs returns all MIBs
func (s *Storage) ListMIBs(ctx context.Context) ([]MIB, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var mibs []MIB
	prefix := []byte(PrefixMIB)

	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: []byte(PrefixMIB + "~"),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create iterator: %w", err)
	}
	// Iteration has finished; a close error cannot change the rows already read.
	defer func() { _ = iter.Close() }()

	for iter.First(); iter.Valid(); iter.Next() {
		var m MIB
		if err := json.Unmarshal(iter.Value(), &m); err != nil {
			continue // Skip invalid entries
		}
		mibs = append(mibs, m)
	}

	if err := iter.Error(); err != nil {
		return nil, fmt.Errorf("iterator error: %w", err)
	}

	sortByName(mibs, mibName)
	return mibs, nil
}

// UpdateMIB updates an existing MIB
func (s *Storage) UpdateMIB(ctx context.Context, m *MIB) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Check if MIB exists
	key := []byte(PrefixMIB + formatID(m.ID))
	var existing MIB
	if err := s.getJSON(key, &existing); err != nil {
		return fmt.Errorf("MIB not found")
	}

	// If name changed, update name index
	if existing.Name != m.Name {
		// Check if new name already exists
		nameKey := []byte(PrefixNameIndex + "mib:" + m.Name)
		exists, err := s.keyExists(nameKey)
		if err != nil {
			return fmt.Errorf("failed to check MIB name: %w", err)
		}
		if exists {
			return fmt.Errorf("MIB with name %s already exists", m.Name)
		}

		// Delete old name index
		oldNameKey := []byte(PrefixNameIndex + "mib:" + existing.Name)
		if err := s.deleteKey(oldNameKey); err != nil {
			return fmt.Errorf("failed to delete old name index: %w", err)
		}

		// Store new name index
		if err := s.db.Set(nameKey, []byte(formatID(m.ID)), pebble.Sync); err != nil {
			return fmt.Errorf("failed to store new name index: %w", err)
		}
	}

	// Keep original import time
	m.ImportedAt = existing.ImportedAt

	// Update MIB
	if err := s.storeJSON(key, m); err != nil {
		return fmt.Errorf("failed to update MIB: %w", err)
	}

	return nil
}

// ReplaceMIBContent refreshes a stored module's parsed metadata in place and
// returns the resulting record.
//
// Only the fields that come from the file change. The record keeps its ID — the
// device and profile join rows are keyed by it, so a new ID would silently
// detach the module from everything polling it — along with its name, its tags
// and its original import time. No join row is read or written here, so every
// assignment and every per-assignment filter survives by construction.
//
// The record is rewritten with a single store write, so a caller that fails
// mid-refresh leaves the previous record intact rather than a half-updated one.
func (s *Storage) ReplaceMIBContent(ctx context.Context, name string, content MIBContent) (*MIB, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	id, err := s.mibIDLocked(name)
	if err != nil {
		return nil, err
	}

	key := []byte(PrefixMIB + formatID(id))
	var m MIB
	if err := s.getJSON(key, &m); err != nil {
		// The name index pointed at a record that is not there. Report it as the
		// missing module it is rather than as a read failure.
		return nil, &MIBNotFoundError{Name: name}
	}

	refreshed := time.Now()
	m.FilePath = content.FilePath
	m.Description = content.Description
	m.ObjectCount = content.ObjectCount
	m.RefreshedAt = &refreshed

	if err := s.storeJSON(key, &m); err != nil {
		return nil, &MIBStoreError{Op: "refresh", Name: name, Err: err}
	}

	return &m, nil
}

// DeleteMIB deletes a MIB
func (s *Storage) DeleteMIB(ctx context.Context, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Get MIB to find name for index deletion
	key := []byte(PrefixMIB + formatID(id))
	var m MIB
	if err := s.getJSON(key, &m); err != nil {
		return fmt.Errorf("MIB not found")
	}

	// Delete MIB
	if err := s.deleteKey(key); err != nil {
		return fmt.Errorf("failed to delete MIB: %w", err)
	}

	// Delete name index
	nameKey := []byte(PrefixNameIndex + "mib:" + m.Name)
	if err := s.deleteKey(nameKey); err != nil {
		return fmt.Errorf("failed to delete name index: %w", err)
	}

	// Delete all device-MIB associations
	// Note: We need to iterate through all device-MIB associations to find ones with this MIB
	devMIBPrefix := []byte(PrefixDevMIB)
	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: devMIBPrefix,
		UpperBound: []byte(PrefixDevMIB + "~"),
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

	// Delete all device-MIB associations
	for _, k := range keysToDelete {
		if deleteErr := s.deleteKey([]byte(k)); deleteErr != nil {
			return fmt.Errorf("failed to delete device-MIB association: %w", deleteErr)
		}
	}

	// Delete all profile-MIB associations
	profMIBPrefix := []byte(PrefixProfMIB)
	iter, err = s.db.NewIter(&pebble.IterOptions{
		LowerBound: profMIBPrefix,
		UpperBound: []byte(PrefixProfMIB + "~"),
	})
	if err != nil {
		return fmt.Errorf("failed to create iterator: %w", err)
	}

	keysToDelete = nil
	for iter.First(); iter.Valid(); iter.Next() {
		keyStr := string(iter.Key())
		if strings.HasSuffix(keyStr, suffix) {
			keysToDelete = append(keysToDelete, keyStr)
		}
	}
	_ = iter.Close() // Iteration has finished; a close error cannot change the rows already read.

	// Delete all profile-MIB associations
	for _, k := range keysToDelete {
		if err := s.deleteKey([]byte(k)); err != nil {
			return fmt.Errorf("failed to delete profile-MIB association: %w", err)
		}
	}

	return nil
}

// UpdateMIBTags updates the tags for a MIB
func (s *Storage) UpdateMIBTags(ctx context.Context, id int64, tags []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Get existing MIB
	key := []byte(PrefixMIB + formatID(id))
	var m MIB
	if err := s.getJSON(key, &m); err != nil {
		return fmt.Errorf("MIB not found")
	}

	// Update tags
	m.Tags = tags

	// Store updated MIB
	if err := s.storeJSON(key, &m); err != nil {
		return fmt.Errorf("failed to update MIB tags: %w", err)
	}

	return nil
}
