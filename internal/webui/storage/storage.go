// Package storage persists the web UI's own records — devices, profiles, MIBs
// and their assignments — in an embedded Pebble key-value store.
package storage

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/cockroachdb/pebble"
)

// Storage provides database access for the Web UI using PebbleDB
type Storage struct {
	db   *pebble.DB
	path string
	mu   sync.RWMutex
}

// Key prefixes for different entity types
const (
	PrefixDevice    = "dev:"
	PrefixProfile   = "prof:"
	PrefixMIB       = "mib:"
	PrefixDevMIB    = "dev-mib:"
	PrefixProfMIB   = "prof-mib:"
	PrefixDevProf   = "dev-prof:"
	PrefixCounter   = "counter:"
	PrefixNameIndex = "idx:"
)

// Counter keys for auto-increment IDs
const (
	CounterDevice  = PrefixCounter + "device"
	CounterProfile = PrefixCounter + "profile"
	CounterMIB     = PrefixCounter + "mib"
)

// New creates a new storage instance
func New(dbPath string) (*Storage, error) {
	db, err := pebble.Open(dbPath, &pebble.Options{
		Cache:        pebble.NewCache(128 << 20), // 128MB cache
		MaxOpenFiles: 1000,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to open pebble db: %w", err)
	}

	return &Storage{
		db:   db,
		path: dbPath,
	}, nil
}

// Close closes the database connection
func (s *Storage) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.db != nil {
		err := s.db.Close()
		s.db = nil // Set to nil to prevent double-close
		return err
	}
	return nil
}

// nextID returns the next auto-increment ID for a counter
func (s *Storage) nextID(counterKey string) (int64, error) {
	key := []byte(counterKey)

	// Get current value
	data, closer, err := s.db.Get(key)
	var current int64
	switch {
	case err == pebble.ErrNotFound:
		current = 0
	case err != nil:
		return 0, fmt.Errorf("failed to get counter: %w", err)
	default:
		// The counter was written below as uint64 of an int64 that only ever
		// increments from zero, so it cannot be above int64 range.
		current = int64(binary.BigEndian.Uint64(data)) // #nosec G115 -- round-trip of a value this function wrote as an int64
		_ = closer.Close()                             // closer only unpins the value, which has already been copied out.
	}

	// Increment
	next := current + 1
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, uint64(next))

	// Store new value
	if err := s.db.Set(key, buf, pebble.Sync); err != nil {
		return 0, fmt.Errorf("failed to update counter: %w", err)
	}

	return next, nil
}

// formatID formats an int64 ID as a sortable string key
func formatID(id int64) string {
	return fmt.Sprintf("%020d", id)
}

// parseID parses an ID from a key
func parseID(key string) (int64, error) {
	var id int64
	_, err := fmt.Sscanf(key, "%d", &id)
	return id, err
}

// storeJSON stores a value as JSON
func (s *Storage) storeJSON(key []byte, value interface{}) error {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("failed to marshal JSON: %w", err)
	}
	return s.db.Set(key, data, pebble.Sync)
}

// getJSON retrieves and unmarshals a JSON value
func (s *Storage) getJSON(key []byte, value interface{}) error {
	data, closer, err := s.db.Get(key)
	if err != nil {
		if err == pebble.ErrNotFound {
			return fmt.Errorf("key not found")
		}
		return fmt.Errorf("failed to get key: %w", err)
	}
	// closer only unpins the value, which has already been copied out.
	defer func() { _ = closer.Close() }()

	if err := json.Unmarshal(data, value); err != nil {
		return fmt.Errorf("failed to unmarshal JSON: %w", err)
	}

	return nil
}

// deleteKey deletes a key
func (s *Storage) deleteKey(key []byte) error {
	return s.db.Delete(key, pebble.Sync)
}

// deletePrefix deletes all keys with a given prefix
func (s *Storage) deletePrefix(prefix string) error {
	prefixBytes := []byte(prefix)
	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: prefixBytes,
		UpperBound: []byte(prefix + "~"),
	})
	if err != nil {
		return fmt.Errorf("failed to create iterator: %w", err)
	}
	// Iteration has finished; a close error cannot change the rows already read.
	defer func() { _ = iter.Close() }()

	batch := s.db.NewBatch()
	// Close only releases the batch's memory, committed or not.
	defer func() { _ = batch.Close() }()

	for iter.First(); iter.Valid(); iter.Next() {
		if err := batch.Delete(iter.Key(), nil); err != nil {
			return fmt.Errorf("failed to delete key in batch: %w", err)
		}
	}

	if err := iter.Error(); err != nil {
		return fmt.Errorf("iterator error: %w", err)
	}

	if err := batch.Commit(pebble.Sync); err != nil {
		return fmt.Errorf("failed to commit batch: %w", err)
	}

	return nil
}

// keyExists checks if a key exists
func (s *Storage) keyExists(key []byte) (bool, error) {
	_, closer, err := s.db.Get(key)
	if err == pebble.ErrNotFound {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	_ = closer.Close() // closer only unpins the value, which has already been copied out.
	return true, nil
}
