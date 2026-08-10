// Package loader parses MIB modules and loads their OID metadata into the MIB
// store.
package loader

import (
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jeremyhahn/go-netmon/internal/config"
	"github.com/jeremyhahn/go-netmon/internal/mib"
	"github.com/jeremyhahn/go-netmon/internal/mib/parser"
	"github.com/jeremyhahn/go-netmon/internal/mib/profiles"
	"github.com/jeremyhahn/go-netmon/internal/mib/storage"
)

// Loader handles MIB initialization and loading
type Loader struct {
	config         *config.MIBConfig
	storage        *storage.Storage
	parser         *parser.Parser
	profileManager *profiles.Manager

	// revision counts the changes this loader has made to the store's
	// contents. It is what lets a consumer of the store -- the metrics
	// exporter's MIB index above all -- tell "nothing has been loaded since I
	// last looked" from "seventeen modules have", without polling the store
	// or being handed a notification per module.
	revision atomic.Uint64

	// loadMu serializes parsing. The parser is a facade over gosmi, which
	// keeps its loaded modules in package-global state, so two callers loading
	// two *different* modules at once corrupt that state just as surely as two
	// loading the same one. It is held for the parse and the store write
	// together, so a module is never visible half-written to a reader that is
	// about to decide it need not be loaded.
	loadMu sync.Mutex
}

// New creates a new MIB loader
func New(cfg *config.MIBConfig) (*Loader, error) {
	// Initialize storage
	store, err := storage.New(cfg.StoragePath)
	if err != nil {
		return nil, &StoreOpenError{Path: cfg.StoragePath, Err: err}
	}

	// Initialize parser with MIB paths
	mibParser := parser.New(cfg.Paths...)
	if err := mibParser.Init(); err != nil {
		// Unwinding a failed construction; the close error adds nothing to the
		// parser error we are about to return.
		_ = store.Close()
		return nil, &ParserInitError{Err: err}
	}

	// Initialize profile manager
	profileMgr := profiles.NewManager(store)

	return &Loader{
		config:         cfg,
		storage:        store,
		parser:         mibParser,
		profileManager: profileMgr,
	}, nil
}

// Load loads all configured MIBs.
//
// The auto-load list is a bootstrap, not a limit: it is what the poller holds
// before it has been told which devices exist. Modules a device turns out to
// need are brought in afterwards by EnsureModule.
func (l *Loader) Load() error {
	log.Println("Loading MIB modules...")

	// Load built-in profiles first
	if err := l.profileManager.LoadBuiltInProfiles(); err != nil {
		return &ProfileLoadError{Err: err}
	}
	log.Printf("Loaded %d built-in profiles", len(l.profileManager.ListProfiles()))

	// Load auto-load modules
	for _, moduleName := range l.config.AutoLoad {
		if err := l.LoadModule(moduleName); err != nil {
			log.Printf("Warning: failed to load module %s: %v", moduleName, err)
			// Continue loading other modules
			continue
		}
	}

	// Get storage statistics
	oidCount, moduleCount, tableCount, err := l.storage.Count()
	if err != nil {
		return &StoreError{Op: "count stored MIB metadata", Err: err}
	}

	log.Printf("MIB loading complete: %d OIDs, %d modules, %d tables",
		oidCount, moduleCount, tableCount)

	return nil
}

// Revision returns a counter that increases every time this loader changes what
// the MIB store holds. It never decreases and is safe to read concurrently.
//
// Two reads returning the same value mean the store's module set is unchanged
// between them, which is all a consumer needs to skip rebuilding state derived
// from it.
func (l *Loader) Revision() uint64 {
	return l.revision.Load()
}

// HasModule reports whether the store already holds moduleName's metadata.
func (l *Loader) HasModule(moduleName string) bool {
	_, err := l.storage.GetModule(moduleName)
	return err == nil
}

// EnsureModule loads moduleName unless the store already holds it.
//
// This is what lets a device poll a MIB that was assigned to it after the
// process started: assignment reaches the poller as a module name, and the
// module's objects have to be in the store before any of them can be selected.
// A module that is already there is left alone — reparsing it would cost tens
// of milliseconds and produce the same objects.
func (l *Loader) EnsureModule(moduleName string) error {
	// The common case is a module that is already present, and answering that
	// without the lock is what keeps one slow parse from serializing every
	// other caller's presence check.
	if l.HasModule(moduleName) {
		return nil
	}

	l.loadMu.Lock()
	defer l.loadMu.Unlock()

	// Re-check under the lock: several devices can want the same module at the
	// same moment, and only the first of them should pay to parse it.
	if l.HasModule(moduleName) {
		return nil
	}

	return l.loadModule(moduleName)
}

// LoadModule parses a MIB module and replaces whatever the store holds for it.
func (l *Loader) LoadModule(moduleName string) error {
	l.loadMu.Lock()
	defer l.loadMu.Unlock()

	return l.loadModule(moduleName)
}

// parsedModule is one module as the parser describes it, held in memory before
// anything in the store is touched.
type parsedModule struct {
	info   *mib.ModuleInfo
	oids   []*mib.OIDMetadata
	tables []*mib.TableInfo
}

// loadModule is LoadModule with l.loadMu already held.
//
// The parse happens before anything is deleted. Deleting first — as this once
// did — means a module that fails to parse is erased from the store by the
// attempt to refresh it, so a reload that goes wrong costs the operator the
// good copy they already had. Parsing first degrades that to "no change".
func (l *Loader) loadModule(moduleName string) error {
	parsed, err := l.parseModule(moduleName)
	if err != nil {
		return err
	}

	if err := l.replaceModule(parsed); err != nil {
		return err
	}

	// Bumped only after the store write completes, so a consumer that reads
	// the revision and then sweeps the store is guaranteed to see everything
	// that revision accounts for.
	l.revision.Add(1)

	log.Printf("Loaded MIB module %s: %d OIDs, %d tables",
		moduleName, len(parsed.oids), len(parsed.tables))

	return nil
}

