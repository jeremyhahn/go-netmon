package backup

import (
	"compress/gzip"
	"context"
	"strings"
)

// RestoreOptions selects what a restore applies and how far it is allowed to go.
//
// Every field's zero value is the safe choice: no restore happens without Apply,
// a safety backup is taken unless it is refused, a project mismatch is fatal, and
// a populated MIB source tree is left alone.
type RestoreOptions struct {
	// From is the backup directory to restore.
	From string

	// Apply performs the restore. Without it the plan is printed, nothing is
	// changed, and the call returns a DryRunError.
	Apply bool

	// VerifyOnly checks the archive and stops, touching no Docker object.
	VerifyOnly bool

	// Only, when set, restricts the restore to these components.
	Only []Component

	// Skip omits components. Mutually exclusive with Only.
	Skip []Component

	// NoSafetyBackup skips backing up the current state before overwriting it.
	NoSafetyBackup bool

	// ForceProject allows restoring a backup taken from another compose project.
	ForceProject bool

	// AllowTreeOverwrite permits replacing a MIB source tree that already holds
	// files. It is required in addition to Apply.
	AllowTreeOverwrite bool

	// MIBTree restores the MIB source tree into this directory instead of the
	// one the target compose file bind-mounts.
	MIBTree string
}

// RestoreResult reports what a completed restore changed.
type RestoreResult struct {
	// Restored lists the components applied.
	Restored []Component
	// SafetyBackup is where the previous state was preserved, if it was.
	SafetyBackup string
}

// restorePlan is the resolved, checked shape of a restore.
type restorePlan struct {
	archive    *Archive
	components []Component
	skipped    []Component
	volumes    []volumeTarget
	tree       *TreeLocation
	treeState  treeState
}

// Restore replaces a deployment's data with a backup's.
//
// It is built so that every way it can fail leaves the current data intact. The
// archive is verified in full before any Docker call; the backup's compose
// project must match the target; the target must hold no container this compose
// file did not create and no volume without its label; nothing happens without
// Apply; the current state is preserved first; and the one destination that is
// not a Docker volume - the MIB source tree in the operator's own working copy -
// needs its own opt-in on top of Apply.
func (s *Service) Restore(ctx context.Context, opts RestoreOptions) (*RestoreResult, error) {
	archive, err := s.Verify(opts.From)
	if err != nil {
		return nil, err
	}
	// a root is a directory handle; closing it cannot affect what was read through it.
	defer func() { _ = archive.Close() }()

	if opts.VerifyOnly {
		s.info("--verify-only: nothing was changed")
		return &RestoreResult{}, nil
	}

	plan, err := s.planRestore(ctx, archive, opts)
	if err != nil {
		return nil, err
	}
	s.printPlan(ctx, plan, opts)

	if !opts.Apply {
		return nil, &DryRunError{Dir: archive.Dir}
	}
	if plan.tree != nil && plan.treeState.Files > 0 && !opts.AllowTreeOverwrite {
		incoming, _ := archive.Manifest.Tree(ComponentMIBs)
		return nil, &TreeOverwriteError{
			Dir:      plan.tree.Dir,
			Existing: plan.treeState.Files,
			Incoming: incoming.Files,
		}
	}

	safety, err := s.takeSafetyBackup(ctx, plan, opts)
	if err != nil {
		return nil, err
	}
	if err := s.applyRestore(ctx, plan); err != nil {
		return nil, err
	}

	s.info("starting stack")
	if err := s.cfg.Docker.UpDetached(ctx); err != nil {
		return nil, err
	}
	s.rule()
	s.info("restore complete")
	s.info("  restored: %s", componentNames(plan.components))
	if safety != "" {
		s.info("  previous state saved at: %s", safety)
	}
	s.rule()
	return &RestoreResult{Restored: plan.components, SafetyBackup: safety}, nil
}

