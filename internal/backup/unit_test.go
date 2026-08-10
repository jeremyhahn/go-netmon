package backup

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

// --- components -----------------------------------------------------------

func TestParseComponents(t *testing.T) {
	got, err := ParseComponents(" config , mibs,metrics ")
	if err != nil {
		t.Fatalf("ParseComponents: %v", err)
	}
	if componentNames(got) != "config, mibs, metrics" {
		t.Fatalf("got %q", componentNames(got))
	}
	if empty, err := ParseComponents(""); err != nil || len(empty) != 0 {
		t.Fatalf("an empty list should parse to nothing, got %v, %v", empty, err)
	}
	if trailing, err := ParseComponents("metrics,,"); err != nil || len(trailing) != 1 {
		t.Fatalf("trailing separators should be ignored, got %v, %v", trailing, err)
	}
}

func TestParseComponentsRefusesUnknownNames(t *testing.T) {
	for _, list := range []string{"mib", "config,volumes", "METRICS", "everything"} {
		_, err := ParseComponents(list)
		var unknown *UnknownComponentError
		if !errors.As(err, &unknown) {
			t.Fatalf("%q: want *UnknownComponentError, got %T: %v", list, err, err)
		}
		if !strings.Contains(unknown.Error(), "valid:") {
			t.Errorf("the refusal should list the valid names: %v", unknown)
		}
	}
}

func TestComponentVolumesAndDescriptions(t *testing.T) {
	withVolume := map[Component]string{
		ComponentWebUI:   "webui-data",
		ComponentPoller:  "go-netmon-data",
		ComponentMetrics: "prometheus-data",
		ComponentGrafana: "grafana-data",
	}
	for c, want := range withVolume {
		if got := c.Volume(); got != want {
			t.Errorf("%s.Volume() = %q, want %q", c, got, want)
		}
	}
	for _, c := range []Component{ComponentConfig, ComponentMIBs, Component("nonsense")} {
		if got := c.Volume(); got != "" {
			t.Errorf("%s.Volume() = %q, want none", c, got)
		}
	}
	for _, c := range AllComponents() {
		if c.Description() == "" || c.Description() == string(c) {
			t.Errorf("%s has no operator-facing description", c)
		}
		if !c.Valid() {
			t.Errorf("%s should be a valid component", c)
		}
	}
	if Component("mib").Valid() {
		t.Error("an unknown name must not be valid")
	}
	if got := Component("mib").Description(); got != "mib" {
		t.Errorf("an unknown component describes itself as %q", got)
	}
}

func TestSelectComponents(t *testing.T) {
	all := AllComponents()

	kept, err := selectComponents(all, nil, []Component{ComponentMIBs, ComponentGrafana})
	if err != nil || componentNames(kept) != "config, webui, poller, metrics" {
		t.Fatalf("skip = %q, %v", componentNames(kept), err)
	}

	// --only preserves restore order rather than the order it was typed in.
	only, err := selectComponents(all, []Component{ComponentMetrics, ComponentConfig}, nil)
	if err != nil || componentNames(only) != "config, metrics" {
		t.Fatalf("only = %q, %v", componentNames(only), err)
	}

	var conflict *SelectionConflictError
	if _, err := selectComponents(all, []Component{ComponentConfig}, []Component{ComponentMIBs}); !errors.As(err, &conflict) {
		t.Fatalf("want *SelectionConflictError, got %T: %v", err, err)
	}

	var empty *EmptySelectionError
	if _, err := selectComponents(all, nil, all); !errors.As(err, &empty) {
		t.Fatalf("want *EmptySelectionError, got %T: %v", err, err)
	}

	var absent *ComponentAbsentError
	available := []Component{ComponentMetrics}
	if _, err := selectComponents(available, []Component{ComponentGrafana}, nil); !errors.As(err, &absent) {
		t.Fatalf("want *ComponentAbsentError, got %T: %v", err, err)
	}
}

// --- manifest -------------------------------------------------------------