// parseModule reads a module from the search path without touching the store.
func (l *Loader) parseModule(moduleName string) (*parsedModule, error) {
	info, err := l.parser.LoadModule(moduleName)
	if err != nil {
		return nil, &ModuleLoadError{Module: moduleName, Op: "read", Err: err}
	}

	oids, err := l.parser.ParseModule(moduleName)
	if err != nil {
		return nil, &ModuleLoadError{Module: moduleName, Op: "parse", Err: err}
	}

	// Table metadata is an enrichment: a module with no tables is perfectly
	// normal, and losing the table descriptions of one that has them still
	// leaves every object usable. So this failure is reported and not fatal.
	tables, err := l.parser.GetTableInfo(moduleName)
	if err != nil {
		log.Printf("Warning: failed to read table metadata for %s: %v", moduleName, err)
		tables = nil
	}

	return &parsedModule{info: info, oids: oids, tables: tables}, nil
}

// replaceModule writes a parsed module over whatever the store holds for it.
func (l *Loader) replaceModule(parsed *parsedModule) error {
	moduleName := parsed.info.Name

	if err := l.clearModule(moduleName); err != nil {
		return err
	}

	if err := l.storage.StoreModule(parsed.info); err != nil {
		return &ModuleLoadError{Module: moduleName, Op: "store", Err: err}
	}

	storedOIDs := 0
	for _, oid := range parsed.oids {
		if err := l.storage.StoreOID(oid); err != nil {
			log.Printf("Warning: failed to store OID %s of %s: %v", oid.OID, moduleName, err)
			continue
		}
		storedOIDs++
	}
	if storedOIDs < len(parsed.oids) {
		log.Printf("Stored %d of %d OIDs from %s", storedOIDs, len(parsed.oids), moduleName)
	}

	for _, table := range parsed.tables {
		if err := l.storage.StoreTable(table); err != nil {
			log.Printf("Warning: failed to store table %s of %s: %v", table.Name, moduleName, err)
		}
	}

	return nil
}

// clearModule removes a module's objects and metadata, leaving the store as if
// the module had never been loaded.
func (l *Loader) clearModule(moduleName string) error {
	if !l.HasModule(moduleName) {
		return nil
	}

	// Read only this module's objects. Scanning the whole store per module made
	// each reload cost the size of everything imported so far, which is what
	// pushed large imports past the request timeout.
	moduleOIDs, err := l.storage.ListModuleOIDs(moduleName)
	if err != nil {
		return &ModuleLoadError{Module: moduleName, Op: "list stored OIDs of", Err: err}
	}

	staleOIDs := 0
	for _, oid := range moduleOIDs {
		if err := l.storage.DeleteOID(oid.OID); err != nil {
			staleOIDs++
		}
	}
	if staleOIDs > 0 {
		log.Printf("Failed to delete %d of %d OIDs for module %s; the reload may leave stale entries",
			staleOIDs, len(moduleOIDs), moduleName)
	}

	if err := l.storage.DeleteModule(moduleName); err != nil {
		log.Printf("Failed to delete module metadata for %s: %v", moduleName, err)
	}

	return nil
}

// GetStorage returns the storage instance as interface{} to avoid import cycles
func (l *Loader) GetStorage() interface{} {
	return l.storage
}

// GetParser returns the parser instance as interface{} to avoid import cycles
func (l *Loader) GetParser() interface{} {
	return l.parser
}

// GetProfileManager returns the profile manager
func (l *Loader) GetProfileManager() *profiles.Manager {
	return l.profileManager
}

// Close closes the loader and all resources
func (l *Loader) Close() error {
	var errs []error

	if l.parser != nil {
		l.parser.Close()
	}

	if l.storage != nil {
		if err := l.storage.Close(); err != nil {
			errs = append(errs, &StoreError{Op: "close MIB store", Err: err})
		}
	}

	if len(errs) > 0 {
		return &CloseError{Errs: errs}
	}

	return nil
}

// GetModuleStatistics returns statistics about a loaded module
func (l *Loader) GetModuleStatistics(moduleName string) (*ModuleStats, error) {
	moduleInfo, err := l.storage.GetModule(moduleName)
	if err != nil {
		return nil, &ModuleNotFoundError{Module: moduleName}
	}

	// Get all OIDs for this module
	oids, err := l.storage.GetPollableOIDs(moduleName)
	if err != nil {
		return nil, &StoreError{Op: "read pollable OIDs of " + moduleName, Err: err}
	}

	// Get tables
	tables, err := l.storage.ListTables(moduleName)
	if err != nil {
		// Tables might not exist
		tables = nil
	}

	return &ModuleStats{
		Name:         moduleInfo.Name,
		TotalOIDs:    moduleInfo.OIDCount,
		PollableOIDs: len(oids),
		Tables:       len(tables),
		LoadedAt:     moduleInfo.LoadedAt,
	}, nil
}

// ListLoadedModules returns all loaded modules
func (l *Loader) ListLoadedModules() ([]string, error) {
	modules, err := l.storage.ListModules()
	if err != nil {
		return nil, &StoreError{Op: "list stored modules", Err: err}
	}

	names := make([]string, len(modules))
	for i, mod := range modules {
		names[i] = mod.Name
	}

	return names, nil
}

// ModuleStats contains statistics about a loaded module
type ModuleStats struct {
	Name         string
	TotalOIDs    int
	PollableOIDs int
	Tables       int
	LoadedAt     time.Time
}
