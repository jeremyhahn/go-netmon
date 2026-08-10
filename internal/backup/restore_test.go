package backup_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-netmon/internal/backup"
	"github.com/jeremyhahn/go-netmon/internal/backup/backuptest"
)

// restoreFixture takes a full backup, then wrecks the deployment: every volume
// gets different contents, the MIB tree gets a module the backup does not have
// and loses one it does, and the config files are edited. What the backup held
// is returned so a test can prove the restore put exactly that back.
type restoreFixture struct {
	*harness
	dir      string
	wantMIBs map[string]string
	wantVols map[backup.Component]map[string]string
	wantYAML string
}

func newRestoreFixture(t *testing.T) *restoreFixture {
	t.Helper()
	h := newHarness(t)

	f := &restoreFixture{
		harness:  h,
		wantMIBs: h.mibFiles(),
		wantVols: make(map[backup.Component]map[string]string),
		wantYAML: "poller:\n  interval: 60s\n",
	}
	for _, c := range volumeComponents() {
		f.wantVols[c] = h.volumeFiles(c)
	}
	f.dir = h.backup(backup.Options{Name: "reference"}).Dir

	for _, c := range volumeComponents() {
		if err := h.engine.Seed(h.volumes[c], map[string]string{
			"damage/oops.txt": "written after the backup",
		}); err != nil {
			t.Fatalf("damage volume: %v", err)
		}
	}
	if err := backuptest.Seed(h.root, "repo/mibs", map[string]string{
		"vendor/new/NEW-MIB.txt": "added after the backup\n",
	}); err != nil {
		t.Fatalf("add a mib: %v", err)
	}
	if err := h.root.Remove("repo/mibs/standard/IF-MIB.txt"); err != nil {
		t.Fatalf("remove a mib: %v", err)
	}
	if err := h.root.WriteFile("repo/config.yaml", []byte("poller:\n  interval: 5s\n"), 0o644); err != nil {
		t.Fatalf("edit config: %v", err)
	}
	// The reference backup stopped and started the stack. Forget that, so the
	// assertions below are about what the restore under test did.
	h.engine.ResetCalls()
	return f
}

func (f *restoreFixture) restore(opts backup.RestoreOptions) (*backup.RestoreResult, error) {
	if opts.From == "" {
		opts.From = f.dir
	}
	return f.svc.Restore(context.Background(), opts)
}

func TestRestoreRoundTripsEveryComponent(t *testing.T) {
	f := newRestoreFixture(t)

	result, err := f.restore(backup.RestoreOptions{Apply: true, AllowTreeOverwrite: true})
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if names(result.Restored) != "config, mibs, webui, poller, metrics, grafana" {
		t.Fatalf("restored %q", names(result.Restored))
	}

	assertSameFiles(t, "MIB source tree", f.wantMIBs, f.mibFiles())
	for _, c := range volumeComponents() {
		assertSameFiles(t, string(c)+" volume", f.wantVols[c], f.volumeFiles(c))
	}

	got, err := f.root.ReadFile("repo/config.yaml")
	if err != nil {
		t.Fatalf("read restored config: %v", err)
	}
	if string(got) != f.wantYAML {
		t.Fatalf("config.yaml = %q, want %q", got, f.wantYAML)
	}
	if result.SafetyBackup == "" {
		t.Fatal("a restore should preserve the state it replaced")
	}
	if running := f.engine.Running(); len(running) != 4 {
		t.Fatalf("the stack was left as %v", running)
	}
}

