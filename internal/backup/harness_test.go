package backup_test

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-netmon/internal/backup"
	"github.com/jeremyhahn/go-netmon/internal/backup/backuptest"
)

// The fixtures below build a whole deployment inside one t.TempDir():
//
//	<tmp>/repo/                  the "checkout": compose file, config, mibs/
//	<tmp>/repo/mibs/             the MIB source tree, a host directory
//	<tmp>/repo/backups/          where backups land
//	<tmp>/engine/volumes/<name>/ the Docker volumes, as real directories
//
// Nothing reaches outside it. Every directory is created through a Root anchored
// on the temp directory, the engine writes only through its own Root, and the
// service under test is handed those paths and no others.
//
// These tests drive the service through its exported API only. That is
// deliberate: the command-line tool has no other way in either, so anything they
// cannot reach is not reachable in production.

const testProject = "go-netmon"

// harness is a deployment under test.
type harness struct {
	t       *testing.T
	base    string
	root    *backup.Root
	repo    string
	mibs    string
	engine  *backuptest.Engine
	svc     *backup.Service
	out     *bytes.Buffer
	clock   time.Time
	volumes map[backup.Component]string
}

// newHarness builds a deployment with four labeled volumes, four services and a
// populated MIB source tree.
func newHarness(t *testing.T) *harness {
	t.Helper()
	base := t.TempDir()

	root, err := backup.Open("test fixture", base)
	if err != nil {
		t.Fatalf("open fixture root: %v", err)
	}
	t.Cleanup(func() { root.Close() })

	h := &harness{
		t:       t,
		base:    base,
		root:    root,
		repo:    filepath.Join(base, "repo"),
		mibs:    filepath.Join(base, "repo", "mibs"),
		out:     &bytes.Buffer{},
		clock:   time.Date(2026, 8, 10, 15, 27, 4, 0, time.UTC),
		volumes: make(map[backup.Component]string),
	}

	if seedErr := backuptest.Seed(root, "repo", map[string]string{
		"docker-compose.yml": "services: {}\n",
		"config.yaml":        "poller:\n  interval: 60s\n",
		"prometheus.yml":     "global:\n  scrape_interval: 15s\n",
	}); seedErr != nil {
		t.Fatalf("seed repo: %v", seedErr)
	}
	if seedErr := backuptest.Seed(root, "repo/mibs", map[string]string{
		"standard/SNMPv2-MIB.txt":      "SNMPv2-MIB DEFINITIONS ::= BEGIN\n",
		"standard/IF-MIB.txt":          "IF-MIB DEFINITIONS ::= BEGIN\n",
		"cisco/CISCO-SMI.txt":          "CISCO-SMI DEFINITIONS ::= BEGIN\n",
		"vendor/dell/DELL-MIB.txt":     "DELL-MIB DEFINITIONS ::= BEGIN\n",
		"vendor/dell/.hidden-note.txt": "a dotfile a glob would miss\n",
	}); seedErr != nil {
		t.Fatalf("seed mib tree: %v", seedErr)
	}

	engine, err := backuptest.NewEngine(filepath.Join(base, "engine"))
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	t.Cleanup(func() { engine.Close() })
	h.engine = engine

	logical := make([]string, 0, len(volumeComponents()))
	for _, c := range volumeComponents() {
		name := testProject + "_" + c.Volume()
		h.volumes[c] = name
		logical = append(logical, c.Volume())
		if err := engine.AddVolume(name, testProject); err != nil {
			t.Fatalf("add volume %s: %v", name, err)
		}
		if err := engine.Seed(name, map[string]string{
			string(c) + "/CURRENT":    "manifest for " + string(c),
			string(c) + "/000001.log": "records for " + string(c),
		}); err != nil {
			t.Fatalf("seed volume %s: %v", name, err)
		}
	}
	engine.SetComposeVolumes(logical...)

	mibBind := backup.Bind{Source: h.mibs, Target: backup.DefaultMIBMountTarget}
	engine.AddService("go-netmon", "go-netmon:latest", true, mibBind)
	engine.AddService("webui", "go-netmon-webui:latest", true, mibBind)
	engine.AddService("prometheus", "prom/prometheus:latest", true)
	engine.AddService("grafana", "grafana/grafana:latest", true)

	h.svc = h.newService(backup.Config{})
	return h
}

