// Package loader parses MIB modules and loads their OID metadata into the MIB
// store.
package loader

import (
	"log"
	"os"
	"path/filepath"
	"strings"
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

	// revision counts changes this loader made to the store, so consumers can skip rebuilding when unchanged.
	revision atomic.Uint64

	// progress, when set, is told after each module of a bulk load. It is
	// installed by the startup path before Load runs and not touched again, so
	// it needs no synchronization of its own.
	progress mib.LoadProgress

	// loadMu serializes parsing and the store write: gosmi keeps global state.
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

// SetProgress installs a reporter told after each module of a bulk load; call it before Load, from Load's goroutine.
func (l *Loader) SetProgress(p mib.LoadProgress) {
	l.progress = p
}

// report tells the installed reporter, if there is one, that done of total
// modules are through.
func (l *Loader) report(done, total int, module string) {
	if l.progress != nil {
		l.progress(done, total, module)
	}
}

// Load loads the configured auto-load MIBs; EnsureModule adds modules devices need later.
func (l *Loader) Load() error {
	log.Println("Loading MIB modules...")

	// Load built-in profiles first
	if err := l.profileManager.LoadBuiltInProfiles(); err != nil {
		return &ProfileLoadError{Err: err}
	}
	log.Printf("Loaded %d built-in profiles", len(l.profileManager.ListProfiles()))

	// At startup a changed file can be detected reliably, so reparse only changed modules.
	for i, moduleName := range l.config.AutoLoad {
		if err := l.EnsureModuleCurrent(moduleName); err != nil {
			log.Printf("Warning: failed to load module %s: %v", moduleName, err)
			// Continue loading other modules
		}
		// Reported for the failures too: an operator watching a start needs to
		// see it moving, and a module that would not parse is still a module
		// this loop is done with.
		l.report(i+1, len(l.config.AutoLoad), moduleName)
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

// Revision returns a monotonic counter of store changes by this loader; safe for concurrent use.
func (l *Loader) Revision() uint64 {
	return l.revision.Load()
}

// HasModule reports whether the store already holds moduleName's metadata.
func (l *Loader) HasModule(moduleName string) bool {
	return l.storedModule(moduleName) != nil
}

// EnsureModule loads moduleName unless the store already holds it; it does not check the file on disk (see EnsureModuleCurrent).
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

// EnsureModuleCurrent loads moduleName unless the stored copy is at least as new as its file; startup only, before anything else is parsed.
func (l *Loader) EnsureModuleCurrent(moduleName string) error {
	if l.moduleIsCurrent(moduleName) {
		return nil
	}

	l.loadMu.Lock()
	defer l.loadMu.Unlock()

	if l.moduleIsCurrent(moduleName) {
		return nil
	}

	return l.loadModule(moduleName)
}

// moduleIsCurrent reports whether the store holds moduleName parsed from a file
// that has not been written since.
func (l *Loader) moduleIsCurrent(moduleName string) bool {
	info := l.storedModule(moduleName)
	if info == nil {
		return false
	}
	return !moduleFileIsNewer(info)
}

// storedModule finds the stored record by declared module name, falling back to the file name.
func (l *Loader) storedModule(moduleName string) *mib.ModuleInfo {
	if info, err := l.storage.GetModule(moduleName); err == nil {
		return info
	}

	modules, err := l.storage.ListModules()
	if err != nil {
		return nil
	}
	for _, info := range modules {
		base := filepath.Base(info.Path)
		if info.Path != "" && strings.TrimSuffix(base, filepath.Ext(base)) == moduleName {
			return info
		}
	}
	return nil
}

// moduleFileIsNewer reports whether a module's file was written since it was parsed; unknown path or stat failure counts as not newer.
func moduleFileIsNewer(info *mib.ModuleInfo) bool {
	if info.Path == "" {
		return false
	}

	fi, err := os.Stat(info.Path)
	if err != nil {
		return false
	}

	return fi.ModTime().After(info.LoadedAt)
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

// loadModule is LoadModule with l.loadMu held; it parses before deleting, so a failed parse leaves the store unchanged.
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

	// moduleName is what was asked for -- possibly a file name. The module the
	// file declares is info.Name, and it is the one the parser knows.
	moduleName = info.Name

	oids, report, err := l.parser.ParseModuleWithReport(moduleName)
	if err != nil {
		return nil, &ModuleLoadError{Module: moduleName, Op: "parse", Err: err}
	}

	// Unresolved IMPORTS leave some objects untyped; the module still loads.
	if degraded := report.Err(); degraded != nil {
		log.Printf("Warning: %v", degraded)
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
		// By record rather than by OID: an object with no numeric identifier is
		// stored under module and name, and deleting it by its empty OID
		// removes nothing at all.
		if err := l.storage.DeleteObject(oid); err != nil {
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
