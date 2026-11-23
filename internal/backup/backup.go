package backup

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"time"
)

// Options selects what a backup covers and where it lands.
type Options struct {
	// OutputDir is the parent directory backups are created in. Empty means
	// <repo>/backups.
	OutputDir string

	// Name is the backup directory's name. Empty means netmon-<UTC timestamp>.
	Name string

	// Only, when set, restricts the backup to these components.
	Only []Component

	// Skip omits components. Mutually exclusive with Only.
	Skip []Component

	// KeepStopped leaves the stack down afterwards instead of starting exactly
	// what was running.
	KeepStopped bool

	// MIBTree backs up this directory as the MIB source tree instead of the one
	// the compose file bind-mounts. A restore passes the destination it is about
	// to empty, so the safety copy is of what is actually at risk.
	MIBTree string
}

// Result reports what a completed backup produced.
type Result struct {
	// Dir is the backup directory.
	Dir string
	// Manifest describes everything the directory holds.
	Manifest *Manifest
	// Downtime is how long the stack was stopped for.
	Downtime time.Duration
}

// backupPlan is the resolved, checked shape of a backup, computed before
// anything is created or stopped.
type backupPlan struct {
	components []Component
	volumes    []volumeTarget
	tree       *TreeLocation
	treeState  treeState
	dir        string
	name       string
}

type volumeTarget struct {
	component Component
	logical   string
	docker    string
}

// Backup stops the stack, copies its data at rest and starts again exactly what
// was running.
//
// Everything that can fail is proved first: the compose project's ownership,
// every volume's label, and the MIB source tree's location and contents. A
// backup that discovers a problem after taking the stack down has caused an
// outage for nothing.
func (s *Service) Backup(ctx context.Context, opts Options) (*Result, error) {
	plan, err := s.planBackup(ctx, opts)
	if err != nil {
		return nil, err
	}

	s.info("project:    %s", s.cfg.Project)
	s.info("compose:    %s", s.cfg.ComposeFile)
	s.info("components: %s", componentNames(plan.components))
	s.info("destination: %s", plan.dir)

	outputRoot, err := Ensure("backup destination parent", filepath.Dir(plan.dir))
	if err != nil {
		return nil, err
	}
	// Closing a root handle releases a descriptor; it cannot affect what was
	// written through it.
	defer func() { _ = outputRoot.Close() }()

	dst, err := Create("backup destination", plan.dir)
	if err != nil {
		return nil, err
	}
	// a root is a directory handle; closing it cannot affect what was written through it.
	defer func() { _ = dst.Close() }()
	s.announce(dst)

	// The logical export reads the web UI over HTTP, so it has to happen while
	// the stack is still up — before runBackup stops anything.
	logical := s.exportLogical(ctx, dst)

	manifest, downtime, err := s.runBackup(ctx, plan, dst, opts.KeepStopped)
	if err != nil {
		return nil, err
	}
	manifest.Logical = logical

	encoded, err := encodeManifest(manifest)
	if err != nil {
		return nil, err
	}
	if writeFileErr := dst.WriteFile(ManifestFile, encoded, 0o644); writeFileErr != nil {
		return nil, writeFileErr
	}
	if writeChecksumsErr := writeChecksums(dst); writeChecksumsErr != nil {
		return nil, writeChecksumsErr
	}

	total, err := dst.SizeOf(".")
	if err != nil {
		return nil, err
	}
	s.rule()
	s.info("backup complete: %s", plan.dir)
	s.info("  consistency: %s (stack stopped during the copy)", ConsistencyCold)
	s.info("  components:  %s", componentNames(plan.components))
	s.info("  downtime:    %s", downtime.Round(time.Millisecond))
	s.info("  total size:  %s", humanBytes(total))
	s.rule()

	return &Result{Dir: plan.dir, Manifest: manifest, Downtime: downtime}, nil
}