func validManifestJSON() string {
	return `{
	  "schema_version": 2,
	  "backup_name": "netmon-20260810T152704Z",
	  "created_at": "2026-08-10T15:27:12Z",
	  "created_by": "user@gpu-host",
	  "tool": "backup.sh",
	  "consistency": "cold-offline",
	  "components": ["config", "mibs", "metrics"],
	  "compose": {"project": "go-netmon", "file": "docker-compose.yml", "file_sha256": "de6c", "volumes": ["prometheus-data"]},
	  "images": [{"service": "prometheus", "image": "prom/prometheus:latest", "image_id": "sha256:8da6"}],
	  "volumes": [{"component": "metrics", "compose_volume": "prometheus-data", "docker_volume": "go-netmon_prometheus-data", "member": "volumes/metrics.tar.gz", "bytes": 12, "entries": 3, "sha256": "aa"}],
	  "trees": [{"component": "mibs", "source_path": "/srv/go-netmon/mibs", "mount_target": "/app/mibs", "member": "trees/mibs.tar.gz", "bytes": 34, "entries": 5, "files": 4, "source_bytes": 99, "sha256": "bb"}],
	  "config_files": [{"path": "config.yaml", "sha256": "cc", "restorable": true}],
	  "logical": {"status": "ok", "counts": {"devices": 4}},
	  "host": {"docker_version": "27.0.0", "compose_version": "v2.29.0"}
	}`
}

// TestManifestReadsWhatTheShellToolWrote proves schema compatibility with the
// archives already on the deployment host: this is the same JSON scripts/backup.sh
// produced, field for field.
func TestManifestReadsWhatTheShellToolWrote(t *testing.T) {
	m, err := decodeManifest([]byte(validManifestJSON()))
	if err != nil {
		t.Fatalf("decodeManifest: %v", err)
	}
	if m.SchemaVersion != 2 || m.Tool != "backup.sh" {
		t.Fatalf("manifest = %+v", m)
	}
	if m.Logical.Status != "ok" || m.Logical.Counts["devices"] != 4 {
		t.Fatalf("the logical export record was dropped: %+v", m.Logical)
	}
	if got := m.Members(); len(got) != 2 {
		t.Fatalf("Members() = %d, want the volume and the tree", len(got))
	}
	tree, ok := m.Tree(ComponentMIBs)
	if !ok || tree.Files != 4 {
		t.Fatalf("Tree(mibs) = %+v, %v", tree, ok)
	}
	if _, treeOK := m.Tree(ComponentMetrics); treeOK {
		t.Error("Tree() must not answer for a component carried as a volume")
	}
	vol, ok := m.Volume(ComponentMetrics)
	if !ok || vol.DockerVolume != "go-netmon_prometheus-data" {
		t.Fatalf("Volume(metrics) = %+v, %v", vol, ok)
	}
	if _, volumeOK := m.Volume(ComponentMIBs); volumeOK {
		t.Error("Volume() must not answer for a host tree")
	}
	img, ok := m.Image("prometheus")
	if !ok || img.ImageID != "sha256:8da6" {
		t.Fatalf("Image(prometheus) = %+v, %v", img, ok)
	}
	if _, ok := m.Image("nothing"); ok {
		t.Error("Image() answered for a service that is not there")
	}
}

func TestManifestSchema1StillReads(t *testing.T) {
	one := strings.Replace(validManifestJSON(), `"schema_version": 2`, `"schema_version": 1`, 1)
	one = strings.Replace(one, `"components": ["config", "mibs", "metrics"]`, `"components": ["config", "metrics"]`, 1)
	one = strings.Replace(one, `"trees": [{"component": "mibs", "source_path": "/srv/go-netmon/mibs", "mount_target": "/app/mibs", "member": "trees/mibs.tar.gz", "bytes": 34, "entries": 5, "files": 4, "source_bytes": 99, "sha256": "bb"}],`, "", 1)

	m, err := decodeManifest([]byte(one))
	if err != nil {
		t.Fatalf("a schema-1 archive must still restore: %v", err)
	}
	if len(m.Trees) != 0 || len(m.Members()) != 1 {
		t.Fatalf("schema 1 carries no trees, got %+v", m.Trees)
	}
}