// TestRestoreVerifiesBeforeItTouchesAnything is the ordering the whole design
// depends on: a damaged archive must be found before the stack is stopped and
// long before a volume is emptied it cannot then be refilled from.
func TestRestoreVerifiesBeforeItTouchesAnything(t *testing.T) {
	damage := map[string]func(t *testing.T, root *backup.Root){
		"a member's bytes changed": func(t *testing.T, root *backup.Root) {
			if err := root.WriteFile("volumes/metrics.tar.gz", []byte("not a gzip stream"), 0o644); err != nil {
				t.Fatalf("damage: %v", err)
			}
		},
		"a member truncated": func(t *testing.T, root *backup.Root) {
			data, err := root.ReadFile("volumes/webui.tar.gz")
			if err != nil {
				t.Fatalf("read member: %v", err)
			}
			if err := root.WriteFile("volumes/webui.tar.gz", data[:len(data)/2], 0o644); err != nil {
				t.Fatalf("truncate: %v", err)
			}
		},
		"a member removed": func(t *testing.T, root *backup.Root) {
			if err := root.Remove("trees/mibs.tar.gz"); err != nil {
				t.Fatalf("remove member: %v", err)
			}
		},
		"the manifest edited": func(t *testing.T, root *backup.Root) {
			data, err := root.ReadFile(backup.ManifestFile)
			if err != nil {
				t.Fatalf("read manifest: %v", err)
			}
			edited := strings.Replace(string(data), `"consistency": "cold-offline"`, `"consistency": "hot"`, 1)
			if err := root.WriteFile(backup.ManifestFile, []byte(edited), 0o644); err != nil {
				t.Fatalf("edit manifest: %v", err)
			}
		},
		"the checksum list removed": func(t *testing.T, root *backup.Root) {
			if err := root.Remove(backup.ChecksumFile); err != nil {
				t.Fatalf("remove checksums: %v", err)
			}
		},
		"an extra file smuggled in": func(t *testing.T, root *backup.Root) {
			if err := root.WriteFile("volumes/extra.tar.gz", []byte("unaccounted for"), 0o644); err != nil {
				t.Fatalf("add file: %v", err)
			}
		},
	}

	for name, wreck := range damage {
		t.Run(name, func(t *testing.T) {
			f := newRestoreFixture(t)
			before := f.mibFiles()
			wreck(t, f.archiveRoot(f.dir))

			if _, err := f.restore(backup.RestoreOptions{Apply: true, AllowTreeOverwrite: true}); err == nil {
				t.Fatal("a damaged archive was accepted")
			}
			if f.engine.Called("stop:") {
				t.Error("the stack was stopped before the archive was proved sound")
			}
			if f.engine.Called("import:") {
				t.Error("a volume was overwritten from an archive that had not verified")
			}
			assertSameFiles(t, "MIB source tree", before, f.mibFiles())
		})
	}
}

func TestRestoreDryRunChangesNothing(t *testing.T) {
	f := newRestoreFixture(t)
	before := f.mibFiles()

	_, err := f.restore(backup.RestoreOptions{})
	var dryRun *backup.DryRunError
	if !errors.As(err, &dryRun) {
		t.Fatalf("want *backup.DryRunError, got %T: %v", err, err)
	}
	if f.engine.Called("stop:") || f.engine.Called("import:") {
		t.Fatal("a dry run changed something")
	}
	assertSameFiles(t, "MIB source tree", before, f.mibFiles())

	out := f.output()
	for _, want := range []string{
		"RESTORE PLAN",
		"The following will be OVERWRITTEN:",
		"volume  go-netmon_metrics-data",
		"HOST DIRECTORY in your working tree",
		"requires --allow-tree-overwrite",
	} {
		if want == "volume  go-netmon_metrics-data" {
			want = "volume  " + f.volumes[backup.ComponentMetrics]
		}
		if !strings.Contains(out, want) {
			t.Errorf("the plan should mention %q:\n%s", want, out)
		}
	}
}

func TestRestoreVerifyOnlyStopsAfterVerification(t *testing.T) {
	f := newRestoreFixture(t)

	if _, err := f.restore(backup.RestoreOptions{VerifyOnly: true, Apply: true}); err != nil {
		t.Fatalf("verify-only: %v", err)
	}
	if f.engine.Called("stop:") || f.engine.Called("import:") {
		t.Fatal("--verify-only touched the deployment")
	}
	if !strings.Contains(f.output(), "backup verified") {
		t.Fatal("--verify-only should report the outcome")
	}
}

