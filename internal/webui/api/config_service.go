package api

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"time"

	"github.com/jeremyhahn/go-netmon/internal/webui/storage"
)

// The actions a MIB module can take part in during an import.
const (
	MIBActionImport  = "import"  // the bundle carries it and this instance does not have it
	MIBActionRefresh = "refresh" // the bundle carries it and this instance already has it
	MIBActionPresent = "present" // the bundle does not carry it but this instance has it
	MIBActionMissing = "missing" // neither, and the configuration references it
)

// safetyExportPrefix names the export taken immediately before an import is
// applied. It follows scripts/restore.sh, whose safety backup is named
// "pre-restore-<timestamp>" for the same reason: the artifact has to say what it
// is the state before, and when.
const safetyExportPrefix = "pre-import-"

// ImportReport is what an import did, or would do.
//
// The same shape answers a dry run and an apply, so a client renders one thing
// and the only difference an operator sees is whether Applied is set.
type ImportReport struct {
	Applied       bool `json:"applied"`
	SchemaVersion int  `json:"schema_version"`

	// Changed reports whether the configuration would move at all. Importing an
	// instance's own export leaves it false.
	Changed bool `json:"changed"`

	Counts ImportCounts `json:"counts"`

	MIBs     []MIBChange            `json:"mibs"`
	Profiles []storage.ConfigChange `json:"profiles"`
	Devices  []storage.ConfigChange `json:"devices"`

	// SafetyExport is where the configuration that was replaced was written, so
	// a wrong import is recoverable by importing that file back. It is present
	// only on an apply.
	SafetyExport string `json:"safety_export,omitempty"`
}

// MIBChange is what an import does about one MIB module.
type MIBChange struct {
	Module string `json:"module"`
	Action string `json:"action"`
	File   string `json:"file,omitempty"`
}

// ImportCounts summarizes a report.
type ImportCounts struct {
	storage.ConfigCounts
	MIBsImported  int `json:"mibs_imported"`
	MIBsRefreshed int `json:"mibs_refreshed"`
	MIBsPresent   int `json:"mibs_present"`
}

// ExportResult is a configuration bundle together with anything about it the
// operator has to know.
type ExportResult struct {
	Bundle *ConfigBundle

	// MissingSources names modules whose source file could not be read, so the
	// bundle records them as prerequisites instead of carrying them. A restore
	// onto an instance that lacks those modules will fail naming them, which is
	// why the export says so at the time it is taken.
	MissingSources []string
}

// moduleImporter stores one MIB source file and records the module it defines.
type moduleImporter interface {
	importMIBSource(
		ctx context.Context, fileName string, content io.Reader, tags []string, replace bool,
	) (*importedMIB, error)
}

// pollerNotifier brings the poller back in line with the stored configuration.
type pollerNotifier interface {
	syncImportedDevices(ctx context.Context, created, removed []string)
}

// configService is the configuration export and import use case.
//
// It sits between the HTTP handlers and the record store: the handlers deal in
// status codes and content types, this deals in bundles, plans and the order
// operations have to happen in, and the store deals in records. Nothing here
// knows what an HTTP request is, which is what lets the CLI path and the web UI
// path be the same path.
type configService struct {
	store     *storage.Storage
	modules   moduleImporter
	poller    pollerNotifier
	backupDir string
	logger    *slog.Logger
}

// newConfigService wires the use case to the server's collaborators.
func newConfigService(s *Server) *configService {
	return &configService{
		store:     s.storage,
		modules:   s,
		poller:    s,
		backupDir: s.configBackupDir(),
		logger:    s.log(),
	}
}

// export produces a self-contained bundle of this instance's configuration.
func (c *configService) export(
	ctx context.Context, opts storage.ConfigExportOptions,
) (*ExportResult, error) {
	doc, err := c.store.ExportConfig(ctx, opts)
	if err != nil {
		return nil, err
	}

	catalog, err := c.store.ListMIBs(ctx)
	if err != nil {
		return nil, err
	}
	pathByModule := make(map[string]string, len(catalog))
	for _, m := range catalog {
		pathByModule[m.Name] = m.FilePath
	}

	result := &ExportResult{Bundle: &ConfigBundle{Config: doc, MIBFiles: map[string][]byte{}}}
	for i := range doc.MIBs {
		entry := &doc.MIBs[i]
		content, readErr := readMIBSource(pathByModule[entry.Name])
		if readErr != nil {
			c.logger.WarnContext(ctx, "configuration export cannot carry a MIB source file",
				"module", entry.Name, "path", pathByModule[entry.Name], "error", readErr)
			result.MissingSources = append(result.MissingSources, entry.Name)
			continue
		}

		entry.File = path.Join(bundleMIBDir, sourceFileName(entry.Name, pathByModule[entry.Name]))
		entry.SHA256 = checksum(content)
		result.Bundle.MIBFiles[entry.File] = content
	}
	return result, nil
}