// planRestore resolves the restore against the target deployment. Everything
// here is a read: nothing it discovers can leave the deployment half-changed.
func (s *Service) planRestore(ctx context.Context, archive *Archive, opts RestoreOptions) (*restorePlan, error) {
	available := archive.Manifest.Components
	components, err := selectComponents(available, opts.Only, opts.Skip)
	if err != nil {
		return nil, err
	}
	plan := &restorePlan{archive: archive, components: components}
	for _, c := range available {
		if !containsComponent(components, c) {
			plan.skipped = append(plan.skipped, c)
		}
	}
	s.warnSplitPair(components)

	if backupProject := archive.Manifest.Compose.Project; backupProject != s.cfg.Project {
		if !opts.ForceProject {
			return nil, &ProjectMismatchError{Backup: backupProject, Target: s.cfg.Project}
		}
		s.warn("backup was taken from project %q, restoring into %q (--force-project given)",
			backupProject, s.cfg.Project)
	}
	if err := s.assertProjectOwnership(ctx); err != nil {
		return nil, err
	}
	if err := s.resolveRestoreVolumes(ctx, plan); err != nil {
		return nil, err
	}
	if err := s.resolveRestoreTree(ctx, plan, opts); err != nil {
		return nil, err
	}
	s.reportImageDrift(ctx, archive.Manifest)
	return plan, nil
}

// warnSplitPair reports a selection that separates records from the files they
// reference. It is a legitimate thing to want, so it warns rather than refuses.
func (s *Service) warnSplitPair(components []Component) {
	webui := containsComponent(components, ComponentWebUI)
	poller := containsComponent(components, ComponentPoller)
	if webui == poller {
		return
	}
	s.warn("restoring 'webui' and 'poller' separately: device MIB assignments reference")
	s.warn("MIB files in the poller volume, so the two can end up describing different sets")
}

// resolveRestoreVolumes proves every volume the restore needs is defined by the
// target compose file and, where it already exists, carries the project's label.
func (s *Service) resolveRestoreVolumes(ctx context.Context, plan *restorePlan) error {
	defined, err := s.cfg.Docker.ComposeVolumes(ctx)
	if err != nil {
		return err
	}
	for _, c := range plan.components {
		logical := c.Volume()
		if logical == "" {
			continue
		}
		if !containsString(defined, logical) {
			return &VolumeUndefinedError{Component: c, Volume: logical, File: s.cfg.ComposeFile}
		}
		volume := s.volumeFor(c)
		_, exists, err := s.cfg.Docker.VolumeProject(ctx, volume)
		if err != nil {
			return err
		}
		if exists {
			if err := s.assertVolumeOwned(ctx, volume); err != nil {
				return err
			}
		}
		plan.volumes = append(plan.volumes, volumeTarget{component: c, logical: logical, docker: volume})
	}
	return nil
}

// resolveRestoreTree locates where the MIB source tree would land and what is
// already there.
//
// The destination is the TARGET deployment's answer, not the backup's: restoring
// into a different checkout is normal, so it comes from the compose file being
// restored into, and a difference from the recorded origin is reported rather
// than silently followed.
func (s *Service) resolveRestoreTree(ctx context.Context, plan *restorePlan, opts RestoreOptions) error {
	if !containsComponent(plan.components, ComponentMIBs) {
		return nil
	}
	recorded, ok := plan.archive.Manifest.Tree(ComponentMIBs)
	if !ok {
		return &ManifestError{
			Path:   plan.archive.Dir,
			Reason: "lists component 'mibs' but carries no MIB tree member",
		}
	}
	loc, err := s.treeResolver().resolve(ctx, opts.MIBTree)
	if err != nil {
		return err
	}
	state, err := inspectTree(loc)
	if err != nil {
		return err
	}
	plan.tree, plan.treeState = &loc, state
	if recorded.SourcePath != loc.Dir {
		s.warn("MIB tree was backed up from %q and will be restored into %q (%s)",
			recorded.SourcePath, loc.Dir, loc.Origin)
	}
	return nil
}