// TestRestoreTreeGate covers the one destination that is not a Docker volume.
func TestRestoreTreeGate(t *testing.T) {
	t.Run("refuses to replace a populated tree without the opt-in", func(t *testing.T) {
		f := newRestoreFixture(t)
		before := f.mibFiles()

		_, err := f.restore(backup.RestoreOptions{Apply: true})
		var gate *backup.TreeOverwriteError
		if !errors.As(err, &gate) {
			t.Fatalf("want *backup.TreeOverwriteError, got %T: %v", err, err)
		}
		if gate.Existing != len(before) {
			t.Errorf("the refusal counted %d existing files, the tree holds %d", gate.Existing, len(before))
		}
		// It must fail, not skip: a "restore complete" over an unrestored MIB
		// tree is the exact half-restore this gate exists to prevent.
		if f.engine.Called("import:") {
			t.Error("the gate must be reached before anything is written")
		}
		if f.engine.Called("stop:") {
			t.Error("the gate must be reached before the stack is stopped")
		}
		assertSameFiles(t, "MIB source tree", before, f.mibFiles())
	})

	t.Run("replaces a populated tree with the opt-in", func(t *testing.T) {
		f := newRestoreFixture(t)
		if _, err := f.restore(backup.RestoreOptions{Apply: true, AllowTreeOverwrite: true}); err != nil {
			t.Fatalf("restore: %v", err)
		}
		assertSameFiles(t, "MIB source tree", f.wantMIBs, f.mibFiles())
	})

	t.Run("needs no opt-in for an empty destination", func(t *testing.T) {
		f := newRestoreFixture(t)
		if err := f.root.Empty("repo/mibs"); err != nil {
			t.Fatalf("empty the tree: %v", err)
		}
		if _, err := f.restore(backup.RestoreOptions{Apply: true}); err != nil {
			t.Fatalf("the fresh-host path must not need a gate: %v", err)
		}
		assertSameFiles(t, "MIB source tree", f.wantMIBs, f.mibFiles())
	})

	t.Run("skipping the component leaves the tree alone", func(t *testing.T) {
		f := newRestoreFixture(t)
		before := f.mibFiles()
		if _, err := f.restore(backup.RestoreOptions{Apply: true, Skip: []backup.Component{backup.ComponentMIBs}}); err != nil {
			t.Fatalf("restore: %v", err)
		}
		assertSameFiles(t, "MIB source tree", before, f.mibFiles())
	})
}

func TestRestoreCanRedirectTheTreeSomewhereHarmless(t *testing.T) {
	f := newRestoreFixture(t)
	elsewhere := f.base + "/inspect-me"
	if err := f.root.Mkdir("inspect-me", 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	before := f.mibFiles()

	if _, err := f.restore(backup.RestoreOptions{
		Apply:   true,
		Only:    []backup.Component{backup.ComponentMIBs},
		MIBTree: elsewhere,
	}); err != nil {
		t.Fatalf("restore: %v", err)
	}

	redirected, err := backuptest.Files(f.root, "inspect-me")
	if err != nil {
		t.Fatalf("read the redirected tree: %v", err)
	}
	assertSameFiles(t, "redirected tree", f.wantMIBs, redirected)
	assertSameFiles(t, "the working copy", before, f.mibFiles())
	if !strings.Contains(f.output(), "will be restored into") {
		t.Error("a tree landing somewhere other than its origin should be reported")
	}
}

func TestRestoreRefusesADifferentComposeProject(t *testing.T) {
	f := newRestoreFixture(t)
	other := f.newService(backup.Config{Project: "someone-elses-stack"})

	_, err := other.Restore(context.Background(), backup.RestoreOptions{From: f.dir, Apply: true, AllowTreeOverwrite: true})
	var mismatch *backup.ProjectMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("want *backup.ProjectMismatchError, got %T: %v", err, err)
	}
	if mismatch.Backup != testProject || mismatch.Target != "someone-elses-stack" {
		t.Fatalf("the refusal named %+v", mismatch)
	}
	if f.engine.Called("import:") {
		t.Fatal("a cross-project restore wrote something")
	}
}

func TestRestoreRefusesAForeignComposeProject(t *testing.T) {
	f := newRestoreFixture(t)
	f.engine.SetForeignContainers(backup.Container{Name: "gitea", ConfigFiles: "/srv/gitea/docker-compose.yml"})

	_, err := f.restore(backup.RestoreOptions{Apply: true, AllowTreeOverwrite: true})
	var foreign *backup.ForeignProjectError
	if !errors.As(err, &foreign) {
		t.Fatalf("want *backup.ForeignProjectError, got %T: %v", err, err)
	}
	if f.engine.Called("stop:") || f.engine.Called("import:") {
		t.Fatal("a project this tooling does not own was operated on")
	}
}