// readMIBSource reads one stored module's source file.
func readMIBSource(storedPath string) ([]byte, error) {
	if storedPath == "" {
		return nil, &MIBUploadError{
			Op: "read source", Path: "", Err: os.ErrNotExist,
		}
	}
	// storedPath was written by this server when the module was imported.
	content, err := os.ReadFile(storedPath) // #nosec G304 -- the path is this server's own record of where it saved the module
	if err != nil {
		return nil, &MIBUploadError{Op: "read source", Path: storedPath, Err: err}
	}
	return content, nil
}

// sourceFileName names a module's file inside the bundle, keeping the name it
// was uploaded under so a round trip does not rename anything on disk.
func sourceFileName(module, storedPath string) string {
	if base := filepath.Base(storedPath); base != "." && base != string(filepath.Separator) {
		return base
	}
	return module
}

// plan reports what importing a bundle would do, changing nothing.
func (c *configService) plan(ctx context.Context, bundle *ConfigBundle) (*ImportReport, error) {
	prepared, err := c.prepare(ctx, bundle)
	if err != nil {
		return nil, err
	}
	return prepared.report(false), nil
}

// apply imports a bundle, replacing this instance's configuration with it.
//
// The order is deliberate and is the whole safety argument:
//
//  1. Validate everything — the document's own fields, the bundle's checksums,
//     and every module the configuration references. Nothing is written yet, so
//     a document that is going to be rejected has cost nothing.
//  2. Export the current configuration to a file, so the change is reversible.
//     If that fails the import stops, exactly as scripts/restore.sh refuses to
//     overwrite data it could not back up first.
//  3. Import the MIB source files. This is additive: it creates modules the
//     instance lacks and refreshes ones it has, and deletes nothing. A failure
//     here leaves extra modules imported and the configuration untouched, which
//     changes nothing about what is polled.
//  4. Replace the configuration in one store batch, all-or-nothing.
//  5. Push the result to the poller so it matches storage without a restart.
//
// The destructive step is last and atomic; every step before it is either
// read-only or additive and harmless if interrupted.
func (c *configService) apply(ctx context.Context, bundle *ConfigBundle) (*ImportReport, error) {
	prepared, err := c.prepare(ctx, bundle)
	if err != nil {
		return nil, err
	}

	safetyPath, err := c.writeSafetyExport(ctx)
	if err != nil {
		return nil, err
	}

	if importErr := c.importSources(ctx, prepared); importErr != nil {
		return nil, importErr
	}

	plan, err := c.store.ImportConfig(ctx, prepared.doc)
	if err != nil {
		return nil, err
	}
	prepared.plan = plan

	c.poller.syncImportedDevices(ctx, changedNames(plan.Devices, storage.ConfigActionCreate),
		changedNames(plan.Devices, storage.ConfigActionDelete))

	report := prepared.report(true)
	report.SafetyExport = safetyPath
	return report, nil
}

// preparedImport is a validated bundle with everything the apply needs.
type preparedImport struct {
	doc      *storage.Config
	sources  map[string]bundleSource
	mibs     []MIBChange
	plan     *storage.ConfigPlan
	imported int
	replaced int
}

// prepare validates a bundle and works out both halves of the plan.
func (c *configService) prepare(ctx context.Context, bundle *ConfigBundle) (*preparedImport, error) {
	if bundle == nil || bundle.Config == nil {
		return nil, &BundleFormatError{Reason: "no configuration document was supplied"}
	}
	if err := storage.ValidateConfig(bundle.Config); err != nil {
		return nil, err
	}

	sources, err := verifyBundleSources(bundle)
	if err != nil {
		return nil, err
	}

	catalog, err := c.store.ListMIBs(ctx)
	if err != nil {
		return nil, err
	}
	imported := make(map[string]struct{}, len(catalog))
	for _, m := range catalog {
		imported[m.Name] = struct{}{}
	}

	mibs, missing := planModules(bundle.Config, sources, imported)
	if len(missing) > 0 {
		return nil, &storage.ConfigMIBMissingError{Modules: missing}
	}

	plan, err := c.store.PlanImportConfig(ctx, bundle.Config, carriedModules(sources))
	if err != nil {
		return nil, err
	}

	return &preparedImport{doc: bundle.Config, sources: sources, mibs: mibs, plan: plan}, nil
}

