package backup_test

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-netmon/internal/backup"
)

// A backup that cannot finish must not leave one that looks finished.
//
// Every step below can fail on a real host -- a daemon that stops answering
// mid-export, a volume whose size cannot be read, a config file that was moved.
// What matters is not that the failure is unusual but that it is reported: an
// archive written from a half-run backup is indistinguishable from a good one
// at the moment it is needed, and that is the only moment anybody looks.

// failingDocker is a runtime that fails one named operation.
//
// It wraps a working one rather than reimplementing it, so a test names the
// single thing that goes wrong and everything else behaves.
type failingDocker struct {
	backup.Docker

	failOn string
	err    error
}

func (f *failingDocker) fails(op string) bool { return f.failOn == op }

func (f *failingDocker) ExportVolume(ctx context.Context, volume string, w io.Writer) error {
	if f.fails("ExportVolume") {
		return f.err
	}
	return f.Docker.ExportVolume(ctx, volume, w)
}

func (f *failingDocker) VolumeBytes(ctx context.Context, volume string) (int64, error) {
	if f.fails("VolumeBytes") {
		return 0, f.err
	}
	return f.Docker.VolumeBytes(ctx, volume)
}

func (f *failingDocker) ImageID(ctx context.Context, image string) (string, error) {
	if f.fails("ImageID") {
		return "", f.err
	}
	return f.Docker.ImageID(ctx, image)
}

func (f *failingDocker) StartServices(ctx context.Context, services []string) error {
	if f.fails("StartServices") {
		return f.err
	}
	return f.Docker.StartServices(ctx, services)
}

var errRuntimeGone = errors.New("cannot connect to the docker daemon")

// backupWith runs a backup against a runtime that fails one operation, and
// returns the error rather than failing the test.
func backupWith(t *testing.T, h *harness, failOn string, opts backup.Options) error {
	t.Helper()

	svc := h.newService(backup.Config{
		Docker: &failingDocker{Docker: h.engine, failOn: failOn, err: errRuntimeGone},
	})
	_, err := svc.Backup(context.Background(), opts)
	return err
}

// TestAFailedExportFailsTheBackup is the central one. A volume that cannot be
// read is the difference between a backup and an empty file with a confident
// name.
func TestAFailedExportFailsTheBackup(t *testing.T) {
	h := newHarness(t)

	err := backupWith(t, h, "ExportVolume", backup.Options{})
	if err == nil {
		t.Fatal("a backup whose volume export failed reported success")
	}
	if !errors.Is(err, errRuntimeGone) {
		t.Errorf("error was %v, want the runtime failure that caused it", err)
	}
}

// TestAFailureRecordingTheManifestStopsTheBackup covers the last step. The
// manifest is what a restore reads to know what it is restoring, so a backup
// whose manifest could not be completed must not be left looking finished.
func TestAFailureRecordingTheManifestStopsTheBackup(t *testing.T) {
	h := newHarness(t)

	// Resolving each service's image id happens while the manifest is filled.
	err := backupWith(t, h, "ImageID", backup.Options{})
	if err == nil {
		t.Fatal("a backup whose manifest could not be completed reported success")
	}
	if !errors.Is(err, errRuntimeGone) {
		t.Errorf("error was %v, want the runtime failure", err)
	}
}

// TestTheStackIsRestartedEvenWhenTheBackupFails is the property an operator
// depends on most: a failed backup must not leave the monitoring stack down.
func TestTheStackIsRestartedEvenWhenTheBackupFails(t *testing.T) {
	h := newHarness(t)

	before := h.engine.Running()
	if len(before) == 0 {
		t.Fatal("the fixture has nothing running, so there is nothing to restore")
	}

	if err := backupWith(t, h, "ExportVolume", backup.Options{}); err == nil {
		t.Fatal("the backup did not fail")
	}

	after := h.engine.Running()
	if len(after) != len(before) {
		t.Errorf("after a failed backup %d services are running, want the %d that were: "+
			"a backup that fails must not leave the stack stopped", len(after), len(before))
	}
}

// TestAConfigFileThatIsGoneIsWarnedAboutAndNotClaimed covers the config
// component's deliberate tolerance.
//
// Not every config file is present in every deployment, so one that is absent
// is a warning rather than a failure. What must not happen is the manifest
// listing it anyway: a restore reads that list, and a file recorded but never
// archived is one a restore would claim to have put back.
func TestAConfigFileThatIsGoneIsWarnedAboutAndNotClaimed(t *testing.T) {
	h := newHarness(t)

	if err := h.root.Remove("repo/prometheus.yml"); err != nil {
		t.Fatalf("remove the fixture config: %v", err)
	}

	result, err := h.svc.Backup(context.Background(),
		backup.Options{Only: []backup.Component{backup.ComponentConfig}})
	if err != nil {
		t.Fatalf("an absent config file aborted the backup: %v", err)
	}

	if !strings.Contains(h.output(), "prometheus.yml") {
		t.Errorf("nothing in the output mentions the missing file:\n%s", h.output())
	}
	if !strings.Contains(h.output(), "WARNING") {
		t.Errorf("the omission was not reported as a warning:\n%s", h.output())
	}
	for _, recorded := range result.Manifest.ConfigFiles {
		if strings.Contains(recorded.Path, "prometheus.yml") {
			t.Errorf("the manifest lists %q, which was never archived; "+
				"a restore would claim to put it back", recorded.Path)
		}
	}
	// The files that are there are still recorded, so the omission is narrow.
	if len(result.Manifest.ConfigFiles) == 0 {
		t.Error("no config files were recorded at all")
	}
}

// TestABackupIntoAnUnwritableDirectoryFails covers the whole write path at
// once: the archive directory, its members, the config copies and the manifest.
//
// A full disk and a directory owned by someone else both look like this, and
// both are things that happen to a backup that runs unattended on a schedule.
// The one outcome that must not occur is a directory that exists, is named like
// a backup, and is not one.
func TestABackupIntoAnUnwritableDirectoryFails(t *testing.T) {
	h := newHarness(t)

	if err := h.root.MkdirAll("locked", 0o755); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	// Read and traverse but not write. Tests do not run as root, so this holds.
	if err := h.root.Chmod("locked", 0o500); err != nil {
		t.Fatalf("lock the directory: %v", err)
	}
	t.Cleanup(func() { _ = h.root.Chmod("locked", 0o755) })

	_, err := h.svc.Backup(context.Background(), backup.Options{
		OutputDir: filepath.Join(h.base, "locked"),
		Name:      "attempt",
	})
	if err == nil {
		t.Fatal("a backup into a directory it cannot write reported success")
	}

	// And the stack is back up regardless.
	if len(h.engine.Running()) == 0 {
		t.Error("a backup that could not write left the stack stopped")
	}
}

// TestABackupRefusesToOverwriteAnExistingArchive is the other half of the same
// concern: an archive that is already there is not silently replaced, because
// the one being overwritten may be the only good one.
func TestABackupRefusesToOverwriteAnExistingArchive(t *testing.T) {
	h := newHarness(t)

	first := h.backup(backup.Options{Name: "same-name"})
	if first == nil {
		t.Fatal("the first backup did not run")
	}

	_, err := h.svc.Backup(context.Background(), backup.Options{Name: "same-name"})
	if err == nil {
		t.Fatal("a second backup silently reused the first one's directory")
	}
	var exists *backup.ExistsError
	if !errors.As(err, &exists) {
		t.Errorf("error was %T (%v), want *backup.ExistsError", err, err)
	}
}