// reportImageDrift reports, but does not enforce, a difference between the
// images the data was produced by and the images now deployed. Restoring onto a
// newer build is a normal upgrade path; whether a build can read the data is not
// something an image tag can answer, so the operator gets the facts.
func (s *Service) reportImageDrift(ctx context.Context, m *Manifest) {
	services, err := s.cfg.Docker.ComposeServices(ctx)
	if err != nil {
		return
	}
	for _, svc := range services {
		recorded, ok := m.Image(svc.Name)
		if ok && recorded.Image != "" && recorded.Image != svc.Image {
			s.warn("service %q image differs: backup %q, target %q", svc.Name, recorded.Image, svc.Image)
		}
	}
}

// takeSafetyBackup preserves exactly what is about to be destroyed.
//
// 'mibs' is dropped when the destination holds nothing: a backup refuses an
// empty MIB tree, correctly, and there is by definition nothing there to
// preserve. Aborting over it would block precisely the fresh-host recovery this
// tool exists for.
func (s *Service) takeSafetyBackup(ctx context.Context, plan *restorePlan, opts RestoreOptions) (string, error) {
	if opts.NoSafetyBackup {
		s.warn("--no-safety-backup: the current data will not be recoverable after this")
		return "", nil
	}
	var components []Component
	for _, c := range plan.components {
		if c == ComponentMIBs && plan.treeState.Files == 0 {
			s.info("safety backup: omitting 'mibs' (nothing in %s to preserve)", plan.tree.Dir)
			continue
		}
		// Same rule for a volume that does not exist yet, which is the fresh
		// host case: there is nothing there to preserve, and refusing over it
		// would block precisely the recovery this tool exists for.
		if volume := s.volumeFor(c); volume != "" {
			_, exists, err := s.cfg.Docker.VolumeProject(ctx, volume)
			if err != nil {
				return "", &SafetyBackupError{Err: err}
			}
			if !exists {
				s.info("safety backup: omitting %q (volume %s does not exist yet)", c, volume)
				continue
			}
		}
		components = append(components, c)
	}
	if len(components) == 0 {
		s.info("safety backup: nothing to preserve, skipping")
		return "", nil
	}
	name := "pre-restore-" + s.cfg.Now().UTC().Format("20060102T150405Z")
	s.info("taking a safety backup of current state: %s", name)

	treeOverride := ""
	if plan.tree != nil {
		treeOverride = plan.tree.Dir
	}
	result, err := s.Backup(ctx, Options{
		Name:    name,
		Only:    components,
		MIBTree: treeOverride,
	})
	if err != nil {
		return "", &SafetyBackupError{Err: err}
	}
	return result.Dir, nil
}

// applyRestore performs the destructive work, with the stack stopped.
func (s *Service) applyRestore(ctx context.Context, plan *restorePlan) error {
	running, err := s.cfg.Docker.RunningServices(ctx)
	if err != nil {
		return err
	}
	if len(running) > 0 {
		s.info("stopping services: %s", strings.Join(running, ", "))
		if err := s.cfg.Docker.StopServices(ctx, running); err != nil {
			return err
		}
	}
	if err := s.createMissingVolumes(ctx, plan); err != nil {
		return err
	}
	for _, c := range plan.components {
		if err := s.restoreComponent(ctx, plan, c); err != nil {
			return err
		}
	}
	return nil
}

// createMissingVolumes materializes absent volumes through compose, so they are
// created with the project labels the ownership checks depend on. Creating them
// by hand would produce unlabelled volumes this tooling would then refuse.
func (s *Service) createMissingVolumes(ctx context.Context, plan *restorePlan) error {
	for _, target := range plan.volumes {
		_, exists, err := s.cfg.Docker.VolumeProject(ctx, target.docker)
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		s.info("creating missing volumes via compose")
		return s.cfg.Docker.CreateVolumes(ctx)
	}
	return nil
}

