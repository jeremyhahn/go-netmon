// Package storage persists MIB metadata — OIDs, modules and tables — in an
// embedded Pebble key-value store.
package storage

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/cockroachdb/pebble"

	"github.com/jeremyhahn/go-netmon/internal/mib"
)

// Storage provides persistent storage for MIB metadata using PebbleDB
type Storage struct {
	db   *pebble.DB
	path string
	mu   sync.RWMutex
}

// Prefixes for different key types.
//
// The module index prefixes deliberately do not begin with "mod", so that they
// sort outside the "mod:" range that ListModules and Count scan: a module
// record and 29,000 index entries sharing a scan range would corrupt both the
// module listing and the module count.
const (
	PrefixOID    = "oid:"
	PrefixModule = "mod:"
	PrefixTable  = "tbl:"
	PrefixName   = "name:"

	// PrefixModuleOID indexes a module's OID keys as
	// "midx:<module>:<oid key>" -> "<oid key>", where the OID key is the
	// suffix of the record's primary "oid:" key. It makes a per-module
	// lookup cost O(module) instead of O(store).
	PrefixModuleOID = "midx:"

	// PrefixModuleIndexed marks a module whose index has been built from a
	// full scan, as "midxdone:<module>". It only matters for a module with
	// no OIDs at all, which is indistinguishable from an unindexed module
	// by the index alone.
	PrefixModuleIndexed = "midxdone:"
)

// indexedMarker is the value stored under PrefixModuleIndexed. Only the
// presence of the key carries meaning.
var indexedMarker = []byte{1}

// New creates a new MIB storage instance
func New(path string) (*Storage, error) {
	db, err := pebble.Open(path, &pebble.Options{
		Cache:        pebble.NewCache(64 << 20), // 64MB cache
		MaxOpenFiles: 500,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to open pebble db: %w", err)
	}

	return &Storage{
		db:   db,
		path: path,
	}, nil
}

// Close closes the storage
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

// oidKeyValue returns the key a record is stored under, minus the prefix.
//
// OIDs with a numeric identifier are keyed by it. Objects without one (groups,
// notifications) are keyed by module and name, which is still unique.
func oidKeyValue(meta *mib.OIDMetadata) string {
	if meta.OID != "" {
		return meta.OID
	}
	return meta.Module + ":" + meta.Name
}

// moduleOIDIndexKey returns the index key binding an OID key to its module.
func moduleOIDIndexKey(module, oidKey string) []byte {
	return []byte(PrefixModuleOID + module + ":" + oidKey)
}

// moduleIndexedKey returns the key marking a module's index as built.
func moduleIndexedKey(module string) []byte {
	return []byte(PrefixModuleIndexed + module)
}

// StoreOID stores OID metadata, its name mapping, and its module index entry.
//
// All three keys are written in one batch so a reader never sees a record
// without its index entry.
func (s *Storage) StoreOID(meta *mib.OIDMetadata) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("failed to marshal OID metadata: %w", err)
	}

	keyValue := oidKeyValue(meta)

	batch := s.db.NewBatch()
	// Close only releases the batch's memory, committed or not.
	defer func() { _ = batch.Close() }()

	if err := batch.Set([]byte(PrefixOID+keyValue), data, nil); err != nil {
		return &WriteError{Op: "store OID " + keyValue, Cause: err}
	}
	// Store by name for reverse lookup.
	nameKey := []byte(PrefixName + meta.Module + ":" + meta.Name)
	if err := batch.Set(nameKey, []byte(keyValue), nil); err != nil {
		return &WriteError{Op: "store name mapping for " + keyValue, Cause: err}
	}
	if err := batch.Set(moduleOIDIndexKey(meta.Module, keyValue), []byte(keyValue), nil); err != nil {
		return &IndexError{Module: meta.Module, Op: "write", Cause: err}
	}

	if err := batch.Commit(pebble.Sync); err != nil {
		return &WriteError{Op: "store OID " + keyValue, Cause: err}
	}

	return nil
}

