//go:build integration

package integration

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-netmon/internal/backup"
)

// Backup and restore against a real Docker daemon.
//
// The rest of this package's backup coverage runs against a fake runtime that
// implements the same interface, which proves the right argument vectors are
// built. It cannot prove the daemon does what those vectors say: that a tar
// written by one container is read back byte for byte by another, that a volume
// is genuinely emptied before it is extracted into, and that numeric ownership
// survives. Those are what a restore rests on, and they only exist at the
// daemon.
//
// The daemon here is the sibling "dind" service, not the host's. It starts
// empty and dies with the stack, so nothing below can see -- let alone stop,
// empty or overwrite -- the live deployment whose backup path it is testing.
// That isolation is why the test container itself is unprivileged and has no
// host socket.
//
// The operations under test go through backup.CLI, the same client the tool
// uses. Only the fixtures -- creating a labelled volume, reading back an
// owner -- talk to docker directly, because the client deliberately offers no
// way to do either and adding one for a test would be shaping production code
// around its tests.

// dindProject is the compose project these tests claim. Nothing else uses it,
// and it exists only inside the sibling daemon.
const dindProject = "netmon-backup-it"

// requireDinD returns a client for the sibling daemon, or skips.
//
// Skipping rather than failing keeps the suite runnable where no daemon is
// offered, and says plainly that this coverage was not obtained rather than
// letting the run look complete.
func requireDinD(t *testing.T) *backup.CLI {
	t.Helper()

	if os.Getenv("DOCKER_HOST") == "" {
		t.Skip("DOCKER_HOST is unset: the sibling docker daemon is not available, so the " +
			"backup round trip against a real daemon is NOT covered by this run")
	}

	cli, err := backup.NewCLI(backup.CLIOptions{
		Project: dindProject,
		// No compose method is called here; the client only requires a value.
		ComposeFile: "/workspace/docker-compose.test.yml",
		HelperImage: helperImage(t),
	})
	if err != nil {
		t.Fatalf("build a docker client: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if _, err := cli.Versions(ctx); err != nil {
		t.Fatalf("the sibling daemon did not answer: %v", err)
	}
	return cli
}

// helperImage is the image the client runs volume work inside.
//
// It is BUILT by run-tests.sh and loaded into the sibling daemon rather than
// pulled from a registry inside it, and its name arrives in
// NETMON_HELPER_IMAGE so that the script holds the only copy of the string.
//
// The previous value was the literal "busybox:1.36". dind is recreated for
// every run and starts with an empty image store, so that was a fresh Docker
// Hub pull on EVERY run -- measured 2026-08-28 (#37) as a TLS handshake
// timeout to auth.docker.io that failed the suite for a reason unrelated to
// the code under test. A tag pin fixes WHICH image, not whether it can be
// fetched.
//
// Fatal rather than skip: DOCKER_HOST is already set by the time this is
// reached, so the daemon was offered. A missing helper image is the harness
// being wrong, not this coverage being unavailable, and silently skipping
// would hide that.
func helperImage(t *testing.T) string {
	t.Helper()

	image := os.Getenv("NETMON_HELPER_IMAGE")
	if image == "" {
		t.Fatal("NETMON_HELPER_IMAGE is unset while DOCKER_HOST is set: the sibling daemon " +
			"was offered without the helper image run-tests.sh loads into it, so these " +
			"tests would fail on a missing image rather than on the code under test")
	}
	return image
}

// dockerRun runs a docker command against the sibling daemon, for fixtures.
func dockerRun(t *testing.T, args ...string) string {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("docker %s: %v (%s)", strings.Join(args, " "), err, stderr.String())
	}
	return strings.TrimSpace(stdout.String())
}

// newVolume creates a volume labelled for a project and removes it afterwards.
func newVolume(t *testing.T, name, project string) string {
	t.Helper()

	dockerRun(t, "volume", "create",
		"--label", "com.docker.compose.project="+project, name)
	t.Cleanup(func() {
		// Best effort: the daemon dies with the stack regardless.
		_ = exec.Command("docker", "volume", "rm", "-f", name).Run()
	})
	return name
}