func TestRestoreRefusesAComponentTheTargetCannotHold(t *testing.T) {
	f := newRestoreFixture(t)
	f.engine.SetComposeVolumes("webui-data", "go-netmon-data", "grafana-data")

	_, err := f.restore(backup.RestoreOptions{Apply: true, AllowTreeOverwrite: true})
	var undefined *backup.VolumeUndefinedError
	if !errors.As(err, &undefined) {
		t.Fatalf("want *backup.VolumeUndefinedError, got %T: %v", err, err)
	}
	if undefined.Component != backup.ComponentMetrics {
		t.Fatalf("the refusal named component %q", undefined.Component)
	}
}

func TestRestoreCreatesAMissingVolumeThroughCompose(t *testing.T) {
	f := newRestoreFixture(t)
	// The shape of a "docker compose down -v" followed by a redeploy: the
	// volume is gone entirely. It must come back through compose, so it carries
	// the project labels the ownership checks depend on - creating it by hand
	// would produce an unlabelled volume this tooling would then refuse.
	if err := f.engine.RemoveVolume(f.volumes[backup.ComponentMetrics]); err != nil {
		t.Fatalf("remove volume: %v", err)
	}

	if _, err := f.restore(backup.RestoreOptions{
		Apply: true,
		Only:  []backup.Component{backup.ComponentMetrics},
	}); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if !f.engine.Called("create-volumes") {
		t.Fatal("the missing volume should have been created through compose")
	}
	assertSameFiles(t, "metrics volume", f.wantVols[backup.ComponentMetrics], f.volumeFiles(backup.ComponentMetrics))
}

func TestRestoreSelectiveLeavesTheRestAlone(t *testing.T) {
	f := newRestoreFixture(t)
	damagedGrafana := f.volumeFiles(backup.ComponentGrafana)

	result, err := f.restore(backup.RestoreOptions{
		Apply: true,
		Only:  []backup.Component{backup.ComponentMetrics},
	})
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if names(result.Restored) != "metrics" {
		t.Fatalf("restored %q", names(result.Restored))
	}
	assertSameFiles(t, "metrics volume", f.wantVols[backup.ComponentMetrics], f.volumeFiles(backup.ComponentMetrics))
	assertSameFiles(t, "grafana volume", damagedGrafana, f.volumeFiles(backup.ComponentGrafana))
	if !strings.Contains(f.output(), "Left untouched") {
		t.Error("the plan should say what it is leaving alone")
	}
}

func TestRestoreWarnsWhenWebUIAndPollerAreSplit(t *testing.T) {
	f := newRestoreFixture(t)

	if _, err := f.restore(backup.RestoreOptions{Apply: true, Only: []backup.Component{backup.ComponentWebUI}}); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if !strings.Contains(f.output(), "restoring 'webui' and 'poller' separately") {
		t.Fatal("splitting the pair should warn")
	}
}

func TestRestoreSafetyBackupIsTakenAndCanBeRefused(t *testing.T) {
	t.Run("preserves exactly what it is about to destroy", func(t *testing.T) {
		f := newRestoreFixture(t)
		damaged := f.volumeFiles(backup.ComponentMetrics)

		result, err := f.restore(backup.RestoreOptions{Apply: true, Only: []backup.Component{backup.ComponentMetrics}})
		if err != nil {
			t.Fatalf("restore: %v", err)
		}
		if result.SafetyBackup == "" {
			t.Fatal("no safety backup was taken")
		}
		archive, err := f.svc.Verify(result.SafetyBackup)
		if err != nil {
			t.Fatalf("the safety backup does not verify: %v", err)
		}
		defer archive.Close()
		if got := names(archive.Manifest.Components); got != "metrics" {
			t.Fatalf("the safety backup covered %q, want the components being destroyed", got)
		}

		// Restoring the safety backup must undo the restore exactly.
		if _, err := f.svc.Restore(context.Background(), backup.RestoreOptions{
			From:           result.SafetyBackup,
			Apply:          true,
			NoSafetyBackup: true,
		}); err != nil {
			t.Fatalf("restore the safety backup: %v", err)
		}
		assertSameFiles(t, "metrics volume", damaged, f.volumeFiles(backup.ComponentMetrics))
	})

	t.Run("can be refused explicitly", func(t *testing.T) {
		f := newRestoreFixture(t)
		result, err := f.restore(backup.RestoreOptions{
			Apply:              true,
			AllowTreeOverwrite: true,
			NoSafetyBackup:     true,
		})
		if err != nil {
			t.Fatalf("restore: %v", err)
		}
		if result.SafetyBackup != "" {
			t.Fatal("--no-safety-backup still took one")
		}
		if !strings.Contains(f.output(), "will not be recoverable") {
			t.Error("refusing the safety backup should say what that means")
		}
	})

	t.Run("omits the MIB tree when there is nothing to preserve", func(t *testing.T) {
		f := newRestoreFixture(t)
		if err := f.root.Empty("repo/mibs"); err != nil {
			t.Fatalf("empty: %v", err)
		}
		if _, err := f.restore(backup.RestoreOptions{Apply: true, Only: []backup.Component{backup.ComponentMIBs}}); err != nil {
			t.Fatalf("the fresh-host path must not be blocked by the safety backup: %v", err)
		}
		if !strings.Contains(f.output(), "omitting 'mibs'") {
			t.Error("omitting an empty tree from the safety backup should be reported")
		}
	})
}