// DeleteObject removes the record stored for meta, whichever key it was stored
// under.
//
// DeleteOID cannot reach an object that has no numeric OID. Those are keyed by
// module and name -- groups, notifications, and anything a module defines that
// resolved to no identifier -- so deleting by an empty OID looks up a key that
// was never written, finds nothing, and leaves the record in place. Reloading a
// module therefore accumulated one orphan per such object, every time, and no
// re-import could clear them because the clear had the same blind spot.
func (s *Storage) DeleteObject(meta *mib.OIDMetadata) error {
	if meta == nil {
		return nil
	}
	return s.DeleteOID(oidKeyValue(meta))
}

// GetOID retrieves OID metadata by OID
func (s *Storage) GetOID(oid string) (*mib.OIDMetadata, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.getOIDLocked(oid)
}

// getOIDLocked reads a single OID record. The caller holds s.mu.
func (s *Storage) getOIDLocked(oidKey string) (*mib.OIDMetadata, error) {
	data, closer, err := s.db.Get([]byte(PrefixOID + oidKey))
	if err != nil {
		if err == pebble.ErrNotFound {
			return nil, fmt.Errorf("OID %s not found", oidKey)
		}
		return nil, fmt.Errorf("failed to get OID: %w", err)
	}
	// closer only unpins the value, which has already been copied out.
	defer func() { _ = closer.Close() }()

	var meta mib.OIDMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("failed to unmarshal OID metadata: %w", err)
	}

	return &meta, nil
}

// GetOIDByName retrieves OID metadata by module and name
func (s *Storage) GetOIDByName(module, name string) (*mib.OIDMetadata, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// First get the OID from name mapping
	nameKey := []byte(PrefixName + module + ":" + name)
	oidBytes, closer, err := s.db.Get(nameKey)
	if err != nil {
		if err == pebble.ErrNotFound {
			return nil, fmt.Errorf("OID name %s::%s not found", module, name)
		}
		return nil, fmt.Errorf("failed to get name mapping: %w", err)
	}
	oidKey := string(oidBytes)
	_ = closer.Close() // closer only unpins the value, which has already been copied out.

	// Then get the full metadata by OID
	return s.getOIDLocked(oidKey)
}

// ListOIDs lists all OIDs, optionally restricted to a subtree.
//
// The prefix matches whole sub-identifiers: "1.3.6.1.2.1.2" selects that node
// and everything beneath it, but not the sibling subtree "1.3.6.1.2.1.20". A
// plain string prefix would match both. An empty prefix returns every OID.
//
// Use ListModuleOIDs to select the objects of one module: filtering the result
// of ListOIDs("") deserializes the whole store to answer a question about a
// fraction of it.
func (s *Storage) ListOIDs(oidPrefix string) ([]*mib.OIDMetadata, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.listOIDsLocked(oidPrefix)
}

// listOIDsLocked implements ListOIDs. The caller holds s.mu.
func (s *Storage) listOIDsLocked(oidPrefix string) ([]*mib.OIDMetadata, error) {
	lower, upper := oidScanBounds(oidPrefix)

	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: lower,
		UpperBound: upper,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create iterator: %w", err)
	}
	// Iteration has finished; a close error cannot change the rows already read.
	defer func() { _ = iter.Close() }()

	var oids []*mib.OIDMetadata
	for iter.First(); iter.Valid(); iter.Next() {
		var meta mib.OIDMetadata
		if err := json.Unmarshal(iter.Value(), &meta); err != nil {
			continue // Skip entries that are not valid OID metadata.
		}
		oids = append(oids, &meta)
	}

	if err := iter.Error(); err != nil {
		return nil, fmt.Errorf("iterator error: %w", err)
	}

	return oids, nil
}

