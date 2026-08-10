package backup_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-netmon/internal/backup"
	"github.com/jeremyhahn/go-netmon/internal/backup/backuptest"
)

func TestBackupCarriesEveryComponent(t *testing.T) {
	h := newHarness(t)
	result := h.backup(backup.Options{})

	m := result.Manifest
	if m.SchemaVersion != backup.ManifestSchema {
		t.Fatalf("schema_version = %d, want %d", m.SchemaVersion, backup.ManifestSchema)
	}
	if m.Consistency != backup.ConsistencyCold {
		t.Fatalf("consistency = %q, want %q", m.Consistency, backup.ConsistencyCold)
	}
	if m.Tool != backup.ToolName {
		t.Fatalf("tool = %q", m.Tool)
	}
	if m.Compose.Project != testProject {
		t.Fatalf("compose.project = %q", m.Compose.Project)
	}
	if m.Compose.FileSHA256 == "" {
		t.Fatal("the compose file's digest was not recorded")
	}
	if len(m.Volumes) != 4 {
		t.Fatalf("volumes[] holds %d members, want 4", len(m.Volumes))
	}
	if len(m.Trees) != 1 {
		t.Fatalf("trees[] holds %d members, want 1", len(m.Trees))
	}
	if len(m.Images) != 4 {
		t.Fatalf("images[] holds %d entries, want 4", len(m.Images))
	}
	if m.Host.DockerVersion == "" || m.Host.ComposeVersion == "" {
		t.Fatalf("host versions were not recorded: %+v", m.Host)
	}

	tree, ok := m.Tree(backup.ComponentMIBs)
	if !ok {
		t.Fatal("the MIB tree is not recorded in trees[]")
	}
	if tree.SourcePath != h.mibs {
		t.Fatalf("tree.source_path = %q, want %q", tree.SourcePath, h.mibs)
	}
	if tree.MountTarget != backup.DefaultMIBMountTarget {
		t.Fatalf("tree.mount_target = %q", tree.MountTarget)
	}
	if tree.Files != 5 {
		t.Fatalf("tree.files = %d, want 5", tree.Files)
	}
	if _, isVolume := m.Volume(backup.ComponentMIBs); isVolume {
		t.Fatal("the host MIB tree must never appear in volumes[]")
	}

	// The archive must verify by the same rules a restore applies to it.
	archive, err := h.svc.Verify(result.Dir)
	if err != nil {
		t.Fatalf("a freshly written backup did not verify: %v", err)
	}
	defer archive.Close()

	// config.yaml is restorable; docker-compose.yml is recorded but is not.
	restorable := map[string]bool{}
	for _, f := range m.ConfigFiles {
		restorable[f.Path] = f.Restorable
	}
	for _, name := range backup.ConfigFiles() {
		if !restorable[name] {
			t.Errorf("%s should be restorable in a full backup", name)
		}
	}
	root := h.archiveRoot(result.Dir)
	if ok, _ := root.Exists("config/reference/docker-compose.yml"); !ok {
		t.Error("the compose file should be recorded for provenance")
	}
	for _, f := range m.ConfigFiles {
		if f.Path == "docker-compose.yml" {
			t.Error("the compose file must never be listed as restorable")
		}
	}
}

func TestBackupStopsAndRestartsExactlyWhatWasRunning(t *testing.T) {
	h := newHarness(t)
	if err := h.engine.StopServices(context.Background(), []string{"grafana"}); err != nil {
		t.Fatalf("pre-stop grafana: %v", err)
	}
	before := h.engine.Running()

	h.backup(backup.Options{})

	after := h.engine.Running()
	if strings.Join(after, ",") != strings.Join(before, ",") {
		t.Fatalf("running services after the backup = %v, want %v", after, before)
	}
	if !h.engine.Called("stop:") {
		t.Fatal("the stack was never stopped; the copy was not cold")
	}
	// grafana was already down and must not be started by a backup.
	for _, call := range h.engine.Calls() {
		if strings.HasPrefix(call, "start:") && strings.Contains(call, "grafana") {
			t.Fatalf("a backup started a service that was not running: %q", call)
		}
	}
}