func TestRestoreReportsImageDrift(t *testing.T) {
	f := newRestoreFixture(t)
	other := f.newService(backup.Config{})
	f.engine.AddService("go-netmon", "go-netmon:v2", true)

	if _, err := other.Restore(context.Background(), backup.RestoreOptions{
		From:  f.dir,
		Apply: true,
		Only:  []backup.Component{backup.ComponentMetrics},
	}); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if !strings.Contains(f.output(), "image differs") {
		t.Fatalf("image drift should be reported:\n%s", f.output())
	}
}

func TestRestoreRefusesAnArchiveThatIsNotOne(t *testing.T) {
	h := newHarness(t)

	var manifestErr *backup.ManifestError
	if _, err := h.svc.Restore(context.Background(), backup.RestoreOptions{From: h.repo}); !errors.As(err, &manifestErr) {
		t.Fatalf("want *backup.ManifestError for a directory with no manifest, got %T: %v", err, err)
	}

	var openErr *backup.RootOpenError
	if _, err := h.svc.Restore(context.Background(), backup.RestoreOptions{From: h.base + "/nowhere"}); !errors.As(err, &openErr) {
		t.Fatalf("want *backup.RootOpenError for a missing directory, got %T: %v", err, err)
	}
}

func TestRestoreRefusesAnArchiveFromANewerTool(t *testing.T) {
	f := newRestoreFixture(t)
	root := f.archiveRoot(f.dir)

	data, err := root.ReadFile(backup.ManifestFile)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	edited := strings.Replace(string(data), `"schema_version": 2`, `"schema_version": 99`, 1)
	if byteErr := root.WriteFile(backup.ManifestFile, []byte(edited), 0o644); byteErr != nil {
		t.Fatalf("edit manifest: %v", byteErr)
	}
	// Re-sign the archive so the schema check is what refuses it, not the digest.
	resign(t, root)

	_, err = f.restore(backup.RestoreOptions{Apply: true, AllowTreeOverwrite: true})
	var schema *backup.SchemaError
	if !errors.As(err, &schema) {
		t.Fatalf("want *backup.SchemaError, got %T: %v", err, err)
	}
}

// resign rewrites checksums.sha256 for an archive a test has edited on purpose,
// so that the check under test is the one that fires.
func resign(t *testing.T, root *backup.Root) {
	t.Helper()
	files, err := backuptest.Files(root, ".")
	if err != nil {
		t.Fatalf("list archive: %v", err)
	}
	var b strings.Builder
	for _, name := range sortedNames(files) {
		if name == backup.ChecksumFile {
			continue
		}
		b.WriteString(backuptest.Digest([]byte(files[name])))
		b.WriteString("  ./")
		b.WriteString(name)
		b.WriteString("\n")
	}
	if err := root.WriteFile(backup.ChecksumFile, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write checksums: %v", err)
	}
}

func sortedNames(files map[string]string) []string {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	for i := 1; i < len(names); i++ {
		for j := i; j > 0 && names[j] < names[j-1]; j-- {
			names[j], names[j-1] = names[j-1], names[j]
		}
	}
	return names
}