// ListModuleOIDs returns every object belonging to one module.
//
// The module index answers this without touching the rest of the store, so the
// cost tracks the size of the module rather than the size of the store. An
// unknown module yields no objects and no error; an empty module name is a
// programming error and is reported as one.
func (s *Storage) ListModuleOIDs(module string) ([]*mib.OIDMetadata, error) {
	if module == "" {
		return nil, &ModuleRequiredError{Op: "ListModuleOIDs"}
	}

	s.mu.RLock()
	oids, indexed, err := s.listModuleOIDsLocked(module)
	s.mu.RUnlock()
	if err != nil {
		return nil, err
	}
	if indexed {
		return oids, nil
	}

	// No index for this module: this store predates the index.
	return s.backfillModuleIndex(module)
}

// listModuleOIDsLocked reads a module's objects through the index and reports
// whether the index covers the module at all. The caller holds s.mu.
func (s *Storage) listModuleOIDsLocked(module string) ([]*mib.OIDMetadata, bool, error) {
	keys, err := s.moduleOIDKeysLocked(module)
	if err != nil {
		return nil, false, err
	}

	if len(keys) == 0 {
		// Empty is ambiguous: either the module has no objects or it was
		// never indexed. The marker distinguishes the two.
		indexed, err := s.moduleIndexedLocked(module)
		return nil, indexed, err
	}

	oids := make([]*mib.OIDMetadata, 0, len(keys))
	for _, key := range keys {
		meta, err := s.getOIDLocked(key)
		if err != nil {
			// A dangling index entry must not hide the rest of the module.
			continue
		}
		oids = append(oids, meta)
	}

	return oids, true, nil
}

// moduleOIDKeysLocked returns the OID keys the index holds for a module.
func (s *Storage) moduleOIDKeysLocked(module string) ([]string, error) {
	lower := PrefixModuleOID + module + ":"

	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte(lower),
		UpperBound: []byte(prefixSuccessor(lower)),
	})
	if err != nil {
		return nil, &IndexError{Module: module, Op: "scan", Cause: err}
	}
	// Iteration has finished; a close error cannot change the rows already read.
	defer func() { _ = iter.Close() }()

	var keys []string
	for iter.First(); iter.Valid(); iter.Next() {
		keys = append(keys, string(iter.Value()))
	}
	if err := iter.Error(); err != nil {
		return nil, &IndexError{Module: module, Op: "scan", Cause: err}
	}

	return keys, nil
}

// moduleIndexedLocked reports whether a full scan has already indexed a module.
func (s *Storage) moduleIndexedLocked(module string) (bool, error) {
	_, closer, err := s.db.Get(moduleIndexedKey(module))
	if err != nil {
		if err == pebble.ErrNotFound {
			return false, nil
		}
		return false, &IndexError{Module: module, Op: "read marker", Cause: err}
	}
	_ = closer.Close() // closer only unpins the value, which has already been copied out.

	return true, nil
}

// backfillModuleIndex builds the module index from one full scan of the store
// and returns the requested module's objects.
//
// Deployments upgraded in place hold OID records written before the index
// existed, and re-importing 170 MIBs to recover the index is not an acceptable
// upgrade step. The scan deserializes every record anyway, so it indexes every
// module it sees rather than only the one asked for: a single scan repairs the
// whole store, no matter which module triggered it. The requested module is
// marked even when it has no objects, so that a module which is genuinely
// empty does not force a rescan on every call.
func (s *Storage) backfillModuleIndex(module string) ([]*mib.OIDMetadata, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Another caller may have rebuilt the index while this one waited.
	oids, indexed, err := s.listModuleOIDsLocked(module)
	if err != nil {
		return nil, err
	}
	if indexed {
		return oids, nil
	}

	all, err := s.listOIDsLocked("")
	if err != nil {
		return nil, err
	}

	batch := s.db.NewBatch()
	// Close only releases the batch's memory, committed or not.
	defer func() { _ = batch.Close() }()

	matched := make([]*mib.OIDMetadata, 0)
	modules := map[string]bool{module: true}
	for _, meta := range all {
		key := oidKeyValue(meta)
		if err := batch.Set(moduleOIDIndexKey(meta.Module, key), []byte(key), nil); err != nil {
			return nil, &IndexError{Module: meta.Module, Op: "backfill", Cause: err}
		}
		modules[meta.Module] = true

		if meta.Module == module {
			matched = append(matched, meta)
		}
	}

	for name := range modules {
		if err := batch.Set(moduleIndexedKey(name), indexedMarker, nil); err != nil {
			return nil, &IndexError{Module: name, Op: "backfill", Cause: err}
		}
	}

	if err := batch.Commit(pebble.Sync); err != nil {
		return nil, &IndexError{Module: module, Op: "backfill", Cause: err}
	}

	return matched, nil
}

