// Package backuptest provides a real, in-process implementation of
// backup.Docker.
//
// It is not a stub. Volumes are directories, exporting one produces a genuine
// tar stream and importing one genuinely empties the destination and unpacks
// into it, so a test drives the same code paths a deployment does: the ownership
// checks, the manifest, the integrity passes and the restore gates all see real
// answers. What it does not do is talk to a daemon, which is what makes the
// tests fast and what makes them incapable of reaching a live stack.
//
// Every mutation it makes goes through a backup.Root anchored on the directory
// it was constructed with. A test that hands it t.TempDir() cannot make it write
// anywhere else, whatever the code under test asks for. That property is the
// reason this exists in Go at all: the shell test harness it replaces translated
// a container's "find /dst -mindepth 1 -delete" into a delete on the host, and a
// mistaken path there was a delete aimed at the operator's filesystem.
package backuptest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"sort"
	"strings"
	"sync"

	"github.com/jeremyhahn/go-netmon/internal/backup"
)

// UnknownServiceError reports an operation naming a service the engine does not
// have. Unexpected input is refused rather than ignored, the way the runtime
// would refuse it.
type UnknownServiceError struct {
	Name string
}

func (e *UnknownServiceError) Error() string {
	return "backuptest: no such service: " + e.Name
}

// UnknownVolumeError reports an operation naming a volume the engine does not
// have.
type UnknownVolumeError struct {
	Name string
}

func (e *UnknownVolumeError) Error() string {
	return "backuptest: no such volume: " + e.Name
}

// volume is one Docker volume: its contents live under volumes/<name> in the
// engine's root, and its compose project label decides whether the tooling is
// allowed to touch it at all.
type volume struct {
	project string
}

// service is one compose service.
type service struct {
	name    string
	image   string
	running bool
	binds   []backup.Bind
}

// Engine implements backup.Docker against directories under one root.
//
// The zero value is not usable; construct one with NewEngine. It is safe for
// concurrent use, which matters only because tests run under the race detector.
type Engine struct {
	mu             sync.Mutex
	root           *backup.Root
	volumes        map[string]*volume
	services       []*service
	composeVolumes []string
	foreign        []backup.Container
	versions       backup.Versions
	calls          []string

	// OnExport runs before each volume export. A test uses it to interrupt a
	// backup at the one moment that matters: while the stack is stopped.
	OnExport func(ctx context.Context, volume string) error

	// OnImport runs before each volume import, so a test can make a restore
	// fail partway and check what was left behind.
	OnImport func(ctx context.Context, volume string) error
}

// NewEngine returns an engine whose volumes live under dir.
//
// dir must exist; in a test it is t.TempDir(), which is what confines everything
// the engine does to that test's own scratch space.
func NewEngine(dir string) (*Engine, error) {
	root, err := backup.Ensure("test docker engine", dir)
	if err != nil {
		return nil, err
	}
	if err := root.MkdirAll("volumes", 0o755); err != nil {
		// The engine is being abandoned; report why it could not be built.
		_ = root.Close()
		return nil, err
	}
	return &Engine{
		root:     root,
		volumes:  make(map[string]*volume),
		versions: backup.Versions{Docker: "27.0.0-test", Compose: "v2.29.0-test"},
	}, nil
}

// Close releases the engine's root.
func (e *Engine) Close() error { return e.root.Close() }

// Root reports the engine's root, so a test can inspect volume contents through
// the same containment the engine writes them with.
func (e *Engine) Root() *backup.Root { return e.root }

// VolumeDir reports the path, relative to the engine's root, holding a volume's
// contents.
func VolumeDir(name string) string { return "volumes/" + name }

// --- configuration --------------------------------------------------------

// AddVolume creates a volume labeled with a compose project. An empty project
// makes it an unlabelled volume, which the tooling must refuse to touch.
func (e *Engine) AddVolume(name, project string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.root.MkdirAll(VolumeDir(name), 0o755); err != nil {
		return err
	}
	e.volumes[name] = &volume{project: project}
	return nil
}

// AddService registers a compose service.
func (e *Engine) AddService(name, image string, running bool, binds ...backup.Bind) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.services = append(e.services, &service{name: name, image: image, running: running, binds: binds})
}

// SetComposeVolumes sets the logical volume names the compose file defines.
func (e *Engine) SetComposeVolumes(names ...string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.composeVolumes = append([]string(nil), names...)
}

