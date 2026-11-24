package backup

import (
	"context"
	"io"
	"slices"
	"strings"
)

// ConfigFilesLabel is the compose label naming a container's compose file; note the underscore.
const ConfigFilesLabel = "com.docker.compose.project.config_files"

// Container is a container in the target compose project.
type Container struct {
	// Name is the container's name.
	Name string
	// ConfigFiles is the ConfigFilesLabel label, i.e. the compose file the
	// container was created from.
	ConfigFiles string
}

// ComposeService is a service the compose file defines.
type ComposeService struct {
	// Name is the service name.
	Name string
	// Image is the image reference the service runs.
	Image string
	// Binds lists the host directories the service bind-mounts.
	Binds []Bind
}

// Bind is one host-directory bind mount.
type Bind struct {
	// Source is the host path, resolved to absolute by compose.
	Source string
	// Target is the path inside the container.
	Target string
}

// Versions reports the container runtime this backup was taken against.
type Versions struct {
	Docker  string
	Compose string
}

// Docker is the boundary to the container runtime; objects are addressed by exact name or the pinned project.
type Docker interface {
	// ForeignContainers lists containers in the compose project that the
	// configured compose file did not create.
	ForeignContainers(ctx context.Context) ([]Container, error)

	// ComposeVolumes lists the logical volume names the compose file defines.
	ComposeVolumes(ctx context.Context) ([]string, error)

	// ComposeServices lists the services the compose file defines, with the
	// images they run and the host directories they bind-mount.
	ComposeServices(ctx context.Context) ([]ComposeService, error)

	// RunningServices lists the services that currently have a running
	// container.
	RunningServices(ctx context.Context) ([]string, error)

	// StopServices stops the named services.
	StopServices(ctx context.Context, services []string) error

	// StartServices starts the named services.
	StartServices(ctx context.Context, services []string) error

	// CreateVolumes materializes the project's volumes without starting
	// anything, so they carry the compose project labels the ownership checks
	// depend on.
	CreateVolumes(ctx context.Context) error

	// UpDetached brings the whole project up in the background.
	UpDetached(ctx context.Context) error

	// VolumeNames lists every volume the runtime knows.
	VolumeNames(ctx context.Context) ([]string, error)

	// VolumeProject reports the compose project label on a volume. The boolean
	// is false when the volume does not exist.
	VolumeProject(ctx context.Context, volume string) (string, bool, error)

	// VolumeBytes reports the on-disk size of a volume's contents.
	VolumeBytes(ctx context.Context, volume string) (int64, error)

	// ExportVolume writes a volume's contents to w as an uncompressed tar with
	// numeric ownership preserved.
	ExportVolume(ctx context.Context, volume string, w io.Writer) error

	// ImportVolume replaces a volume's contents with the tar stream in r. The
	// volume is emptied first: a restore is a replacement, not a merge.
	ImportVolume(ctx context.Context, volume string, r io.Reader) error

	// ImageID resolves an image reference to its content id.
	ImageID(ctx context.Context, image string) (string, error)

	// Versions reports the runtime and compose plugin versions.
	Versions(ctx context.Context) (Versions, error)
}

// validName reports whether a Docker object name is safe to pass as an argument.
func validName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
			continue
		case i > 0 && (c == '_' || c == '-' || c == '.'):
			continue
		default:
			return false
		}
	}
	return true
}

// checkNames refuses a batch of names that would not be safe as arguments.
func checkNames(kind string, names []string) error {
	for _, n := range names {
		if !validName(n) {
			return &InvalidNameError{Kind: kind, Name: n}
		}
	}
	return nil
}

// dockerVolume derives the volume name compose gives a logical volume in a
// project. Derivation, never search.
func dockerVolume(project, logical string) string {
	return project + "_" + logical
}

// bindSources lists the distinct host paths bind-mounted at a container path,
// across every service.
func bindSources(services []ComposeService, target string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, s := range services {
		for _, b := range s.Binds {
			if b.Target != target || seen[b.Source] {
				continue
			}
			seen[b.Source] = true
			out = append(out, b.Source)
		}
	}
	slices.Sort(out)
	return out
}

// sortServices puts services in name order so manifests and plans are stable.
func sortServices(services []ComposeService) {
	slices.SortFunc(services, func(a, b ComposeService) int {
		return strings.Compare(a.Name, b.Name)
	})
}
