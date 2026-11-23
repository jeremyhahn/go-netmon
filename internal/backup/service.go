// Package backup creates and restores cold backups of a deployed go-netmon
// stack: its Docker volumes, the repository configuration bind-mounted into it
// and the MIB source tree on the host.
//
// # Containment
//
// Every filesystem mutation this package makes goes through a Root, which is an
// os.Root anchored on one explicitly chosen directory. Paths that would leave
// that directory - "..", absolute paths, symbolic links pointing outside - are
// refused by the kernel rather than by string inspection, and a path that cannot
// be expressed inside the root is an error rather than something to sanitize and
// carry on with. No function in this package takes a caller-supplied path and
// mutates it; a mutation needs a Root, and a Root has to be opened deliberately,
// with a stated purpose that appears in the plan and in every error.
//
// The one judgement containment cannot make is which directory to anchor on.
// That choice is derived - from the pinned compose project, from the compose
// file's own bind mounts - never defaulted to something broad, and it is checked
// separately, printed in the plan before anything is written, and gated behind
// an explicit opt-in where it lands in the operator's own working tree.
//
// # Consistency
//
// Backups are cold: every service in the compose project is stopped, the data is
// copied at rest, and exactly the services that were running are started again,
// on every exit path including interruption. The web UI and poller stores are
// PebbleDB (single-writer, exclusive LOCK) and this deployment's Prometheus runs
// without --web.enable-admin-api, so there is no supported way to snapshot
// either while running. A hot copy of them would not be slightly risky, it would
// be unverifiable: the damage stays invisible until the restore, which is the
// worst moment to discover it.
package backup

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Config configures a Service. Every field that names a location is explicit;
// nothing is inferred from the working directory.
type Config struct {
	// RepoRoot is the repository the stack is deployed from. Its config files
	// are backed up and restored, and its backups/ directory is the default
	// destination.
	RepoRoot string

	// ComposeFile is the absolute path of the compose file defining the stack.
	ComposeFile string

	// Project is the compose project the stack occupies. Pinned by the caller,
	// never inherited: without it, compose derives the project from the working
	// directory, which is how a command aimed at a test stack reaches the
	// deployed one.
	Project string

	// MIBMountTarget is the container path the MIB source tree is mounted at.
	// Empty means DefaultMIBMountTarget.
	MIBMountTarget string

	// Docker is the container runtime boundary.
	Docker Docker

	// Out receives operator-facing progress and plans. Nil discards them.
	Out io.Writer

	// Now supplies the clock, so timestamps in tests are not the wall clock.
	// Nil means time.Now.
	Now func() time.Time

	// CreatedBy is recorded in manifests as the provenance of a backup.
	CreatedBy string

	// HomeDir is refused as a MIB source tree. Empty disables that check.
	HomeDir string

	// WebUIURL is the web UI the logical export is read from, without a
	// trailing slash. Empty skips the export, which is recorded in the manifest
	// as logical.status=skipped rather than passed over silently.
	WebUIURL string

	// HTTPClient performs the logical export's requests. Nil means a client
	// with a bounded timeout.
	HTTPClient *http.Client
}

// Service performs backups and restores.
//
// It is the only entry point: the command-line tool holds no logic of its own,
// so the checks below cannot be bypassed by calling something further in.
type Service struct {
	cfg Config
}