// newService builds a service over the harness's deployment, filling in
// everything the caller left blank.
func (h *harness) newService(cfg backup.Config) *backup.Service {
	h.t.Helper()
	if cfg.RepoRoot == "" {
		cfg.RepoRoot = h.repo
	}
	if cfg.ComposeFile == "" {
		cfg.ComposeFile = filepath.Join(h.repo, "docker-compose.yml")
	}
	if cfg.Project == "" {
		cfg.Project = testProject
	}
	if cfg.Docker == nil {
		cfg.Docker = h.engine
	}
	if cfg.Out == nil {
		cfg.Out = h.out
	}
	if cfg.Now == nil {
		cfg.Now = h.now
	}
	if cfg.CreatedBy == "" {
		cfg.CreatedBy = "tester@fixture"
	}
	svc, err := backup.New(cfg)
	if err != nil {
		h.t.Fatalf("new service: %v", err)
	}
	return svc
}

// now advances the fixture clock by a second on each call, so downtime is
// measurable and backup names are unique without touching the wall clock.
func (h *harness) now() time.Time {
	h.clock = h.clock.Add(time.Second)
	return h.clock
}

func (h *harness) backup(opts backup.Options) *backup.Result {
	h.t.Helper()
	result, err := h.svc.Backup(context.Background(), opts)
	if err != nil {
		h.t.Fatalf("backup: %v", err)
	}
	return result
}

// mibFiles reads the MIB source tree back through a root on its parent.
func (h *harness) mibFiles() map[string]string {
	h.t.Helper()
	files, err := backuptest.Files(h.root, "repo/mibs")
	if err != nil {
		h.t.Fatalf("read mib tree: %v", err)
	}
	return files
}

// volumeFiles reads a component's volume contents back.
func (h *harness) volumeFiles(c backup.Component) map[string]string {
	h.t.Helper()
	files, err := h.engine.VolumeFiles(h.volumes[c])
	if err != nil {
		h.t.Fatalf("read volume for %s: %v", c, err)
	}
	return files
}

// archiveRoot opens a root on a backup directory so a test can inspect or
// deliberately damage it.
func (h *harness) archiveRoot(dir string) *backup.Root {
	h.t.Helper()
	root, err := backup.Open("archive under test", dir)
	if err != nil {
		h.t.Fatalf("open archive: %v", err)
	}
	h.t.Cleanup(func() { root.Close() })
	return root
}

func (h *harness) output() string { return h.out.String() }

// volumeComponents lists the components whose data lives in a Docker volume.
func volumeComponents() []backup.Component {
	var out []backup.Component
	for _, c := range backup.AllComponents() {
		if c.Volume() != "" {
			out = append(out, c)
		}
	}
	return out
}

func names(components []backup.Component) string {
	out := make([]string, len(components))
	for i, c := range components {
		out[i] = string(c)
	}
	return strings.Join(out, ", ")
}

// assertSameFiles compares two file sets exactly: an extra file is as much a
// restore failure as a missing one.
func assertSameFiles(t *testing.T, what string, want, got map[string]string) {
	t.Helper()
	for name, content := range want {
		actual, ok := got[name]
		if !ok {
			t.Errorf("%s: %q is missing after the restore", what, name)
			continue
		}
		if actual != content {
			t.Errorf("%s: %q = %q, want %q", what, name, actual, content)
		}
	}
	for name := range got {
		if _, ok := want[name]; !ok {
			t.Errorf("%s: %q survived a restore that should have replaced the directory", what, name)
		}
	}
}
