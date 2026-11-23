package backup

import (
	"context"
	"io"
	"slices"
	"strings"
)

// ConfigFilesLabel is the label Compose stamps a container with naming the
// compose file it was created from.
//
// The name matters and is easy to get wrong: it is "project.config_files" with
// an underscore, not "config-files". Asking for the wrong label is not an error
// — docker returns an empty string for a label that does not exist — so a
// mistake here does not fail, it silently reports every container as belonging
// to no compose file, which makes an ownership check pass or fail for reasons
// unrelated to ownership.
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

// Docker is the boundary between this package and the container runtime.
//
// Every method addresses objects by explicit name or by the pinned compose
// project, never by pattern or prefix: a volume whose name merely resembles a
// stack volume must not be reachable, and that has to be a checked fact rather
// than a naming convention.
//
// It is an interface for one reason only - so tests can drive the whole backup
// and restore logic against a real, in-process implementation of these
// semantics instead of a daemon. Nothing above this line knows what a container
// is; nothing below it knows what a backup is.
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
//
// Names are built by this package from the pinned project and a fixed volume
// name, so anything outside this shape means a caller has been given something
// unexpected. Refusing is the only correct response: a name beginning with "-"
// would be read as a flag by whatever it is handed to.
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
