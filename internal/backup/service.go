// Package backup makes cold backups and restores of a deployed go-netmon stack, writing only through a Root.
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

	// Project is the compose project, always pinned by the caller.
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

// Service performs backups and restores; it is the only entry point.
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

// httpClient returns the logical export's client, which has a timeout.
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

// assertProjectOwnership refuses a project holding a container the compose file did not create.
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

// assertVolumeOwned refuses a volume without the pinned project's compose label.
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

// reportUnmanagedVolumes names unlabeled look-alike volumes; nothing acts on them.
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