func TestManifestRejectsUnusableArchives(t *testing.T) {
	cases := map[string]string{
		"not JSON at all":           "{{{",
		"no schema_version":         strings.Replace(validManifestJSON(), `"schema_version": 2,`, "", 1),
		"no backup_name":            strings.Replace(validManifestJSON(), `"backup_name": "netmon-20260810T152704Z",`, "", 1),
		"no created_at":             strings.Replace(validManifestJSON(), `"created_at": "2026-08-10T15:27:12Z",`, "", 1),
		"no consistency":            strings.Replace(validManifestJSON(), `"consistency": "cold-offline",`, "", 1),
		"no compose project":        strings.Replace(validManifestJSON(), `"project": "go-netmon", `, "", 1),
		"no components":             strings.Replace(validManifestJSON(), `"components": ["config", "mibs", "metrics"],`, "", 1),
		"an unknown component name": strings.Replace(validManifestJSON(), `"mibs"`, `"everything"`, 1),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeManifest([]byte(body)); err == nil {
				t.Fatal("this manifest should have been refused")
			}
		})
	}

	newer := strings.Replace(validManifestJSON(), `"schema_version": 2`, `"schema_version": 3`, 1)
	var schema *SchemaError
	if _, err := decodeManifest([]byte(newer)); !errors.As(err, &schema) {
		t.Fatalf("want *SchemaError, got %T: %v", err, err)
	}
}

func TestManifestRoundTrips(t *testing.T) {
	original, err := decodeManifest([]byte(validManifestJSON()))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	encoded, err := encodeManifest(original)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if encoded[len(encoded)-1] != '\n' {
		t.Error("the manifest should end with a newline so it reads like a text file")
	}
	again, err := decodeManifest(encoded)
	if err != nil {
		t.Fatalf("decode the re-encoded manifest: %v", err)
	}
	if again.Compose.Project != original.Compose.Project || len(again.Members()) != len(original.Members()) {
		t.Fatalf("round trip lost information: %+v", again)
	}
}

// --- checksums ------------------------------------------------------------

func TestChecksumsCoverAndDetect(t *testing.T) {
	root := newTestRoot(t, "archive")
	seed(t, root, map[string]string{
		"manifest.json":          `{"schema_version": 2}`,
		"volumes/metrics.tar.gz": "member bytes",
		"config/config.yaml":     "poller: {}",
	})

	if err := writeChecksums(root); err != nil {
		t.Fatalf("writeChecksums: %v", err)
	}
	if err := verifyChecksums(root); err != nil {
		t.Fatalf("a freshly written archive should verify: %v", err)
	}

	data, err := root.ReadFile(ChecksumFile)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// sha256sum's own format, so an operator can check an archive without this
	// tool at all.
	if !strings.Contains(string(data), "  ./manifest.json\n") {
		t.Fatalf("checksums.sha256 is not in sha256sum format:\n%s", data)
	}
	if strings.Contains(string(data), ChecksumFile) {
		t.Error("the checksum list must not cover itself")
	}
}

func TestVerifyChecksumsCatchesTampering(t *testing.T) {
	cases := map[string]func(t *testing.T, root *Root){
		"a file changed": func(t *testing.T, root *Root) {
			if err := root.WriteFile("manifest.json", []byte("edited"), 0o644); err != nil {
				t.Fatalf("edit: %v", err)
			}
		},
		"a file removed": func(t *testing.T, root *Root) {
			if err := root.Remove("volumes/metrics.tar.gz"); err != nil {
				t.Fatalf("remove: %v", err)
			}
		},
		"a file added": func(t *testing.T, root *Root) {
			if err := root.WriteFile("volumes/extra.tar.gz", []byte("smuggled"), 0o644); err != nil {
				t.Fatalf("add: %v", err)
			}
		},
	}
	for name, wreck := range cases {
		t.Run(name, func(t *testing.T) {
			root := newTestRoot(t, "archive")
			seed(t, root, map[string]string{
				"manifest.json":          `{"schema_version": 2}`,
				"volumes/metrics.tar.gz": "member bytes",
			})
			if err := writeChecksums(root); err != nil {
				t.Fatalf("writeChecksums: %v", err)
			}
			wreck(t, root)
			if err := verifyChecksums(root); err == nil {
				t.Fatal("tampering went undetected")
			}
		})
	}
}