// oidScanBounds returns the Pebble key range covering an OID subtree.
//
// Keys under prefix P are exactly P or start with P+"." — and since '.' (0x2E)
// sorts below every digit, the half-open range [P, P+"/") captures precisely
// those and excludes siblings such as P+"0". With no prefix the range spans the
// whole OID keyspace.
func oidScanBounds(oidPrefix string) (lower, upper []byte) {
	if oidPrefix == "" {
		return []byte(PrefixOID), []byte(prefixSuccessor(PrefixOID))
	}
	key := PrefixOID + oidPrefix
	return []byte(key), []byte(key + "/")
}

// prefixSuccessor returns the smallest string greater than every string having
// the given prefix, by incrementing its final byte.
func prefixSuccessor(prefix string) string {
	b := []byte(prefix)
	b[len(b)-1]++
	return string(b)
}

// DeleteOID deletes an OID, its name mapping, and its module index entry.
func (s *Storage) DeleteOID(oid string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Get metadata first to find name mapping (don't call GetOID to avoid deadlock)
	oidKey := []byte(PrefixOID + oid)
	data, closer, err := s.db.Get(oidKey)
	if err != nil {
		if err == pebble.ErrNotFound {
			return fmt.Errorf("OID %s not found", oid)
		}
		return fmt.Errorf("failed to get OID: %w", err)
	}

	var meta mib.OIDMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		_ = closer.Close() // closer only unpins the value, which has already been copied out.
		return fmt.Errorf("failed to unmarshal OID metadata: %w", err)
	}
	_ = closer.Close() // closer only unpins the value, which has already been copied out.

	batch := s.db.NewBatch()
	// Close only releases the batch's memory, committed or not.
	defer func() { _ = batch.Close() }()

	if err := batch.Delete(oidKey, nil); err != nil {
		return &WriteError{Op: "delete OID " + oid, Cause: err}
	}
	nameKey := []byte(PrefixName + meta.Module + ":" + meta.Name)
	if err := batch.Delete(nameKey, nil); err != nil {
		return &WriteError{Op: "delete name mapping for " + oid, Cause: err}
	}
	// The index key is derived from the same key the record is stored under,
	// which is exactly the argument received here.
	if err := batch.Delete(moduleOIDIndexKey(meta.Module, oid), nil); err != nil {
		return &IndexError{Module: meta.Module, Op: "delete", Cause: err}
	}

	if err := batch.Commit(pebble.Sync); err != nil {
		return &WriteError{Op: "delete OID " + oid, Cause: err}
	}

	return nil
}

// StoreModule stores module information
func (s *Storage) StoreModule(info *mib.ModuleInfo) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.Marshal(info)
	if err != nil {
		return fmt.Errorf("failed to marshal module info: %w", err)
	}

	key := []byte(PrefixModule + info.Name)
	if err := s.db.Set(key, data, pebble.Sync); err != nil {
		return fmt.Errorf("failed to store module: %w", err)
	}

	return nil
}

// GetModule retrieves module information
func (s *Storage) GetModule(name string) (*mib.ModuleInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	key := []byte(PrefixModule + name)
	data, closer, err := s.db.Get(key)
	if err != nil {
		if err == pebble.ErrNotFound {
			return nil, fmt.Errorf("module %s not found", name)
		}
		return nil, fmt.Errorf("failed to get module: %w", err)
	}
	// closer only unpins the value, which has already been copied out.
	defer func() { _ = closer.Close() }()

	var info mib.ModuleInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, fmt.Errorf("failed to unmarshal module info: %w", err)
	}

	return &info, nil
}