// planBackup resolves and proves everything the backup depends on, before any
// output exists and before anything is stopped.
func (s *Service) planBackup(ctx context.Context, opts Options) (*backupPlan, error) {
	components, err := selectComponents(AllComponents(), opts.Only, opts.Skip)
	if err != nil {
		return nil, err
	}
	if assertProjectOwnershipErr := s.assertProjectOwnership(ctx); assertProjectOwnershipErr != nil {
		return nil, assertProjectOwnershipErr
	}

	plan := &backupPlan{components: components}
	for _, c := range components {
		volume := s.volumeFor(c)
		if volume == "" {
			continue
		}
		if assertVolumeOwnedErr := s.assertVolumeOwned(ctx, volume); assertVolumeOwnedErr != nil {
			return nil, assertVolumeOwnedErr
		}
		plan.volumes = append(plan.volumes, volumeTarget{component: c, logical: c.Volume(), docker: volume})
	}
	s.reportUnmanagedVolumes(ctx)

	if containsComponent(components, ComponentMIBs) {
		loc, resolveErr := s.treeResolver().resolve(ctx, opts.MIBTree)
		switch {
		case resolveErr == nil:
			state, inspectErr := inspectTree(loc)
			if inspectErr != nil {
				return nil, inspectErr
			}
			if state.Files == 0 {
				return nil, &EmptyTreeError{Dir: loc.Dir}
			}
			plan.tree, plan.treeState = &loc, state
			s.info("mib tree:   %s (%d files, %s)", loc.Dir, state.Files, loc.Origin)

		case isTreeNotMounted(resolveErr):
			// A deployment that mounts no MIB tree keeps its modules inside the
			// volume the poller writes them to, and that volume is already
			// backed up as a volume. There is nothing separate to copy, so the
			// component is dropped rather than treated as a missing dependency
			// — which is what it was before imports were stored internally.
			//
			// An ambiguous mount stays fatal: two services naming different
			// host directories at the same target means the deployment does not
			// agree with itself about where its MIBs are, and guessing would
			// pick one of them to empty on a restore.
			s.info("mib tree:   not mounted; MIB sources live in the %s volume and are covered by it",
				ComponentPoller.Volume())
			components = withoutComponent(components, ComponentMIBs)
			plan.components = components

		default:
			return nil, resolveErr
		}
	}

	plan.name = opts.Name
	if plan.name == "" {
		plan.name = "netmon-" + s.cfg.Now().UTC().Format("20060102T150405Z")
	}
	if !validName(plan.name) {
		return nil, &InvalidNameError{Kind: "backup", Name: plan.name}
	}
	output := opts.OutputDir
	if output == "" {
		output = s.DefaultBackupDir()
	}
	resolved, err := ResolveDir("backup destination", output)
	if err != nil {
		return nil, err
	}
	plan.dir = filepath.Join(resolved, plan.name)
	return plan, nil
}

// runBackup performs the stopped-window work and returns the manifest.
//
// The stack is returned to exactly the set of services that were running, on
// every exit path: an error, a panic, or a canceled context. A backup must
// never be the reason monitoring stayed down.
func (s *Service) runBackup(ctx context.Context, plan *backupPlan, dst *Root, keepStopped bool) (*Manifest, time.Duration, error) {
	running, err := s.cfg.Docker.RunningServices(ctx)
	if err != nil {
		return nil, 0, err
	}

	stopped := s.cfg.Now()
	if len(running) > 0 {
		s.info("stopping services for a consistent copy: %s", strings.Join(running, ", "))
		if stopServicesErr := s.cfg.Docker.StopServices(ctx, running); stopServicesErr != nil {
			return nil, 0, stopServicesErr
		}
	} else {
		s.info("no running services; copying data as it is")
	}

	restarted := false
	restart := func() {
		if restarted {
			return
		}
		restarted = true
		if keepStopped {
			s.warn("--keep-stopped: leaving the stack stopped")
			return
		}
		if len(running) == 0 {
			return
		}
		s.info("starting services again: %s", strings.Join(running, ", "))
		// Deliberately detached from ctx: an interrupted or timed-out backup
		// must still put the stack back, and a canceled context would cancel
		// the very command that does it.
		restartCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
		defer cancel()
		if startServicesErr := s.cfg.Docker.StartServices(restartCtx, running); startServicesErr != nil {
			s.warn("could not start services again: %v", startServicesErr)
		}
	}
	defer restart()

	manifest, err := s.copyAll(ctx, plan, dst)
	restart()
	downtime := s.cfg.Now().Sub(stopped)
	if err != nil {
		return nil, downtime, err
	}

	if fillManifestErr := s.fillManifest(ctx, manifest, plan); fillManifestErr != nil {
		return nil, downtime, fillManifestErr
	}
	return manifest, downtime, nil
}