func (s *Service) restoreComponent(ctx context.Context, plan *restorePlan, c Component) error {
	switch {
	case c == ComponentMIBs:
		return s.restoreTree(plan)
	case c.Volume() == "":
		return s.restoreConfigFiles(plan)
	default:
		return s.restoreVolume(ctx, plan, c)
	}
}

func (s *Service) restoreVolume(ctx context.Context, plan *restorePlan, c Component) error {
	info, ok := plan.archive.Manifest.Volume(c)
	if !ok {
		return &ComponentAbsentError{Name: c, Available: plan.archive.Manifest.Components}
	}
	volume := s.volumeFor(c)
	if err := s.assertVolumeOwned(ctx, volume); err != nil {
		return err
	}
	s.info("restoring %s -> %s", c, volume)

	f, err := plan.archive.root.Open(info.Member)
	if err != nil {
		return err
	}
	// read-only; a close error cannot change the bytes already read.
	defer func() { _ = f.Close() }()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return &MemberError{Member: info.Member, Check: "decompression", Err: err}
	}
	// a decompressor's close reports nothing a completed read has not.
	defer func() { _ = gz.Close() }()

	return s.cfg.Docker.ImportVolume(ctx, volume, gz)
}

// restoreTree replaces the MIB source tree with the backup's copy.
//
// The replacement is wholesale, matching a volume import and the exact-copy
// semantics of the config files: the directory is emptied, then extracted into.
// Merging would leave modules from a newer tree alongside the restored ones, and
// the deployment would resolve against a set matching neither.
func (s *Service) restoreTree(plan *restorePlan) error {
	info, ok := plan.archive.Manifest.Tree(ComponentMIBs)
	if !ok {
		return &ComponentAbsentError{Name: ComponentMIBs, Available: plan.archive.Manifest.Components}
	}
	dst, base, err := treeRoot(*plan.tree)
	if err != nil {
		return err
	}
	// a root is a directory handle; closing it cannot affect what was written through it.
	defer func() { _ = dst.Close() }()
	s.announce(dst)

	s.info("restoring MIB source tree -> %s", plan.tree.Dir)
	if plan.treeState.Files > 0 {
		s.info("  removing %d existing file(s) first (replacement, not merge)", plan.treeState.Files)
	}

	f, err := plan.archive.root.Open(info.Member)
	if err != nil {
		return err
	}
	// read-only; a close error cannot change the bytes already read.
	defer func() { _ = f.Close() }()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return &MemberError{Member: info.Member, Check: "decompression", Err: err}
	}
	// a decompressor's close reports nothing a completed read has not.
	defer func() { _ = gz.Close() }()

	entries, err := ExtractTree(dst, base, gz)
	if err != nil {
		return err
	}
	if entries != info.Entries {
		return &MemberError{
			Member: info.Member, Check: "restored entry count",
			Expected: itoa(info.Entries), Actual: itoa(entries),
		}
	}
	files, err := dst.CountFiles(base)
	if err != nil {
		return err
	}
	s.info("  %d file(s) now present", files)
	return nil
}