// ListModules lists all loaded modules
func (s *Storage) ListModules() ([]*mib.ModuleInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var modules []*mib.ModuleInfo

	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte(PrefixModule),
		UpperBound: []byte(prefixSuccessor(PrefixModule)),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create iterator: %w", err)
	}
	// Iteration has finished; a close error cannot change the rows already read.
	defer func() { _ = iter.Close() }()

	for iter.First(); iter.Valid(); iter.Next() {
		var info mib.ModuleInfo
		if err := json.Unmarshal(iter.Value(), &info); err != nil {
			continue // Skip invalid entries
		}

		modules = append(modules, &info)
	}

	if err := iter.Error(); err != nil {
		return nil, fmt.Errorf("iterator error: %w", err)
	}

	return modules, nil
}

// DeleteModule deletes a module
func (s *Storage) DeleteModule(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := []byte(PrefixModule + name)
	if err := s.db.Delete(key, pebble.Sync); err != nil {
		return fmt.Errorf("failed to delete module: %w", err)
	}

	return nil
}

// StoreTable stores table information
func (s *Storage) StoreTable(table *mib.TableInfo) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.Marshal(table)
	if err != nil {
		return fmt.Errorf("failed to marshal table info: %w", err)
	}

	key := []byte(PrefixTable + table.Module + ":" + table.Name)
	if err := s.db.Set(key, data, pebble.Sync); err != nil {
		return fmt.Errorf("failed to store table: %w", err)
	}

	return nil
}

// GetTable retrieves table information
func (s *Storage) GetTable(module, name string) (*mib.TableInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	key := []byte(PrefixTable + module + ":" + name)
	data, closer, err := s.db.Get(key)
	if err != nil {
		if err == pebble.ErrNotFound {
			return nil, fmt.Errorf("table %s::%s not found", module, name)
		}
		return nil, fmt.Errorf("failed to get table: %w", err)
	}
	// closer only unpins the value, which has already been copied out.
	defer func() { _ = closer.Close() }()

	var table mib.TableInfo
	if err := json.Unmarshal(data, &table); err != nil {
		return nil, fmt.Errorf("failed to unmarshal table info: %w", err)
	}

	return &table, nil
}

// ListTables lists all tables in a module
func (s *Storage) ListTables(module string) ([]*mib.TableInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var tables []*mib.TableInfo
	keyPrefix := PrefixTable + module + ":"

	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte(keyPrefix),
		UpperBound: []byte(prefixSuccessor(keyPrefix)),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create iterator: %w", err)
	}
	// Iteration has finished; a close error cannot change the rows already read.
	defer func() { _ = iter.Close() }()

	for iter.First(); iter.Valid(); iter.Next() {
		var table mib.TableInfo
		if err := json.Unmarshal(iter.Value(), &table); err != nil {
			continue // Skip invalid entries
		}

		tables = append(tables, &table)
	}

	if err := iter.Error(); err != nil {
		return nil, fmt.Errorf("iterator error: %w", err)
	}

	return tables, nil
}

// GetPollableOIDs returns all pollable OIDs for a module.
//
// An empty module means every module, mirroring ListOIDs(""). Matching on
// Module == "" instead would never hit, because every stored OID carries a
// module name, so callers asking for everything would get nothing.
func (s *Storage) GetPollableOIDs(module string) ([]*mib.OIDMetadata, error) {
	oids, err := s.moduleOrAllOIDs(module)
	if err != nil {
		return nil, err
	}

	pollable := make([]*mib.OIDMetadata, 0)
	for _, oid := range oids {
		if oid.IsPollable() {
			pollable = append(pollable, oid)
		}
	}

	return pollable, nil
}