// copyAll writes every member of the backup while the stack is stopped.
func (s *Service) copyAll(ctx context.Context, plan *backupPlan, dst *Root) (*Manifest, error) {
	manifest := &Manifest{
		SchemaVersion: ManifestSchema,
		BackupName:    plan.name,
		CreatedAt:     s.cfg.Now().UTC().Format("2006-01-02T15:04:05Z"),
		CreatedBy:     s.cfg.CreatedBy,
		Tool:          ToolName,
		Consistency:   ConsistencyCold,
		Components:    plan.components,
		Logical:       LogicalInfo{Status: "skipped", Counts: map[string]int{}},
	}

	if len(plan.volumes) > 0 {
		if err := dst.MkdirAll("volumes", 0o755); err != nil {
			return nil, err
		}
	}
	for _, target := range plan.volumes {
		info, err := s.copyVolume(ctx, dst, target)
		if err != nil {
			return nil, err
		}
		manifest.Volumes = append(manifest.Volumes, info)
	}

	if plan.tree != nil {
		info, err := s.copyTree(dst, plan)
		if err != nil {
			return nil, err
		}
		manifest.Trees = append(manifest.Trees, info)
	}

	files, err := s.copyConfigFiles(dst, containsComponent(plan.components, ComponentConfig))
	if err != nil {
		return nil, err
	}
	manifest.ConfigFiles = files
	return manifest, nil
}

// copyVolume streams one Docker volume into the backup as a compressed member.
//
// Members are named after the component rather than the compose volume: a
// filename that cannot be told apart from an object the tooling refuses to touch
// is a filename waiting to be misread during a recovery.
func (s *Service) copyVolume(ctx context.Context, dst *Root, target volumeTarget) (VolumeInfo, error) {
	member := "volumes/" + string(target.component) + ".tar.gz"
	s.info("archiving %s -> %s", target.docker, member)

	bytesWritten, sum, err := writeMember(dst, member, func(w io.Writer) error {
		return s.cfg.Docker.ExportVolume(ctx, target.docker, w)
	})
	if err != nil {
		return VolumeInfo{}, err
	}
	entries, err := countMemberEntries(dst, member)
	if err != nil {
		return VolumeInfo{}, &MemberError{Member: member, Check: "readback", Err: err}
	}
	s.info("  %d entries, %s compressed", entries, humanBytes(bytesWritten))

	return VolumeInfo{
		Component:     target.component,
		ComposeVolume: target.logical,
		DockerVolume:  target.docker,
		Member:        member,
		Bytes:         bytesWritten,
		Entries:       entries,
		SHA256:        sum,
	}, nil
}

// copyTree streams the MIB source tree into the backup.
//
// It is archived exactly like a volume - own member, own entry count, own
// checksum - because it restores like one: as a whole directory, replaced
// outright. It is recorded in trees[] rather than volumes[] so that nothing can
// mistake a host path for a Docker volume during a recovery.
func (s *Service) copyTree(dst *Root, plan *backupPlan) (TreeInfo, error) {
	src, base, err := treeRoot(*plan.tree)
	if err != nil {
		return TreeInfo{}, err
	}
	// a root is a directory handle; closing it cannot affect what was read through it.
	defer func() { _ = src.Close() }()
	s.announce(src)

	member := "trees/mibs.tar.gz"
	if mkdirAllErr := dst.MkdirAll("trees", 0o755); mkdirAllErr != nil {
		return TreeInfo{}, mkdirAllErr
	}
	s.info("archiving %s -> %s", plan.tree.Dir, member)

	var entries int
	bytesWritten, sum, err := writeMember(dst, member, func(w io.Writer) error {
		n, writeTreeErr := WriteTree(src, base, w)
		entries = n
		return writeTreeErr
	})
	if err != nil {
		return TreeInfo{}, err
	}
	readback, err := countMemberEntries(dst, member)
	if err != nil {
		return TreeInfo{}, &MemberError{Member: member, Check: "readback", Err: err}
	}
	if readback != entries {
		return TreeInfo{}, &MemberError{
			Member: member, Check: "entry count",
			Expected: itoa(entries), Actual: itoa(readback),
		}
	}
	s.info("  %d entries, %d files, %s compressed", entries, plan.treeState.Files, humanBytes(bytesWritten))

	return TreeInfo{
		Component:   ComponentMIBs,
		SourcePath:  plan.tree.Dir,
		MountTarget: plan.tree.MountTarget,
		Member:      member,
		Bytes:       bytesWritten,
		Entries:     entries,
		Files:       plan.treeState.Files,
		SourceBytes: plan.treeState.Bytes,
		SHA256:      sum,
	}, nil
}