// restoreConfigFiles copies the restorable config files back into the
// repository, through a root anchored on the repository itself.
func (s *Service) restoreConfigFiles(plan *restorePlan) error {
	dst, err := Open("repository config", s.cfg.RepoRoot)
	if err != nil {
		return err
	}
	// a root is a directory handle; closing it cannot affect what was written through it.
	defer func() { _ = dst.Close() }()
	s.announce(dst)

	for _, file := range plan.archive.Manifest.ConfigFiles {
		if !file.Restorable {
			continue
		}
		data, err := plan.archive.root.ReadFile("config/" + file.Path)
		if err != nil {
			if isNotExist(err) {
				s.warn("config file %q recorded but absent from the backup; skipped", file.Path)
				continue
			}
			return err
		}
		s.info("restoring config file: %s", file.Path)
		if err := dst.WriteFile(file.Path, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// --- the plan -------------------------------------------------------------

// printPlan spells out what would be overwritten, before anything is.
func (s *Service) printPlan(ctx context.Context, plan *restorePlan, opts RestoreOptions) {
	m := plan.archive.Manifest
	s.rule()
	s.line("RESTORE PLAN")
	s.rule()
	s.line("  backup:      %s", m.BackupName)
	s.line("  taken:       %s by %s", m.CreatedAt, m.CreatedBy)
	s.line("  consistency: %s", m.Consistency)
	s.line("  project:     %s", s.cfg.Project)
	s.line("")
	s.line("  The following will be OVERWRITTEN:")

	for _, c := range plan.components {
		switch {
		case c == ComponentMIBs:
			s.printTreePlan(plan, opts)
		case c.Volume() == "":
			s.printConfigPlan(plan)
		default:
			s.printVolumePlan(ctx, plan, c)
		}
	}
	if len(plan.skipped) > 0 {
		s.line("")
		s.line("  Left untouched (present in the backup, not selected): %s", componentNames(plan.skipped))
	}
	s.rule()
}

func (s *Service) printTreePlan(plan *restorePlan, opts RestoreOptions) {
	incoming, _ := plan.archive.Manifest.Tree(ComponentMIBs)
	s.line("    tree    %s", plan.tree.Dir)
	s.line("              holds:   %s", ComponentMIBs.Description())
	s.line("              source:  %s", plan.tree.Origin)
	// Spelled out because this is the one destination that is not a Docker
	// volume. An operator scanning the plan must not have to infer it.
	s.line("              NOTE:    this is a HOST DIRECTORY in your working tree,")
	s.line("                       not a Docker volume")
	if plan.treeState.Files == 0 {
		s.line("              current: empty or absent - nothing will be deleted")
		s.line("              restore: %d entries from the backup", incoming.Entries)
		s.line("              gate:    not required (no existing files)")
		return
	}
	s.line("              current: %d files, %s", plan.treeState.Files, humanBytes(plan.treeState.Bytes))
	s.line("              restore: %d entries from the backup", incoming.Entries)
	s.line("                       (the directory is EMPTIED first, then extracted")
	s.line("                       into - existing files are deleted, not merged)")
	if opts.AllowTreeOverwrite {
		s.line("              gate:    --allow-tree-overwrite given")
		return
	}
	s.line("              gate:    REFUSED - replacing %d existing files", plan.treeState.Files)
	s.line("                       requires --allow-tree-overwrite")
}

func (s *Service) printConfigPlan(plan *restorePlan) {
	for _, file := range plan.archive.Manifest.ConfigFiles {
		if file.Restorable {
			s.line("    file    %s/%s", s.cfg.RepoRoot, file.Path)
		}
	}
}

func (s *Service) printVolumePlan(ctx context.Context, plan *restorePlan, c Component) {
	volume := s.volumeFor(c)
	incoming, _ := plan.archive.Manifest.Volume(c)
	s.line("    volume  %s", volume)
	s.line("              holds:   %s", c.Description())
	s.line("              current: %s", s.currentVolumeSize(ctx, volume))
	s.line("              restore: %d entries from the backup (contents replaced entirely)", incoming.Entries)
}

// currentVolumeSize describes a volume's present contents for the plan. It is
// display only, so a runtime that cannot answer is reported rather than fatal.
func (s *Service) currentVolumeSize(ctx context.Context, volume string) string {
	_, exists, err := s.cfg.Docker.VolumeProject(ctx, volume)
	if err != nil || !exists {
		return "does not exist, will be created"
	}
	size, err := s.cfg.Docker.VolumeBytes(ctx, volume)
	if err != nil {
		return "unknown"
	}
	return humanBytes(size)
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