// SetForeignContainers makes the project report containers the compose file did
// not create.
func (e *Engine) SetForeignContainers(containers ...backup.Container) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.foreign = append([]backup.Container(nil), containers...)
}

// RemoveVolume deletes a volume and its contents, the way "compose down -v"
// does. Its contents go through the engine's own root, so the deletion is
// confined to the directory the engine was constructed with.
func (e *Engine) RemoveVolume(name string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.volumes[name]; !ok {
		return &UnknownVolumeError{Name: name}
	}
	if err := e.root.RemoveAll(VolumeDir(name)); err != nil {
		return err
	}
	delete(e.volumes, name)
	return nil
}

// Digest reports the SHA-256 of some bytes in the form sha256sum prints, so a
// test can rebuild a checksum list for an archive it edited on purpose.
func Digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Seed writes files into a volume, creating parent directories as needed. Keys
// are slash-separated paths relative to the volume root.
func (e *Engine) Seed(name string, files map[string]string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.volumes[name]; !ok {
		return &UnknownVolumeError{Name: name}
	}
	return Seed(e.root, VolumeDir(name), files)
}

// Seed writes a set of files into a directory inside a root. It is the one way
// tests put content on disk, so no test can build a fixture outside its root.
func Seed(root *backup.Root, dir string, files map[string]string) error {
	if err := root.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, name := range sortedKeys(files) {
		full := dir + "/" + name
		if i := strings.LastIndex(full, "/"); i > 0 {
			if err := root.MkdirAll(full[:i], 0o755); err != nil {
				return err
			}
		}
		if err := root.WriteFile(full, []byte(files[name]), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// Files reads back every regular file under a directory inside a root, keyed by
// its path relative to that directory.
func Files(root *backup.Root, dir string) (map[string]string, error) {
	out := make(map[string]string)
	if err := collect(root, dir, "", out); err != nil {
		return nil, err
	}
	return out, nil
}

func collect(root *backup.Root, dir, prefix string, out map[string]string) error {
	entries, err := root.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		child := dir + "/" + entry.Name()
		name := prefix + entry.Name()
		if entry.IsDir() {
			if err := collect(root, child, name+"/", out); err != nil {
				return err
			}
			continue
		}
		data, err := root.ReadFile(child)
		if err != nil {
			return err
		}
		out[name] = string(data)
	}
	return nil
}

// VolumeFiles reads back a volume's contents.
func (e *Engine) VolumeFiles(name string) (map[string]string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.volumes[name]; !ok {
		return nil, &UnknownVolumeError{Name: name}
	}
	return Files(e.root, VolumeDir(name))
}

// Running lists the services currently running, in name order.
func (e *Engine) Running() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.runningLocked()
}

func (e *Engine) runningLocked() []string {
	var names []string
	for _, s := range e.services {
		if s.running {
			names = append(names, s.name)
		}
	}
	sort.Strings(names)
	return names
}

// Calls reports every operation the engine has performed, in order. Tests assert
// on it to prove that a refused operation really did not reach the runtime.
func (e *Engine) Calls() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.calls...)
}

// Called reports whether any recorded call begins with prefix.
func (e *Engine) Called(prefix string) bool {
	for _, c := range e.Calls() {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

// ResetCalls clears the recorded call log, so a test can set up state and then
// assert only on what the code under test did.
func (e *Engine) ResetCalls() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls = nil
}

func (e *Engine) record(call string) {
	e.calls = append(e.calls, call)
}

// --- backup.Docker --------------------------------------------------------

// ForeignContainers lists containers the compose file did not create.
func (e *Engine) ForeignContainers(context.Context) ([]backup.Container, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.record("foreign-containers")
	return append([]backup.Container(nil), e.foreign...), nil
}

// ComposeVolumes lists the logical volume names the compose file defines.
func (e *Engine) ComposeVolumes(context.Context) ([]string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.composeVolumes...), nil
}