// moduleOrAllOIDs returns one module's objects, or every object when no module
// is named.
func (s *Storage) moduleOrAllOIDs(module string) ([]*mib.OIDMetadata, error) {
	if module == "" {
		return s.ListOIDs("")
	}
	return s.ListModuleOIDs(module)
}

// Count returns statistics about stored data
func (s *Storage) Count() (oids, modules, tables int, err error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if oids, err = s.countPrefix(PrefixOID); err != nil {
		return 0, 0, 0, err
	}
	if modules, err = s.countPrefix(PrefixModule); err != nil {
		return 0, 0, 0, err
	}
	if tables, err = s.countPrefix(PrefixTable); err != nil {
		return 0, 0, 0, err
	}

	return oids, modules, tables, nil
}

// countPrefix reports how many keys share a prefix.
//
// A read failure part way through an iteration ends it just like exhaustion
// does, so the error has to be checked: without it a store that cannot be read
// reports a count of zero and no error, which reads as an empty store.
//
// The caller holds s.mu.
func (s *Storage) countPrefix(prefix string) (int, error) {
	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte(prefix),
		UpperBound: []byte(prefixSuccessor(prefix)),
	})
	if err != nil {
		return 0, fmt.Errorf("failed to create iterator: %w", err)
	}
	defer func() {
		// Iteration has finished; a close error cannot change the rows read.
		_ = iter.Close()
	}()

	count := 0
	for iter.First(); iter.Valid(); iter.Next() {
		count++
	}

	if err := iter.Error(); err != nil {
		return 0, fmt.Errorf("iterator error: %w", err)
	}

	return count, nil
}

// GetMIBColumns returns all table columns for a MIB module
func (s *Storage) GetMIBColumns(moduleName string) ([]mib.OIDMetadata, error) {
	return s.selectModuleOIDs(moduleName, func(oid *mib.OIDMetadata) bool {
		return oid.IsColumn
	})
}

// GetScalars returns only scalar objects (not in tables) for a MIB
func (s *Storage) GetScalars(moduleName string) ([]mib.OIDMetadata, error) {
	return s.selectModuleOIDs(moduleName, func(oid *mib.OIDMetadata) bool {
		return oid.ObjectClass == mib.ObjectClassScalar
	})
}

// GetMetricColumns returns chartable columns (Counter/Gauge types)
func (s *Storage) GetMetricColumns(moduleName string) ([]mib.OIDMetadata, error) {
	return s.selectModuleOIDs(moduleName, func(oid *mib.OIDMetadata) bool {
		return oid.ObjectClass == mib.ObjectClassMetricColumn
	})
}

// GetStatusColumns returns displayable text/enum columns
func (s *Storage) GetStatusColumns(moduleName string) ([]mib.OIDMetadata, error) {
	return s.selectModuleOIDs(moduleName, func(oid *mib.OIDMetadata) bool {
		return oid.ObjectClass == mib.ObjectClassStatusColumn
	})
}

// selectModuleOIDs returns the objects of one module that satisfy a predicate.
func (s *Storage) selectModuleOIDs(moduleName string, keep func(*mib.OIDMetadata) bool) ([]mib.OIDMetadata, error) {
	oids, err := s.ListModuleOIDs(moduleName)
	if err != nil {
		return nil, err
	}

	var selected []mib.OIDMetadata
	for _, oid := range oids {
		if keep(oid) {
			selected = append(selected, *oid)
		}
	}

	return selected, nil
}

// GetTableColumns returns all columns for a specific table OID.
//
// The table OID does not identify a module, so this reads the whole store.
func (s *Storage) GetTableColumns(tableOID string) ([]mib.OIDMetadata, error) {
	allOIDs, err := s.ListOIDs("")
	if err != nil {
		return nil, err
	}

	var columns []mib.OIDMetadata
	for _, oid := range allOIDs {
		if oid.TableOID == tableOID {
			columns = append(columns, *oid)
		}
	}

	return columns, nil
}