// copyConfigFiles copies the bind-mounted repository files into the backup.
//
// Reference files are recorded for provenance and compatibility checking only.
// Overwriting a deployment definition beneath a running deployment is a
// deployment action, not a data restore, so they are never restorable.
func (s *Service) copyConfigFiles(dst *Root, restorable bool) ([]ConfigInfo, error) {
	src, err := Open("repository config", s.cfg.RepoRoot)
	if err != nil {
		return nil, err
	}
	// a root is a directory handle; closing it cannot affect what was read through it.
	defer func() { _ = src.Close() }()

	if mkdirAllErr := dst.MkdirAll("config/reference", 0o755); mkdirAllErr != nil {
		return nil, mkdirAllErr
	}

	var recorded []ConfigInfo
	for _, name := range ConfigFiles() {
		info, err := s.copyOneConfigFile(src, dst, "config/"+name, name, restorable)
		if err != nil {
			return nil, err
		}
		if info != nil {
			recorded = append(recorded, *info)
		}
	}
	for _, name := range ReferenceFiles() {
		if _, err := s.copyOneConfigFile(src, dst, "config/reference/"+name, name, false); err != nil {
			return nil, err
		}
	}
	return recorded, nil
}

func (s *Service) copyOneConfigFile(src, dst *Root, member, name string, restorable bool) (*ConfigInfo, error) {
	data, err := src.ReadFile(name)
	if err != nil {
		if isNotExist(err) {
			s.warn("config file missing, not backed up: %s", name)
			return nil, nil
		}
		return nil, err
	}
	if writeFileErr := dst.WriteFile(member, data, 0o644); writeFileErr != nil {
		return nil, writeFileErr
	}
	sum, _, err := digestFile(dst, member)
	if err != nil {
		return nil, err
	}
	return &ConfigInfo{Path: name, SHA256: sum, Restorable: restorable}, nil
}

// fillManifest records the deployment shape the backup was taken from. It runs
// after the stack is back up, because none of it needs the stack to be down.
func (s *Service) fillManifest(ctx context.Context, m *Manifest, _ *backupPlan) error {
	volumes, err := s.cfg.Docker.ComposeVolumes(ctx)
	if err != nil {
		return err
	}
	services, err := s.cfg.Docker.ComposeServices(ctx)
	if err != nil {
		return err
	}
	for _, svc := range services {
		id, imageIDErr := s.cfg.Docker.ImageID(ctx, svc.Image)
		if imageIDErr != nil {
			return imageIDErr
		}
		m.Images = append(m.Images, ImageInfo{Service: svc.Name, Image: svc.Image, ImageID: id})
	}
	sum, err := s.composeFileDigest()
	if err != nil {
		return err
	}
	m.Compose = ComposeInfo{
		Project:    s.cfg.Project,
		File:       filepath.Base(s.cfg.ComposeFile),
		FileSHA256: sum,
		Volumes:    volumes,
	}
	versions, err := s.cfg.Docker.Versions(ctx)
	if err != nil {
		return err
	}
	m.Host = HostInfo{DockerVersion: versions.Docker, ComposeVersion: versions.Compose}
	return nil
}

func (s *Service) composeFileDigest() (string, error) {
	dir, base := filepath.Split(s.cfg.ComposeFile)
	root, err := Open("compose file directory", strings.TrimSuffix(dir, string(filepath.Separator)))
	if err != nil {
		return "", err
	}
	// a root is a directory handle; closing it cannot affect what was read through it.
	defer func() { _ = root.Close() }()
	sum, _, err := digestFile(root, base)
	return sum, err
}