// planModules decides what happens to each module and lists the ones this
// instance cannot supply.
//
// Only the modules the configuration actually assigns can be missing. A document
// may list the whole catalog of the instance it came from as a prerequisite; the
// ones nothing references are not needed to poll anything and are left alone
// rather than demanded.
func planModules(
	doc *storage.Config, sources map[string]bundleSource, imported map[string]struct{},
) (changes []MIBChange, missing []string) {
	referenced := doc.ReferencedModules()

	for _, entry := range doc.MIBs {
		source, carried := sources[entry.Name]
		_, present := imported[entry.Name]

		switch {
		case carried && present:
			changes = append(changes, MIBChange{
				Module: entry.Name, Action: MIBActionRefresh, File: source.fileName,
			})
		case carried:
			changes = append(changes, MIBChange{
				Module: entry.Name, Action: MIBActionImport, File: source.fileName,
			})
		case !slices.Contains(referenced, entry.Name):
			// Not carried, not needed: saying anything about it would bury the
			// entries that matter under the whole catalog of the source.
		case present:
			changes = append(changes, MIBChange{Module: entry.Name, Action: MIBActionPresent})
		default:
			changes = append(changes, MIBChange{Module: entry.Name, Action: MIBActionMissing})
			missing = append(missing, entry.Name)
		}
	}

	// A module a device or profile assigns without the document listing it at
	// all is still a prerequisite, and still has to be there.
	for _, module := range referenced {
		if _, listed := sources[module]; listed {
			continue
		}
		if containsModule(changes, module) {
			continue
		}
		if _, present := imported[module]; present {
			changes = append(changes, MIBChange{Module: module, Action: MIBActionPresent})
			continue
		}
		changes = append(changes, MIBChange{Module: module, Action: MIBActionMissing})
		missing = append(missing, module)
	}

	sort.Slice(changes, func(i, j int) bool { return changes[i].Module < changes[j].Module })
	sort.Strings(missing)
	return changes, missing
}

// containsModule reports whether a module already has a planned action.
func containsModule(changes []MIBChange, module string) bool {
	for _, c := range changes {
		if c.Module == module {
			return true
		}
	}
	return false
}

// carriedModules names the modules a bundle brings with it, in order.
func carriedModules(sources map[string]bundleSource) []string {
	modules := make([]string, 0, len(sources))
	for module := range sources {
		modules = append(modules, module)
	}
	sort.Strings(modules)
	return modules
}

// importSources imports every MIB source the bundle carries, before any
// configuration references it.
//
// Modules are imported with replace semantics, so one that is already here has
// its parsed objects refreshed in place and keeps its ID, its tags and every
// assignment pointing at it.
func (c *configService) importSources(ctx context.Context, prepared *preparedImport) error {
	done := make([]string, 0, len(prepared.sources))
	for _, module := range carriedModules(prepared.sources) {
		source := prepared.sources[module]

		outcome, err := c.modules.importMIBSource(
			ctx, source.fileName, bytes.NewReader(source.content), source.tags, true)
		if err != nil {
			return &ConfigMIBImportError{Module: module, Imported: done, Err: err}
		}

		done = append(done, module)
		if outcome.replaced {
			prepared.replaced++
		} else {
			prepared.imported++
		}
	}

	c.logger.InfoContext(ctx, "configuration import loaded MIB sources",
		"imported", prepared.imported, "refreshed", prepared.replaced)
	return nil
}

// writeSafetyExport saves the configuration about to be replaced.
//
// It is a whole bundle rather than a note, so recovering from a wrong import is
// one command against one file and needs nothing else to be present.
func (c *configService) writeSafetyExport(ctx context.Context) (string, error) {
	result, err := c.export(ctx, storage.ConfigExportOptions{})
	if err != nil {
		return "", &ConfigSafetyExportError{Reason: "exporting the current configuration", Err: err}
	}

	if err := os.MkdirAll(c.backupDir, 0o750); err != nil {
		return "", &ConfigSafetyExportError{Reason: "creating " + c.backupDir, Err: err}
	}
	name := safetyExportPrefix + time.Now().UTC().Format("20060102T150405Z") + ".tar.gz"
	full := filepath.Join(c.backupDir, name)

	var archive bytes.Buffer
	if err := writeConfigBundle(&archive, result.Bundle); err != nil {
		return "", &ConfigSafetyExportError{Reason: "packing " + full, Err: err}
	}
	if err := os.WriteFile(full, archive.Bytes(), 0o600); err != nil {
		return "", &ConfigSafetyExportError{Reason: "writing " + full, Err: err}
	}

	c.logger.InfoContext(ctx, "saved the configuration an import is about to replace",
		"path", full, "bytes", archive.Len())
	return full, nil
}

