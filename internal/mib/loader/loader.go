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

	// revision counts the changes this loader has made to the store's
	// contents. It is what lets a consumer of the store -- the metrics
	// exporter's MIB index above all -- tell "nothing has been loaded since I
	// last looked" from "seventeen modules have", without polling the store
	// or being handed a notification per module.
	revision atomic.Uint64

	// progress, when set, is told after each module of a bulk load. It is
	// installed by the startup path before Load runs and not touched again, so
	// it needs no synchronization of its own.
	progress mib.LoadProgress

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

// SetProgress installs a reporter told after each module of a bulk load.
//
// Call it before Load, from the goroutine that will call Load: it is for a
// startup path that has somewhere to report progress to, and a loader already
// serving requests is past the point where anything would listen.
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

	// EnsureModuleCurrent rather than LoadModule. Load runs once, at startup,
	// which is the one point at which asking whether a file has changed since
	// it was parsed gives a trustworthy answer, and on a warm store the answer
	// is no for every module.
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
	return l.storedModule(moduleName) != nil
}

// EnsureModule loads moduleName unless the store already holds it.
//
// This is what lets a device poll a MIB that was assigned to it after the
// process started: assignment reaches the poller as a module name, and the
// module's objects have to be in the store before any of them can be selected.
// A module that is already there is left alone — reparsing it would cost tens
// of milliseconds and produce the same objects.
//
// It deliberately does not look at the file on disk. Once this process has
// parsed anything, gosmi holds the outcome for the life of the process and only
// Parser.Reload clears it, so a stale-file check here would reparse into the
// cached result and stamp the store as current — burying the operator's edit
// rather than picking it up. Startup, where the cache is empty, is the one
// place that check is sound, and EnsureModuleCurrent is where it lives.
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

// EnsureModuleCurrent loads moduleName unless the store already holds a copy at
// least as new as the file it was parsed from. IT IS A STARTUP OPERATION and
// must be called before this process has parsed anything else; see below.
//
// The store outlives the process. Every module a service has ever imported is
// still in it after a restart, so reparsing them all is work with no result:
// the same files produce the same objects. That reparse is the whole of the web
// UI's cold start — 203 seconds on monitor-01 across 176 modules, all of it with
// nothing listening. Skipping what has not changed reduces a restart to opening
// the store.
//
// A MIB file edited on disk still takes effect on the next start, which is what
// the unconditional reparse bought and what this has to keep: a file written
// since the parse that produced the stored copy is parsed again.
//
// The precondition is gosmi's, not this package's. Loading a module caches its
// outcome for the life of the process — that is what Parser.Reload exists to
// undo — so once anything has been parsed, "the file changed" no longer implies
// "a reparse will read the file". At startup the cache is empty and everything
// that enters it comes from the files as they are now, so the check is sound
// exactly there.
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

// storedModule finds the stored record for a requested name. The store is
// keyed by the declared module name, but a request can name the file instead
// (iDRAC-SMIv2 for IDRAC-MIB-SMIv2); without the fallback such a module is
// never found and is parsed again on every start.
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

// moduleFileIsNewer reports whether the file a stored module was parsed from
// has been written since that parse.
//
// Anything it cannot answer counts as not newer, and both cases are deliberate.
// A module with no recorded path was not read from a file this process can
// find. A path that will not stat is a file that has been moved or removed, so
// reparsing it could only fail — and because loadModule parses before it
// writes, a failure would leave the store exactly as it is. The stored copy is
// then the best answer available, and returning it beats logging a failure per
// module on every start.
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

	// moduleName is what was asked for -- possibly a file name. The module the
	// file declares is info.Name, and it is the one the parser knows.
	moduleName = info.Name

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
