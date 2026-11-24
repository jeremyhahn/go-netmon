package backup

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
)

// DefaultMIBMountTarget is the container path the MIB source tree is
// bind-mounted at.
//
// It is the contract between the images and config.yaml's mib.paths. The host
// path is whatever the compose file says it is, and is read back out of the
// rendered compose file rather than assumed.
const DefaultMIBMountTarget = "/app/mibs"

// systemDirs are directories a MIB source tree must never resolve to.
var systemDirs = map[string]bool{
	"/": true, "/home": true, "/root": true, "/usr": true, "/etc": true,
	"/var": true, "/opt": true, "/srv": true, "/tmp": true, "/mnt": true,
	"/media": true, "/bin": true, "/sbin": true, "/lib": true, "/boot": true,
	"/dev": true, "/proc": true, "/sys": true,
}

// TreeLocation is a resolved, checked MIB source tree.
type TreeLocation struct {
	// Dir is the absolute, symlink-free directory the tree lives in.
	Dir string
	// Origin says how Dir was arrived at. A path that appears in a destructive
	// plan should say where it came from.
	Origin string
	// MountTarget is the container path the tree is mounted at.
	MountTarget string
}

// treeResolver locates and vets the MIB source tree.
type treeResolver struct {
	docker      Docker
	mountTarget string
	repoRoot    string
	homeDir     string
}

// resolve locates the MIB tree from an override or the rendered compose file.
func (t *treeResolver) resolve(ctx context.Context, override string) (TreeLocation, error) {
	dir, origin := strings.TrimSpace(override), "explicit override"
	if dir == "" {
		services, err := t.docker.ComposeServices(ctx)
		if err != nil {
			return TreeLocation{}, err
		}
		sources := bindSources(services, t.mountTarget)
		if len(sources) != 1 {
			return TreeLocation{}, &TreeResolveError{Target: t.mountTarget, Sources: sources}
		}
		dir, origin = sources[0], "compose bind mount at "+t.mountTarget
	} else if !filepath.IsAbs(dir) {
		return TreeLocation{}, &UnsafeTreeError{Dir: dir, Reason: "the path must be absolute"}
	}

	resolved, err := ResolveDir("MIB source tree", dir)
	if err != nil {
		return TreeLocation{}, err
	}
	if err := t.check(resolved); err != nil {
		return TreeLocation{}, err
	}
	return TreeLocation{Dir: resolved, Origin: origin, MountTarget: t.mountTarget}, nil
}

// check refuses destinations that must never be emptied.
func (t *treeResolver) check(dir string) error {
	if systemDirs[dir] {
		return &UnsafeTreeError{Dir: dir, Reason: "it is a system directory"}
	}
	if t.homeDir != "" && dir == t.homeDir {
		return &UnsafeTreeError{Dir: dir, Reason: "it is the home directory"}
	}
	if t.repoRoot != "" && dir == t.repoRoot {
		return &UnsafeTreeError{Dir: dir, Reason: "it is the repository root"}
	}
	if strings.Count(strings.Trim(dir, "/"), "/") < 1 {
		return &UnsafeTreeError{Dir: dir, Reason: "it is too close to the filesystem root to be a deployment layout"}
	}
	return nil
}

// treeRootPurpose is what a root opened by treeRoot is for. It appears in every
// containment refusal raised through that root, so it says which sandbox the
// refused path tried to leave.
const treeRootPurpose = "MIB source tree parent"

// treeRoot anchors a root on a tree's parent and returns the tree's directory name.
func treeRoot(loc TreeLocation) (*Root, string, error) {
	parent, base := filepath.Split(strings.TrimSuffix(loc.Dir, string(filepath.Separator)))
	parent = strings.TrimSuffix(parent, string(filepath.Separator))
	if parent == "" || base == "" {
		return nil, "", &UnsafeTreeError{Dir: loc.Dir, Reason: "it has no parent directory to confine the operation to"}
	}
	root, err := Open(treeRootPurpose, parent)
	if err != nil {
		return nil, "", err
	}
	return root, base, nil
}

// treeState reports what is already in a tree destination.
type treeState struct {
	Files int
	Bytes int64
}

// inspectTree counts what a tree destination currently holds, through a root
// confined to its parent.
func inspectTree(loc TreeLocation) (treeState, error) {
	root, base, err := treeRoot(loc)
	if err != nil {
		return treeState{}, err
	}
	// a root is a directory handle; closing it cannot affect what was read through it.
	defer func() { _ = root.Close() }()

	files, err := root.CountFiles(base)
	if err != nil {
		return treeState{}, err
	}
	size, err := root.SizeOf(base)
	if err != nil {
		return treeState{}, err
	}
	return treeState{Files: files, Bytes: size}, nil
}

// isTreeNotMounted reports whether a resolve failed only because no MIB mount exists.
func isTreeNotMounted(err error) bool {
	var resolve *TreeResolveError
	return errors.As(err, &resolve) && len(resolve.Sources) == 0
}