// report renders the prepared import for a caller.
func (p *preparedImport) report(applied bool) *ImportReport {
	counts := ImportCounts{ConfigCounts: p.plan.Counts}
	for _, change := range p.mibs {
		switch change.Action {
		case MIBActionImport:
			counts.MIBsImported++
		case MIBActionRefresh:
			counts.MIBsRefreshed++
		case MIBActionPresent:
			counts.MIBsPresent++
		}
	}

	return &ImportReport{
		Applied:       applied,
		SchemaVersion: p.doc.SchemaVersion,
		Changed:       p.plan.Changed(),
		Counts:        counts,
		MIBs:          p.mibs,
		Profiles:      p.plan.Profiles,
		Devices:       p.plan.Devices,
	}
}

// changedNames lists the records a plan gives one action to.
func changedNames(changes []storage.ConfigChange, action string) []string {
	var names []string
	for _, change := range changes {
		if change.Action == action {
			names = append(names, change.Name)
		}
	}
	return names
}

// configBackupDir is where safety exports are written.
//
// It defaults to a directory beside the record store, which on a deployed
// instance is the web UI's own data volume: the safety copy then lives with the
// data it protects, survives a container being recreated, and is picked up by
// the physical backup without any extra configuration.
func (s *Server) configBackupDir() string {
	if s.configBackups != "" {
		return s.configBackups
	}
	return filepath.Join(filepath.Dir(s.dbPath), "config-backups")
}

// syncImportedDevices brings the poller back in line with the configuration an
// import has just written.
//
// It re-reads the stored devices rather than trusting the plan, because a device
// the plan did not mention can still have a different module set after an
// import: its profile's assignments may have changed underneath it. Pushing
// every device is what makes the poller consistent with storage, and it is the
// same push a single edit through the API performs, so there is one way a
// device reaches the poller and not two.
func (s *Server) syncImportedDevices(ctx context.Context, created, removed []string) {
	if s.pollerClient == nil {
		return
	}

	for _, name := range removed {
		s.callPoller(ctx, name, func(callCtx context.Context) error {
			return s.pollerClient.RemoveDevice(callCtx, name)
		})
	}

	devices, err := s.storage.ListDevices(ctx)
	if err != nil {
		s.log().WarnContext(ctx, "cannot push imported devices to the poller", "error", err)
		return
	}

	isNew := make(map[string]struct{}, len(created))
	for _, name := range created {
		isNew[name] = struct{}{}
	}

	for i := range devices {
		device := &devices[i]
		modules, mibErr := s.getDeviceModules(ctx, device.ID)
		if mibErr != nil {
			s.log().WarnContext(ctx, "cannot resolve a device's modules after an import",
				"device", device.Name, "error", mibErr)
			continue
		}

		_, fresh := isNew[device.Name]
		s.callPoller(ctx, device.Name, func(callCtx context.Context) error {
			if fresh {
				return s.pollerClient.RegisterDevice(callCtx, device, modules.Modules, modules.Filters)
			}
			return s.pollerClient.UpdateDevice(callCtx, device, modules.Modules, modules.Filters)
		})
	}
}

// callPoller runs one best-effort push inside the poller sync budget.
//
// The outcome is logged rather than returned: the configuration is already
// durable, and the poller picks up anything missed on its next configuration
// sync. Bounding each call is what stops a poller that is not answering from
// turning an import of forty devices into a request that never returns.
func (s *Server) callPoller(ctx context.Context, device string, call func(context.Context) error) {
	callCtx, cancel := context.WithTimeout(ctx, PollerSyncBudget)
	defer cancel()

	if err := call(callCtx); err != nil {
		s.log().WarnContext(ctx, "pushing a device to the poller after an import failed",
			"device", device, "error", err)
	}
}

// ConfigSafetyExportError reports that the configuration an import was about to
// replace could not be saved first, which is why the import did not proceed.
type ConfigSafetyExportError struct {
	Reason string
	Err    error
}

func (e *ConfigSafetyExportError) Error() string {
	return "config import: " + e.Reason + ": " + e.Err.Error() +
		"; nothing was changed, because a configuration that cannot be saved " +
		"must not be replaced"
}

func (e *ConfigSafetyExportError) Unwrap() error { return e.Err }

// ConfigMIBImportError reports a MIB source in a bundle that could not be
// imported, and says exactly how far the import got.
//
// The modules it names as imported are still imported. That is deliberate and
// harmless: a module nothing references polls nothing, and the configuration —
// the part that decides what is collected — was not touched.
type ConfigMIBImportError struct {
	Module   string
	Imported []string
	Err      error
}

func (e *ConfigMIBImportError) Error() string {
	return "config import: MIB module " + strconv.Quote(e.Module) + " could not be imported: " +
		e.Err.Error() + "; " + strconv.Itoa(len(e.Imported)) +
		" module(s) were imported before it and remain imported, " +
		"but the configuration was not changed"
}

func (e *ConfigMIBImportError) Unwrap() error { return e.Err }