// New validates a configuration and returns a Service.
func New(cfg Config) (*Service, error) {
	if cfg.Docker == nil {
		return nil, &ComposeFileError{Path: cfg.ComposeFile, Reason: "no container runtime was configured"}
	}
	if !validName(cfg.Project) {
		return nil, &InvalidNameError{Kind: "compose project", Name: cfg.Project}
	}
	repo, err := ResolveDir("repository", cfg.RepoRoot)
	if err != nil {
		return nil, err
	}
	cfg.RepoRoot = repo

	compose, err := ResolveDir("compose file", cfg.ComposeFile)
	if err != nil {
		return nil, err
	}
	cfg.ComposeFile = compose
	if err := checkComposeFile(compose); err != nil {
		return nil, err
	}

	if cfg.MIBMountTarget == "" {
		cfg.MIBMountTarget = DefaultMIBMountTarget
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Out == nil {
		cfg.Out = io.Discard
	}
	if cfg.CreatedBy == "" {
		cfg.CreatedBy = "unknown"
	}
	return &Service{cfg: cfg}, nil
}

// checkComposeFile proves the compose file is a readable regular file, through a
// root on the directory holding it.
func checkComposeFile(path string) error {
	dir, base := filepath.Split(path)
	root, err := Open("compose file directory", strings.TrimSuffix(dir, string(filepath.Separator)))
	if err != nil {
		return &ComposeFileError{Path: path, Reason: "its directory cannot be opened"}
	}
	// a root is a directory handle; closing it cannot affect what was read through it.
	defer func() { _ = root.Close() }()

	info, err := root.Stat(base)
	if err != nil {
		return &ComposeFileError{Path: path, Reason: "not found"}
	}
	if !info.Mode().IsRegular() {
		return &ComposeFileError{Path: path, Reason: "is not a regular file"}
	}
	return nil
}

// Project reports the compose project this service operates on.
func (s *Service) Project() string { return s.cfg.Project }

// RepoRoot reports the repository this service backs up from.
func (s *Service) RepoRoot() string { return s.cfg.RepoRoot }

// DefaultBackupDir reports where backups are written when no destination is
// given.
func (s *Service) DefaultBackupDir() string { return filepath.Join(s.cfg.RepoRoot, "backups") }

// httpClient reports the client the logical export uses.
//
// The default carries a timeout: the export runs against a stack that is still
// serving traffic, and a wedged web UI must not be able to hold a backup open
// indefinitely.
func (s *Service) httpClient() *http.Client {
	if s.cfg.HTTPClient != nil {
		return s.cfg.HTTPClient
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// --- operator output ------------------------------------------------------

func (s *Service) info(format string, args ...any) {
	fmt.Fprintf(s.cfg.Out, "[netmon-backup] "+format+"\n", args...)
}

func (s *Service) warn(format string, args ...any) {
	fmt.Fprintf(s.cfg.Out, "[netmon-backup] WARNING: "+format+"\n", args...)
}

func (s *Service) line(format string, args ...any) {
	fmt.Fprintf(s.cfg.Out, format+"\n", args...)
}

func (s *Service) rule() {
	s.line("------------------------------------------------------------")
}

// announce records a root and what it is for. Every destructive step names the
// directory it is confined to before it writes anything into it.
func (s *Service) announce(r *Root) {
	s.info("root:       %s (%s)", r.Path(), r.Purpose())
}

// --- shared preflight -----------------------------------------------------

// assertProjectOwnership refuses to continue if the target compose project holds
// a container the configured compose file did not create.
//
// Acting on such a project would mean stopping, and potentially overwriting the
// data of, a deployment this repository does not own.
func (s *Service) assertProjectOwnership(ctx context.Context) error {
	foreign, err := s.cfg.Docker.ForeignContainers(ctx)
	if err != nil {
		return err
	}
	if len(foreign) == 0 {
		return nil
	}
	return &ForeignProjectError{
		Project:     s.cfg.Project,
		ComposeFile: s.cfg.ComposeFile,
		Containers:  foreign,
	}
}

// assertVolumeOwned refuses to touch a volume that does not carry the pinned
// project's compose label.
//
// This is the check that keeps the tooling away from an unlabelled look-alike:
// a stray "go-netmon-data" is a different object from the project's
// "go-netmon_go-netmon-data", and only the label makes that a checked fact
// rather than a naming convention.
func (s *Service) assertVolumeOwned(ctx context.Context, volume string) error {
	project, exists, err := s.cfg.Docker.VolumeProject(ctx, volume)
	if err != nil {
		return err
	}
	if !exists {
		return &VolumeMissingError{Volume: volume}
	}
	if project != s.cfg.Project {
		return &VolumeOwnershipError{Volume: volume, Project: s.cfg.Project, Labeled: project}
	}
	return nil
}

// reportUnmanagedVolumes names volumes that resemble stack volumes but carry no
// project label, so an operator knows they exist. Nothing acts on them; they are
// unreachable by every operation in this package, which addresses volumes only
// by derived name and only after the label check above.
func (s *Service) reportUnmanagedVolumes(ctx context.Context) {
	names, err := s.cfg.Docker.VolumeNames(ctx)
	if err != nil {
		return
	}
	reported := false
	for _, name := range names {
		if !resemblesStackVolume(name) {
			continue
		}
		project, exists, err := s.cfg.Docker.VolumeProject(ctx, name)
		if err != nil || !exists || project == s.cfg.Project {
			continue
		}
		if !reported {
			s.warn("volumes resembling stack volumes but not labeled project %q (ignored, never touched):", s.cfg.Project)
			reported = true
		}
		label := project
		if label == "" {
			label = "<none>"
		}
		s.warn("  %s  (project=%s)", name, label)
	}
}

func resemblesStackVolume(name string) bool {
	for _, c := range AllComponents() {
		if v := c.Volume(); v != "" && strings.Contains(name, v) {
			return true
		}
	}
	return false
}

// volumeFor derives the Docker volume holding a component's data.
func (s *Service) volumeFor(c Component) string {
	logical := c.Volume()
	if logical == "" {
		return ""
	}
	return dockerVolume(s.cfg.Project, logical)
}

// treeResolver builds the MIB source tree resolver for this service.
func (s *Service) treeResolver() *treeResolver {
	return &treeResolver{
		docker:      s.cfg.Docker,
		mountTarget: s.cfg.MIBMountTarget,
		repoRoot:    s.cfg.RepoRoot,
		homeDir:     s.cfg.HomeDir,
	}
}

// humanBytes renders a byte count in units an operator can sanity-check at a
// glance. Plans that destroy data are read by humans under pressure.
func humanBytes(b int64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(b)/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(b)/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(b)/(1<<10))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

func componentNames(components []Component) string {
	names := make([]string, len(components))
	for i, c := range components {
		names[i] = string(c)
	}
	return strings.Join(names, ", ")
}

// itoa renders an integer for the expected/actual fields of a MemberError.
func itoa(n int) string { return strconv.Itoa(n) }