// tarOf builds an uncompressed tar of the given files, as an export produces.
func tarOf(t *testing.T, files map[string]string) []byte {
	t.Helper()

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, name := range sortedKeys(files) {
		body := files[name]
		hdr := &tar.Header{
			Typeflag: tar.TypeReg, Name: "./" + name,
			Mode: 0o644, Size: int64(len(body)), ModTime: time.Unix(1700000000, 0),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("tar header %s: %v", name, err)
		}
		if _, err := io.WriteString(tw, body); err != nil {
			t.Fatalf("tar body %s: %v", name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	return buf.Bytes()
}

// filesIn reads a tar back into a map, ignoring directory entries.
func filesIn(t *testing.T, r io.Reader) map[string]string {
	t.Helper()

	out := map[string]string{}
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read tar: %v", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("read %s: %v", hdr.Name, err)
		}
		out[strings.TrimPrefix(hdr.Name, "./")] = string(body)
	}
	return out
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestVolumeRoundTripSurvivesTheDaemon is what a restore ultimately depends on:
// what went into a volume comes back out of it unchanged.
func TestVolumeRoundTripSurvivesTheDaemon(t *testing.T) {
	cli := requireDinD(t)
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	volume := newVolume(t, dindProject+"_roundtrip", dindProject)

	// Pebble-shaped: a manifest, a sizeable log, and a nested directory, since a
	// flat comparison would not notice a lost subdirectory.
	want := map[string]string{
		"CURRENT":             "MANIFEST-000001\n",
		"000001.log":          strings.Repeat("record\n", 512),
		"nested/deep/OPTIONS": "[Version]\n  pebble_version=0.1\n",
	}
	if err := cli.ImportVolume(ctx, volume, bytes.NewReader(tarOf(t, want))); err != nil {
		t.Fatalf("seed the volume: %v", err)
	}

	var exported bytes.Buffer
	if err := cli.ExportVolume(ctx, volume, &exported); err != nil {
		t.Fatalf("export: %v", err)
	}
	if exported.Len() == 0 {
		t.Fatal("the export produced no bytes at all")
	}

	got := filesIn(t, bytes.NewReader(exported.Bytes()))
	for name, content := range want {
		if got[name] != content {
			t.Errorf("%s came back as %d bytes, want %d", name, len(got[name]), len(content))
		}
	}
	if len(got) != len(want) {
		t.Errorf("the export holds %d files, want %d: %v", len(got), len(want), sortedKeys(got))
	}

	// The manifest records a size; it has to be a real one.
	size, err := cli.VolumeBytes(ctx, volume)
	if err != nil {
		t.Fatalf("size the volume: %v", err)
	}
	if size <= 0 {
		t.Errorf("volume size = %d, want the bytes actually on disk", size)
	}
}

// TestImportReplacesRatherThanMerges is the one an operator discovers at the
// worst possible moment. A restore that merges leaves files the backup does not
// contain, so the stack comes up on a mixture of two states that never existed
// together.
func TestImportReplacesRatherThanMerges(t *testing.T) {
	cli := requireDinD(t)
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	volume := newVolume(t, dindProject+"_replace", dindProject)

	stale := map[string]string{"CURRENT": "old\n", "stale/leftover.log": "from a previous life\n"}
	if err := cli.ImportVolume(ctx, volume, bytes.NewReader(tarOf(t, stale))); err != nil {
		t.Fatalf("seed the old state: %v", err)
	}

	fresh := map[string]string{"CURRENT": "new\n"}
	if err := cli.ImportVolume(ctx, volume, bytes.NewReader(tarOf(t, fresh))); err != nil {
		t.Fatalf("restore over it: %v", err)
	}

	var exported bytes.Buffer
	if err := cli.ExportVolume(ctx, volume, &exported); err != nil {
		t.Fatalf("export: %v", err)
	}
	got := filesIn(t, bytes.NewReader(exported.Bytes()))

	if _, survived := got["stale/leftover.log"]; survived {
		t.Error("a file the archive does not contain survived the restore; the stack " +
			"would come up on a mixture of two states")
	}
	if got["CURRENT"] != "new\n" {
		t.Errorf("CURRENT = %q, want the restored contents", got["CURRENT"])
	}
}

// TestOwnershipSurvivesTheRoundTrip covers --numeric-owner. Prometheus and
// Grafana run as non-root uids; a restore that resets ownership to root leaves
// a container unable to write its own data directory, which reads as a broken
// image rather than a broken restore.
func TestOwnershipSurvivesTheRoundTrip(t *testing.T) {
	cli := requireDinD(t)
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	source := newVolume(t, dindProject+"_owned", dindProject)

	// 65534 is nobody: present in every image and never the uid a test runs as.
	dockerRun(t, "run", "--rm", "-v", source+":/dst", helperImage(t),
		"sh", "-c", "mkdir -p /dst/data && echo x > /dst/data/owned.db && chown -R 65534:65534 /dst/data")

	var exported bytes.Buffer
	if err := cli.ExportVolume(ctx, source, &exported); err != nil {
		t.Fatalf("export: %v", err)
	}

	restored := newVolume(t, dindProject+"_owned_restored", dindProject)
	if err := cli.ImportVolume(ctx, restored, bytes.NewReader(exported.Bytes())); err != nil {
		t.Fatalf("import: %v", err)
	}

	owner := dockerRun(t, "run", "--rm", "-v", restored+":/src", helperImage(t),
		"stat", "-c", "%u:%g", "/src/data/owned.db")
	if owner != "65534:65534" {
		t.Errorf("the restored file is owned by %s, want 65534:65534: a restore that "+
			"resets ownership leaves the container unable to write its own data", owner)
	}
}

// TestVolumeOwnershipIsReadFromRealLabels is the guard that stops a restore
// touching a deployment it was not aimed at, checked against labels a daemon
// actually applied rather than a fake's bookkeeping.
func TestVolumeOwnershipIsReadFromRealLabels(t *testing.T) {
	cli := requireDinD(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	foreign := newVolume(t, dindProject+"_foreign", "somebody-elses-stack")

	project, found, err := cli.VolumeProject(ctx, foreign)
	if err != nil {
		t.Fatalf("read the volume's project: %v", err)
	}
	if !found {
		t.Fatal("a volume reported absent immediately after being created")
	}
	if project != "somebody-elses-stack" {
		t.Errorf("project = %q, want the label it carries; ownership is the only thing "+
			"stopping a restore from emptying another stack's data", project)
	}

	// A volume that genuinely is not there is absent rather than an error, which
	// is the distinction a restore uses to decide whether it may create one.
	_, present, absentErr := cli.VolumeProject(ctx, dindProject+"_never_existed")
	if absentErr != nil || present {
		t.Errorf("an absent volume reported (found=%v, err=%v), want (false, nil)", present, absentErr)
	}
}