func TestVerifyChecksumsRefusesAnUnsignedArchive(t *testing.T) {
	root := newTestRoot(t, "archive")
	seed(t, root, map[string]string{"manifest.json": "{}"})

	var missing *ChecksumFileError
	if err := verifyChecksums(root); !errors.As(err, &missing) {
		t.Fatalf("want *ChecksumFileError, got %T: %v", err, err)
	}
	if !strings.Contains(missing.Error(), "unverifiable") {
		t.Errorf("the refusal should say why: %v", missing)
	}
}

func TestParseChecksums(t *testing.T) {
	good := "aa" + strings.Repeat("b", 62) + "  ./manifest.json\n" +
		"cc" + strings.Repeat("d", 62) + "  *volumes/metrics.tar.gz\n\n"
	parsed, err := parseChecksums(good)
	if err != nil {
		t.Fatalf("parseChecksums: %v", err)
	}
	if len(parsed) != 2 || parsed["manifest.json"] == "" || parsed["volumes/metrics.tar.gz"] == "" {
		t.Fatalf("parsed = %+v", parsed)
	}

	bad := map[string]string{
		"no separator":     "aa" + strings.Repeat("b", 62) + " manifest.json\n",
		"digest too short": "abc  ./manifest.json\n",
		"digest not hex":   strings.Repeat("z", 64) + "  ./manifest.json\n",
		"no file named":    strings.Repeat("a", 64) + "  \n",
		"nothing listed":   "\n\n",
	}
	for name, text := range bad {
		t.Run(name, func(t *testing.T) {
			var badFile *ChecksumFileError
			if _, err := parseChecksums(text); !errors.As(err, &badFile) {
				t.Fatalf("want *ChecksumFileError, got %T: %v", err, err)
			}
		})
	}
}

// --- docker boundary ------------------------------------------------------

func TestValidName(t *testing.T) {
	valid := []string{"go-netmon", "go-netmon_webui-data", "alpine3.19", "a", "A1"}
	for _, name := range valid {
		if !validName(name) {
			t.Errorf("%q should be a usable name", name)
		}
	}
	// Anything that could be read as a flag, a path or shell syntax by whatever
	// it is handed to.
	invalid := []string{"", "-rf", "--force", "a b", "a/b", "a;b", "a$b", "a\nb", "..", "a|b", "`a`"}
	for _, name := range invalid {
		if validName(name) {
			t.Errorf("%q must be refused", name)
		}
	}
}

func TestCheckNames(t *testing.T) {
	if err := checkNames("service", []string{"go-netmon", "webui"}); err != nil {
		t.Fatalf("checkNames: %v", err)
	}
	var invalid *InvalidNameError
	if err := checkNames("service", []string{"webui", "--rm"}); !errors.As(err, &invalid) {
		t.Fatalf("want *InvalidNameError, got %T: %v", err, err)
	}
	if invalid.Name != "--rm" || invalid.Kind != "service" {
		t.Fatalf("the refusal named %+v", invalid)
	}
}

func TestDockerVolumeIsDerivedNotSearched(t *testing.T) {
	if got := dockerVolume("go-netmon", "prometheus-data"); got != "go-netmon_prometheus-data" {
		t.Fatalf("dockerVolume = %q", got)
	}
	// The unlabelled look-alike on the deployment host is a different string,
	// and that is the whole point.
	if dockerVolume("go-netmon", "go-netmon-data") == "go-netmon-data" {
		t.Fatal("a project volume must not collide with the stray of the same logical name")
	}
}

func TestBindSources(t *testing.T) {
	services := make([]ComposeService, 0, 3) // two here, one appended below
	services = append(services,
		ComposeService{Name: "poller", Binds: []Bind{{Source: "/srv/mibs", Target: "/app/mibs"}, {Source: "/srv/cfg", Target: "/app/config"}}},
		ComposeService{Name: "webui", Binds: []Bind{{Source: "/srv/mibs", Target: "/app/mibs"}}},
	)
	if got := bindSources(services, "/app/mibs"); len(got) != 1 || got[0] != "/srv/mibs" {
		t.Fatalf("bindSources = %v, want one agreed source", got)
	}
	if got := bindSources(services, "/nowhere"); len(got) != 0 {
		t.Fatalf("bindSources for an unmounted target = %v", got)
	}

	services = append(services, ComposeService{Name: "stray", Binds: []Bind{{Source: "/elsewhere", Target: "/app/mibs"}}})
	if got := bindSources(services, "/app/mibs"); len(got) != 2 {
		t.Fatalf("bindSources should report every disagreement, got %v", got)
	}
}