// TestBackupRestartsTheStackWhenInterrupted is the property that makes a cold
// backup acceptable at all: a backup must never be the reason monitoring stayed
// down, including when the operator hits Ctrl-C halfway through.
func TestBackupRestartsTheStackWhenInterrupted(t *testing.T) {
	h := newHarness(t)
	before := h.engine.Running()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.engine.OnExport = func(_ context.Context, _ string) error {
		// The stack is stopped at this moment. This is the worst possible
		// instant to be interrupted, so it is the one the test picks.
		if len(h.engine.Running()) != 0 {
			t.Error("services were still running while volumes were being copied")
		}
		cancel()
		return ctx.Err()
	}

	_, err := h.svc.Backup(ctx, backup.Options{})
	if err == nil {
		t.Fatal("an interrupted backup should report the interruption")
	}
	after := h.engine.Running()
	if strings.Join(after, ",") != strings.Join(before, ",") {
		t.Fatalf("the stack was left as %v after an interrupted backup, want %v", after, before)
	}
}

func TestBackupKeepStoppedLeavesTheStackDown(t *testing.T) {
	h := newHarness(t)
	h.backup(backup.Options{KeepStopped: true})

	if running := h.engine.Running(); len(running) != 0 {
		t.Fatalf("--keep-stopped left %v running", running)
	}
	if !strings.Contains(h.output(), "leaving the stack stopped") {
		t.Fatal("--keep-stopped should say so")
	}
}

func TestBackupRefusesAForeignComposeProject(t *testing.T) {
	h := newHarness(t)
	h.engine.SetForeignContainers(backup.Container{
		Name:        "somebody-elses-app",
		ConfigFiles: "/srv/other/docker-compose.yml",
	})

	_, err := h.svc.Backup(context.Background(), backup.Options{})
	var foreign *backup.ForeignProjectError
	if !errors.As(err, &foreign) {
		t.Fatalf("want *backup.ForeignProjectError, got %T: %v", err, err)
	}
	if !strings.Contains(foreign.Error(), "somebody-elses-app") {
		t.Fatalf("the refusal should name the container: %v", foreign)
	}
	if h.engine.Called("stop:") {
		t.Fatal("a refused backup must not have stopped anything")
	}
}

func TestBackupRefusesAnUnlabelledVolume(t *testing.T) {
	h := newHarness(t)
	// The deployment host really does carry an unlabelled "go-netmon-data"
	// alongside the project's "go-netmon_go-netmon-data". Only the label tells
	// them apart, so the tooling must refuse anything that does not carry it.
	if err := h.engine.AddVolume(h.volumes[backup.ComponentMetrics], "someone-else"); err != nil {
		t.Fatalf("relabel: %v", err)
	}

	_, err := h.svc.Backup(context.Background(), backup.Options{})
	var ownership *backup.VolumeOwnershipError
	if !errors.As(err, &ownership) {
		t.Fatalf("want *backup.VolumeOwnershipError, got %T: %v", err, err)
	}
	if h.engine.Called("stop:") || h.engine.Called("export:") {
		t.Fatal("a refused backup must not have stopped or read anything")
	}
}