// ComposeServices lists the services the compose file defines.
func (e *Engine) ComposeServices(context.Context) ([]backup.ComposeService, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]backup.ComposeService, 0, len(e.services))
	for _, s := range e.services {
		out = append(out, backup.ComposeService{
			Name:  s.name,
			Image: s.image,
			Binds: append([]backup.Bind(nil), s.binds...),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// RunningServices lists services with a running container.
func (e *Engine) RunningServices(context.Context) ([]string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.runningLocked(), nil
}

// StopServices stops the named services.
func (e *Engine) StopServices(_ context.Context, services []string) error {
	return e.setRunning("stop", services, false)
}

// StartServices starts the named services.
func (e *Engine) StartServices(_ context.Context, services []string) error {
	return e.setRunning("start", services, true)
}

func (e *Engine) setRunning(action string, names []string, running bool) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.record(action + ":" + strings.Join(names, ","))
	for _, name := range names {
		found := false
		for _, s := range e.services {
			if s.name == name {
				s.running, found = running, true
			}
		}
		if !found {
			return &UnknownServiceError{Name: name}
		}
	}
	return nil
}

// CreateVolumes materializes the compose file's volumes with the project label.
func (e *Engine) CreateVolumes(context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.record("create-volumes")
	for _, logical := range e.composeVolumes {
		name := e.projectLocked() + "_" + logical
		if _, ok := e.volumes[name]; ok {
			continue
		}
		if err := e.root.MkdirAll(VolumeDir(name), 0o755); err != nil {
			return err
		}
		e.volumes[name] = &volume{project: e.projectLocked()}
	}
	return nil
}

// projectLocked reports the project the engine's own volumes are labeled with.
// It is derived from the volumes already present so that CreateVolumes labels
// new ones the same way compose would.
func (e *Engine) projectLocked() string {
	for _, v := range e.volumes {
		if v.project != "" {
			return v.project
		}
	}
	return ""
}

// UpDetached starts every service.
func (e *Engine) UpDetached(context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.record("up-detached")
	for _, s := range e.services {
		s.running = true
	}
	return nil
}

// VolumeNames lists every volume, in name order.
func (e *Engine) VolumeNames(context.Context) ([]string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	names := make([]string, 0, len(e.volumes))
	for name := range e.volumes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// VolumeProject reports a volume's compose project label.
func (e *Engine) VolumeProject(_ context.Context, name string) (string, bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	v, ok := e.volumes[name]
	if !ok {
		return "", false, nil
	}
	return v.project, true, nil
}

// VolumeBytes reports the size of a volume's contents.
func (e *Engine) VolumeBytes(_ context.Context, name string) (int64, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.volumes[name]; !ok {
		return 0, &UnknownVolumeError{Name: name}
	}
	return e.root.SizeOf(VolumeDir(name))
}

// transferVolume is the shape both directions share: run the caller's hook,
// take the lock, prove the volume exists, record the operation, and only then
// move any bytes. Holding the lock across the transfer is deliberate — it is
// what makes a concurrent export and import of one volume serialize, as they do
// against a real daemon.
func (e *Engine) transferVolume(ctx context.Context, name, op string,
	hook func(context.Context, string) error, move func(dir string) error) error {
	if hook != nil {
		if err := hook(ctx, name); err != nil {
			return err
		}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.volumes[name]; !ok {
		return &UnknownVolumeError{Name: name}
	}
	e.record(op + ":" + name)
	return move(VolumeDir(name))
}

// ExportVolume streams a volume's contents as an uncompressed tar.
func (e *Engine) ExportVolume(ctx context.Context, name string, w io.Writer) error {
	return e.transferVolume(ctx, name, "export", e.OnExport, func(dir string) error {
		_, err := backup.WriteTree(e.root, dir, w)
		return err
	})
}

// ImportVolume replaces a volume's contents with a tar stream, emptying it
// first, exactly as the two helper containers do against a real daemon.
func (e *Engine) ImportVolume(ctx context.Context, name string, r io.Reader) error {
	return e.transferVolume(ctx, name, "import", e.OnImport, func(dir string) error {
		_, err := backup.ExtractTree(e.root, dir, r)
		return err
	})
}

// ImageID resolves an image reference to a stable synthetic content id.
func (e *Engine) ImageID(_ context.Context, image string) (string, error) {
	if image == "" {
		return "", nil
	}
	return "sha256:" + strings.ReplaceAll(strings.ReplaceAll(image, ":", "-"), "/", "-"), nil
}

// Versions reports the engine's declared versions.
func (e *Engine) Versions(context.Context) (backup.Versions, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.versions, nil
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

var _ backup.Docker = (*Engine)(nil)