func TestResemblesStackVolume(t *testing.T) {
	for _, name := range []string{"go-netmon-data", "go-netmon_webui-data", "old-prometheus-data"} {
		if !resemblesStackVolume(name) {
			t.Errorf("%q resembles a stack volume and should be reported", name)
		}
	}
	for _, name := range []string{"gitea-data", "act-toolcache", "buildx_buildkit_state"} {
		if resemblesStackVolume(name) {
			t.Errorf("%q is nothing to do with this stack", name)
		}
	}
}

func TestHumanBytes(t *testing.T) {
	cases := map[int64]string{
		0: "0 B", 512: "512 B", 2048: "2.0 KB",
		5 * 1 << 20: "5.0 MB", 3 * 1 << 30: "3.0 GB",
	}
	for size, want := range cases {
		if got := humanBytes(size); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", size, got, want)
		}
	}
}

// --- MIB source tree ------------------------------------------------------

func TestTreeSafetyChecks(t *testing.T) {
	resolver := &treeResolver{repoRoot: "/srv/go-netmon", homeDir: "/home/user"}

	refused := map[string]string{
		"the filesystem root":  "/",
		"a system directory":   "/usr",
		"another one":          "/etc",
		"the home directory":   "/home/user",
		"the repository root":  "/srv/go-netmon",
		"one level below root": "/mibs",
	}
	for name, dir := range refused {
		t.Run(name, func(t *testing.T) {
			var unsafe *UnsafeTreeError
			if err := resolver.check(dir); !errors.As(err, &unsafe) {
				t.Fatalf("check(%q): want *UnsafeTreeError, got %T: %v", dir, err, err)
			}
		})
	}

	for _, dir := range []string{"/srv/go-netmon/mibs", "/home/user/sources/go-netmon/mibs", "/opt/netmon/mibs"} {
		if err := resolver.check(dir); err != nil {
			t.Errorf("check(%q) should allow a real tree: %v", dir, err)
		}
	}
}

func TestTreeResolverRefusesARelativeOverride(t *testing.T) {
	resolver := &treeResolver{mountTarget: DefaultMIBMountTarget}
	var unsafe *UnsafeTreeError
	if _, err := resolver.resolve(context.Background(), "mibs"); !errors.As(err, &unsafe) {
		t.Fatalf("want *UnsafeTreeError for a relative override, got %T: %v", err, err)
	}
}