func TestBackupRefusesAMissingVolume(t *testing.T) {
	h := newHarness(t)
	h.engine.SetComposeVolumes("webui-data")

	svc, err := backup.New(backup.Config{
		RepoRoot:    h.repo,
		ComposeFile: h.repo + "/docker-compose.yml",
		Project:     "no-such-project",
		Docker:      h.engine,
		Out:         h.out,
		Now:         h.now,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	_, err = svc.Backup(context.Background(), backup.Options{})
	var missing *backup.VolumeMissingError
	if !errors.As(err, &missing) {
		t.Fatalf("want *backup.VolumeMissingError, got %T: %v", err, err)
	}
}

func TestBackupRefusesAnEmptyMIBTree(t *testing.T) {
	h := newHarness(t)
	if err := h.root.Empty("repo/mibs"); err != nil {
		t.Fatalf("empty the tree: %v", err)
	}

	_, err := h.svc.Backup(context.Background(), backup.Options{})
	var empty *backup.EmptyTreeError
	if !errors.As(err, &empty) {
		t.Fatalf("want *backup.EmptyTreeError, got %T: %v", err, err)
	}
	if h.engine.Called("stop:") {
		t.Fatal("the tree was checked after the stack was stopped; that is an outage for nothing")
	}
}

func TestBackupRefusesAnUnresolvableMIBTree(t *testing.T) {
	h := newHarness(t)
	svc, err := backup.New(backup.Config{
		RepoRoot:       h.repo,
		ComposeFile:    h.repo + "/docker-compose.yml",
		Project:        testProject,
		MIBMountTarget: "/somewhere/nothing/mounts",
		Docker:         h.engine,
		Out:            h.out,
		Now:            h.now,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}

	_, err = svc.Backup(context.Background(), backup.Options{})
	var resolve *backup.TreeResolveError
	if !errors.As(err, &resolve) {
		t.Fatalf("want *backup.TreeResolveError, got %T: %v", err, err)
	}

	// Skipping the component is the documented way out, and it must work.
	if _, err := svc.Backup(context.Background(), backup.Options{Skip: []backup.Component{backup.ComponentMIBs}}); err != nil {
		t.Fatalf("--skip mibs should sidestep the unresolvable tree: %v", err)
	}
}

func TestBackupRefusesConflictingMIBBindMounts(t *testing.T) {
	h := newHarness(t)
	h.engine.AddService("stray", "stray:latest", false,
		backup.Bind{Source: h.base + "/somewhere-else", Target: backup.DefaultMIBMountTarget})

	_, err := h.svc.Backup(context.Background(), backup.Options{})
	var resolve *backup.TreeResolveError
	if !errors.As(err, &resolve) {
		t.Fatalf("want *backup.TreeResolveError, got %T: %v", err, err)
	}
	if len(resolve.Sources) != 2 {
		t.Fatalf("the refusal should list both candidates, got %v", resolve.Sources)
	}
}

func TestBackupOnlyAndSkipSelectComponents(t *testing.T) {
	h := newHarness(t)

	only := h.backup(backup.Options{Name: "only-metrics", Only: []backup.Component{backup.ComponentMetrics}})
	if got := names(only.Manifest.Components); got != "metrics" {
		t.Fatalf("--only metrics produced components %q", got)
	}
	if len(only.Manifest.Trees) != 0 || len(only.Manifest.Volumes) != 1 {
		t.Fatalf("--only metrics carried %d volumes and %d trees", len(only.Manifest.Volumes), len(only.Manifest.Trees))
	}
	for _, f := range only.Manifest.ConfigFiles {
		if f.Restorable {
			t.Fatalf("config files must not be restorable when 'config' was not selected: %+v", f)
		}
	}

	skipped := h.backup(backup.Options{Name: "no-mibs", Skip: []backup.Component{backup.ComponentMIBs, backup.ComponentGrafana}})
	if got := names(skipped.Manifest.Components); got != "config, webui, poller, metrics" {
		t.Fatalf("--skip produced components %q", got)
	}
}

func TestBackupRejectsBadSelections(t *testing.T) {
	h := newHarness(t)

	_, err := h.svc.Backup(context.Background(), backup.Options{
		Only: []backup.Component{backup.ComponentMetrics},
		Skip: []backup.Component{backup.ComponentGrafana},
	})
	var conflict *backup.SelectionConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("want *backup.SelectionConflictError, got %T: %v", err, err)
	}

	_, err = h.svc.Backup(context.Background(), backup.Options{Skip: backup.AllComponents()})
	var empty *backup.EmptySelectionError
	if !errors.As(err, &empty) {
		t.Fatalf("want *backup.EmptySelectionError, got %T: %v", err, err)
	}

	_, err = h.svc.Backup(context.Background(), backup.Options{Only: []backup.Component{backup.Component("mib")}})
	var absent *backup.ComponentAbsentError
	if !errors.As(err, &absent) {
		t.Fatalf("want *backup.ComponentAbsentError for a name that is not a component, got %T: %v", err, err)
	}
}

func TestBackupRefusesAnExistingDestination(t *testing.T) {
	h := newHarness(t)
	first := h.backup(backup.Options{Name: "collide"})

	_, err := h.svc.Backup(context.Background(), backup.Options{Name: "collide"})
	var exists *backup.ExistsError
	if !errors.As(err, &exists) {
		t.Fatalf("want *backup.ExistsError, got %T: %v", err, err)
	}
	if exists.Dir != first.Dir {
		t.Fatalf("the refusal named %q, want %q", exists.Dir, first.Dir)
	}
}

func TestBackupRefusesAnUnusableName(t *testing.T) {
	h := newHarness(t)
	for _, name := range []string{"../escape", "-rf", "with/slash", ""} {
		if name == "" {
			continue
		}
		_, err := h.svc.Backup(context.Background(), backup.Options{Name: name})
		var invalid *backup.InvalidNameError
		if !errors.As(err, &invalid) {
			t.Fatalf("Name=%q: want *backup.InvalidNameError, got %T: %v", name, err, err)
		}
	}
}

func TestBackupWritesAChecksumListCoveringEveryFile(t *testing.T) {
	h := newHarness(t)
	result := h.backup(backup.Options{})
	root := h.archiveRoot(result.Dir)

	data, err := root.ReadFile(backup.ChecksumFile)
	if err != nil {
		t.Fatalf("read checksums: %v", err)
	}
	listed := make(map[string]bool)
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		_, path, ok := strings.Cut(line, "  ")
		if !ok {
			t.Fatalf("checksums.sha256 is not in sha256sum format: %q", line)
		}
		listed[strings.TrimPrefix(path, "./")] = true
	}

	present, err := backuptest.Files(root, ".")
	if err != nil {
		t.Fatalf("list archive: %v", err)
	}
	for path := range present {
		if path == backup.ChecksumFile {
			continue
		}
		if !listed[path] {
			t.Errorf("%s is in the archive but not in checksums.sha256", path)
		}
	}
	if !listed[backup.ManifestFile] {
		t.Error("the manifest must be covered, or an edited manifest goes unnoticed")
	}
	for path := range present {
		if strings.HasSuffix(path, ".partial") {
			t.Errorf("a partial file was left behind: %s", path)
		}
	}
}

func TestBackupReportsUnmanagedLookAlikeVolumes(t *testing.T) {
	h := newHarness(t)
	if err := h.engine.AddVolume("go-netmon-data", ""); err != nil {
		t.Fatalf("add stray volume: %v", err)
	}

	h.backup(backup.Options{})

	out := h.output()
	if !strings.Contains(out, "go-netmon-data  (project=<none>)") {
		t.Fatalf("the unlabelled look-alike should be reported:\n%s", out)
	}
	for _, call := range h.engine.Calls() {
		if strings.HasSuffix(call, ":go-netmon-data") {
			t.Fatalf("the unlabelled volume was operated on: %q", call)
		}
	}
}

func TestBackupToleratesAMissingConfigFile(t *testing.T) {
	h := newHarness(t)
	if err := h.root.Remove("repo/prometheus.yml"); err != nil {
		t.Fatalf("remove config file: %v", err)
	}

	result := h.backup(backup.Options{})
	if len(result.Manifest.ConfigFiles) != 1 {
		t.Fatalf("config_files = %+v, want only the file that exists", result.Manifest.ConfigFiles)
	}
	if !strings.Contains(h.output(), "config file missing, not backed up: prometheus.yml") {
		t.Fatal("a missing config file should be warned about")
	}
}