func TestTreeRootConfinesToTheParent(t *testing.T) {
	base := newTestRoot(t, "fixture")
	if err := base.MkdirAll("checkout/mibs/standard", 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := base.WriteFile("checkout/mibs/standard/IF-MIB.txt", []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := base.WriteFile("checkout/README", []byte("must survive"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	loc := TreeLocation{Dir: base.Path() + "/checkout/mibs", MountTarget: DefaultMIBMountTarget}
	root, name, err := treeRoot(loc)
	if err != nil {
		t.Fatalf("treeRoot: %v", err)
	}
	defer root.Close()

	if name != "mibs" {
		t.Fatalf("treeRoot named the tree %q", name)
	}
	if root.Path() != base.Path()+"/checkout" {
		t.Fatalf("the root should be the tree's PARENT, got %q", root.Path())
	}

	state, err := inspectTree(loc)
	if err != nil || state.Files != 1 || state.Bytes != 1 {
		t.Fatalf("inspectTree = %+v, %v", state, err)
	}

	// Emptying the tree must leave its siblings alone.
	if err := root.Empty(name); err != nil {
		t.Fatalf("Empty: %v", err)
	}
	if _, err := base.Stat("checkout/README"); err != nil {
		t.Fatalf("emptying the tree reached a sibling: %v", err)
	}
	if _, err := base.Stat("checkout/mibs"); err != nil {
		t.Fatalf("emptying removed the tree directory itself: %v", err)
	}
}

func TestInspectTreeToleratesAnAbsentDestination(t *testing.T) {
	base := newTestRoot(t, "fixture")
	loc := TreeLocation{Dir: base.Path() + "/not-there"}
	state, err := inspectTree(loc)
	if err != nil || state.Files != 0 {
		t.Fatalf("inspectTree on a missing tree = %+v, %v; want zero, nil", state, err)
	}
}

// --- service configuration ------------------------------------------------

func TestNewRejectsBadConfiguration(t *testing.T) {
	root := newTestRoot(t, "fixture")
	seed(t, root, map[string]string{"docker-compose.yml": "services: {}\n"})
	compose := root.Path() + "/docker-compose.yml"

	cases := map[string]Config{
		"no runtime":             {RepoRoot: root.Path(), ComposeFile: compose, Project: "go-netmon"},
		"no project":             {RepoRoot: root.Path(), ComposeFile: compose, Docker: nopDocker{}},
		"an unusable project":    {RepoRoot: root.Path(), ComposeFile: compose, Project: "-rf", Docker: nopDocker{}},
		"no repository":          {ComposeFile: compose, Project: "go-netmon", Docker: nopDocker{}},
		"a missing compose file": {RepoRoot: root.Path(), ComposeFile: root.Path() + "/nope.yml", Project: "go-netmon", Docker: nopDocker{}},
		"a compose directory":    {RepoRoot: root.Path(), ComposeFile: root.Path(), Project: "go-netmon", Docker: nopDocker{}},
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := New(cfg); err == nil {
				t.Fatal("this configuration should have been refused")
			}
		})
	}
}

func TestNewFillsInDefaults(t *testing.T) {
	root := newTestRoot(t, "fixture")
	seed(t, root, map[string]string{"docker-compose.yml": "services: {}\n"})

	svc, err := New(Config{
		RepoRoot:    root.Path(),
		ComposeFile: root.Path() + "/docker-compose.yml",
		Project:     "go-netmon",
		Docker:      nopDocker{},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if svc.Project() != "go-netmon" {
		t.Fatalf("Project() = %q", svc.Project())
	}
	if svc.RepoRoot() != root.Path() {
		t.Fatalf("RepoRoot() = %q", svc.RepoRoot())
	}
	if svc.DefaultBackupDir() != root.Path()+"/backups" {
		t.Fatalf("DefaultBackupDir() = %q", svc.DefaultBackupDir())
	}
	if svc.cfg.MIBMountTarget != DefaultMIBMountTarget {
		t.Fatalf("MIBMountTarget = %q", svc.cfg.MIBMountTarget)
	}
	if svc.cfg.Now == nil || svc.cfg.Out == nil || svc.cfg.CreatedBy != "unknown" {
		t.Fatalf("defaults were not filled in: %+v", svc.cfg)
	}
}

// nopDocker satisfies the boundary for configuration tests that never reach it.
type nopDocker struct{}

func (nopDocker) ForeignContainers(context.Context) ([]Container, error)      { return nil, nil }
func (nopDocker) ComposeVolumes(context.Context) ([]string, error)            { return nil, nil }
func (nopDocker) ComposeServices(context.Context) ([]ComposeService, error)   { return nil, nil }
func (nopDocker) RunningServices(context.Context) ([]string, error)           { return nil, nil }
func (nopDocker) StopServices(context.Context, []string) error                { return nil }
func (nopDocker) StartServices(context.Context, []string) error               { return nil }
func (nopDocker) CreateVolumes(context.Context) error                         { return nil }
func (nopDocker) UpDetached(context.Context) error                            { return nil }
func (nopDocker) VolumeNames(context.Context) ([]string, error)               { return nil, nil }
func (nopDocker) VolumeProject(context.Context, string) (string, bool, error) { return "", false, nil }
func (nopDocker) VolumeBytes(context.Context, string) (int64, error)          { return 0, nil }
func (nopDocker) ExportVolume(context.Context, string, io.Writer) error       { return nil }
func (nopDocker) ImportVolume(context.Context, string, io.Reader) error       { return nil }
func (nopDocker) ImageID(context.Context, string) (string, error)             { return "", nil }
func (nopDocker) Versions(context.Context) (Versions, error)                  { return Versions{}, nil }
