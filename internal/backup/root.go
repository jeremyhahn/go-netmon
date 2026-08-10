package backup

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Root is the only way this package touches the filesystem.
//
// It anchors every operation on one explicitly chosen directory and refuses any
// path that would leave it. Containment is not a string comparison: the
// underlying os.Root resolves each path component with openat2/O_NOFOLLOW
// semantics, so ".." components, absolute paths and symbolic links pointing
// outside are refused by the kernel, not by this code guessing at what a path
// means.
//
// A Root also carries the reason it was opened. Every destructive plan this
// package prints names the roots it is about to write through, so an operator
// reading the plan can see the blast radius before anything happens.
//
// The zero value is not usable; construct one with Open, Create or Ensure.
type Root struct {
	root    *os.Root
	dir     string
	purpose string
}

// Open anchors a root on an existing directory.
//
// purpose is a short human phrase naming what the root is for; it appears in
// every error and in the operator-facing plan.
func Open(purpose, dir string) (*Root, error) {
	abs, err := absDir(purpose, dir)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, &RootOpenError{Purpose: purpose, Dir: abs, Reason: "cannot be inspected", Err: err}
	}
	if !info.IsDir() {
		return nil, &RootOpenError{Purpose: purpose, Dir: abs, Reason: "is not a directory"}
	}
	r, err := os.OpenRoot(abs)
	if err != nil {
		return nil, &RootOpenError{Purpose: purpose, Dir: abs, Reason: "cannot be opened", Err: err}
	}
	return &Root{root: r, dir: abs, purpose: purpose}, nil
}

// Create makes dir's final path element inside its parent and anchors a root on
// it. The parent must already exist and dir must not.
//
// Exactly one directory level is created, inside a parent the operator named.
// Creating a chain of missing directories from a mistyped path is how a tool
// ends up writing somewhere nobody intended, so it is not offered.
func Create(purpose, dir string) (*Root, error) {
	parent, base, err := splitDir(purpose, dir)
	if err != nil {
		return nil, err
	}
	// A root is a directory handle; closing it cannot affect what was created
	// through it.
	defer func() { _ = parent.Close() }()

	if err := parent.Mkdir(base, 0o755); err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, &ExistsError{Dir: filepath.Join(parent.dir, base)}
		}
		return nil, err
	}
	return parent.Sub(purpose, base)
}

// Ensure anchors a root on dir, creating that one directory level inside its
// existing parent if it is not there yet.
func Ensure(purpose, dir string) (*Root, error) {
	abs, err := absDir(purpose, dir)
	if err != nil {
		return nil, err
	}
	if _, statErr := os.Stat(abs); statErr == nil {
		return Open(purpose, abs)
	}
	r, err := Create(purpose, abs)
	if err != nil {
		var exists *ExistsError
		if errors.As(err, &exists) {
			return Open(purpose, abs)
		}
		return nil, err
	}
	return r, nil
}

// splitDir opens a root on dir's parent and returns it with dir's base name.
func splitDir(purpose, dir string) (*Root, string, error) {
	abs, err := absDir(purpose, dir)
	if err != nil {
		return nil, "", err
	}
	parentDir, base := filepath.Split(strings.TrimSuffix(abs, string(filepath.Separator)))
	parentDir = strings.TrimSuffix(parentDir, string(filepath.Separator))
	if parentDir == "" {
		parentDir = string(filepath.Separator)
	}
	if base == "" || base == "." || base == ".." {
		return nil, "", &RootOpenError{Purpose: purpose, Dir: abs, Reason: "has no directory name to create"}
	}
	parent, err := Open(purpose+" parent", parentDir)
	if err != nil {
		return nil, "", err
	}
	return parent, base, nil
}

// absDir normalises a caller-supplied directory to an absolute, cleaned path.
func absDir(purpose, dir string) (string, error) {
	if strings.TrimSpace(dir) == "" {
		return "", &RootOpenError{Purpose: purpose, Dir: "<empty>", Reason: "no directory was given"}
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", &RootOpenError{Purpose: purpose, Dir: dir, Reason: "cannot be made absolute", Err: err}
	}
	return filepath.Clean(abs), nil
}

// ResolveDir turns a caller-supplied directory into the absolute, symlink-free
// path a root would be anchored on, without opening anything.
//
// Deciding WHICH directory to make a root of is the one judgement os.Root cannot
// make: containment bounds the paths reachable inside a root, not the choice of
// the root itself. That choice therefore gets its own resolution step here and
// its own checks in the caller, and the resolved path is what every plan prints.
//
// A path whose final element does not exist yet resolves through its parent, so
// a destination that is about to be created still resolves to a real location.
func ResolveDir(purpose, dir string) (string, error) {
	abs, err := absDir(purpose, dir)
	if err != nil {
		return "", err
	}
	if resolved, evalSymlinksErr := filepath.EvalSymlinks(abs); evalSymlinksErr == nil {
		return resolved, nil
	}
	parent, base := filepath.Split(strings.TrimSuffix(abs, string(filepath.Separator)))
	parent = strings.TrimSuffix(parent, string(filepath.Separator))
	if parent == "" {
		parent = string(filepath.Separator)
	}
	resolvedParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return "", &RootOpenError{Purpose: purpose, Dir: abs, Reason: "its parent directory does not exist", Err: err}
	}
	return filepath.Join(resolvedParent, base), nil
}

// Purpose reports why this root was opened.
func (r *Root) Purpose() string { return r.purpose }

// Path reports the absolute directory this root is anchored on.
func (r *Root) Path() string { return r.dir }

// Close releases the root's directory handle.
func (r *Root) Close() error { return r.root.Close() }

// Sub anchors a new root on a directory inside this one.
func (r *Root) Sub(purpose, name string) (*Root, error) {
	rel, err := r.rel(name)
	if err != nil {
		return nil, err
	}
	sub, err := r.root.OpenRoot(rel)
	if err != nil {
		return nil, &RootOpError{Op: "openroot", Purpose: r.purpose, Root: r.dir, Path: name, Err: err}
	}
	return &Root{root: sub, dir: filepath.Join(r.dir, filepath.FromSlash(rel)), purpose: purpose}, nil
}

// rel validates a caller-supplied path and returns it in slash form relative to
// the root.
//
// os.Root refuses these paths too. Checking first turns a bare "path escapes
// from parent" from the kernel into an error that names the root, its purpose
// and what was wrong, which is what an operator staring at a failed restore
// needs. It is belt and braces, never a substitute: the kernel check is what
// makes containment true.
func (r *Root) rel(name string) (string, error) {
	fail := func(reason string) (string, error) {
		return "", &PathEscapeError{Purpose: r.purpose, Root: r.dir, Path: name, Reason: reason}
	}
	if name == "" {
		return fail("the path is empty")
	}
	if filepath.IsAbs(name) || strings.HasPrefix(name, "/") {
		return fail("absolute paths name a location outside the root")
	}
	if strings.ContainsRune(name, 0) {
		return fail("the path contains a NUL byte")
	}
	slashed := filepath.ToSlash(name)
	cleaned := path.Clean(slashed)
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return fail("the path climbs above the root with \"..\"")
	}
	return cleaned, nil
}

// join validates base and name separately and returns their join.
func (r *Root) join(base, name string) (string, error) {
	rel, err := r.rel(base)
	if err != nil {
		return "", err
	}
	if _, err := r.rel(name); err != nil {
		return "", err
	}
	joined := path.Join(rel, path.Clean(filepath.ToSlash(name)))
	return r.rel(joined)
}

func (r *Root) opErr(op, name string, err error) error {
	if err == nil {
		return nil
	}
	return &RootOpError{Op: op, Purpose: r.purpose, Root: r.dir, Path: name, Err: err}
}

// Stat reports the named file's metadata, following symbolic links.
func (r *Root) Stat(name string) (fs.FileInfo, error) {
	rel, err := r.rel(name)
	if err != nil {
		return nil, err
	}
	info, err := r.root.Stat(rel)
	return info, r.opErr("stat", name, err)
}

// Lstat reports the named file's metadata without following a final symlink.
func (r *Root) Lstat(name string) (fs.FileInfo, error) {
	rel, err := r.rel(name)
	if err != nil {
		return nil, err
	}
	info, err := r.root.Lstat(rel)
	return info, r.opErr("lstat", name, err)
}

// Exists reports whether the named path is present.
func (r *Root) Exists(name string) (bool, error) {
	_, err := r.Lstat(name)
	if err == nil {
		return true, nil
	}
	if isNotExist(err) {
		return false, nil
	}
	return false, err
}

// ReadDir lists the entries of a directory inside the root, sorted by name.
func (r *Root) ReadDir(name string) ([]fs.DirEntry, error) {
	rel, err := r.rel(name)
	if err != nil {
		return nil, err
	}
	f, err := r.root.Open(rel)
	if err != nil {
		return nil, r.opErr("opendir", name, err)
	}
	// Read-only; a close error cannot change the entries already read.
	defer func() { _ = f.Close() }()
	entries, err := f.ReadDir(-1)
	if err != nil {
		return nil, r.opErr("readdir", name, err)
	}
	sortEntries(entries)
	return entries, nil
}

// Open opens a file inside the root for reading.
func (r *Root) Open(name string) (*os.File, error) {
	rel, err := r.rel(name)
	if err != nil {
		return nil, err
	}
	f, err := r.root.Open(rel)
	return f, r.opErr("open", name, err)
}

// ReadFile reads a whole file inside the root.
func (r *Root) ReadFile(name string) ([]byte, error) {
	rel, err := r.rel(name)
	if err != nil {
		return nil, err
	}
	b, err := r.root.ReadFile(rel)
	return b, r.opErr("read", name, err)
}

// Create makes or truncates a file inside the root and opens it for writing.
func (r *Root) Create(name string) (*os.File, error) {
	rel, err := r.rel(name)
	if err != nil {
		return nil, err
	}
	f, err := r.root.Create(rel)
	return f, r.opErr("create", name, err)
}

// WriteFile writes a whole file inside the root.
func (r *Root) WriteFile(name string, data []byte, perm fs.FileMode) error {
	rel, err := r.rel(name)
	if err != nil {
		return err
	}
	return r.opErr("write", name, r.root.WriteFile(rel, data, perm))
}

// Mkdir creates one directory inside the root.
func (r *Root) Mkdir(name string, perm fs.FileMode) error {
	rel, err := r.rel(name)
	if err != nil {
		return err
	}
	return r.opErr("mkdir", name, r.root.Mkdir(rel, perm))
}

// MkdirAll creates a directory inside the root along with any missing parents,
// all of which are still resolved inside the root.
func (r *Root) MkdirAll(name string, perm fs.FileMode) error {
	rel, err := r.rel(name)
	if err != nil {
		return err
	}
	return r.opErr("mkdirall", name, r.root.MkdirAll(rel, perm))
}

// Remove deletes one file or empty directory inside the root.
func (r *Root) Remove(name string) error {
	rel, err := r.rel(name)
	if err != nil {
		return err
	}
	return r.opErr("remove", name, r.root.Remove(rel))
}

// RemoveAll deletes a path inside the root and everything beneath it.
//
// A symbolic link is removed as a link: its target is never followed, so a link
// pointing outside cannot be used to delete anything out there.
func (r *Root) RemoveAll(name string) error {
	rel, err := r.rel(name)
	if err != nil {
		return err
	}
	if rel == "." {
		return &PathEscapeError{
			Purpose: r.purpose,
			Root:    r.dir,
			Path:    name,
			Reason:  "the root directory itself is not removable; empty it instead",
		}
	}
	return r.opErr("removeall", name, r.root.RemoveAll(rel))
}

// Rename moves a path inside the root. Both ends stay inside it.
func (r *Root) Rename(from, to string) error {
	relFrom, err := r.rel(from)
	if err != nil {
		return err
	}
	relTo, err := r.rel(to)
	if err != nil {
		return err
	}
	return r.opErr("rename", from+" -> "+to, r.root.Rename(relFrom, relTo))
}

// Symlink creates a symbolic link inside the root.
//
// The target is refused unless it also resolves inside the root. os.Root would
// refuse to follow an escaping link later, but a link that cannot be followed is
// a restored tree that silently does not work, so it is rejected at the point it
// would be written instead.
func (r *Root) Symlink(target, name string) error {
	rel, err := r.rel(name)
	if err != nil {
		return err
	}
	if err := r.checkLinkTarget(rel, name, target); err != nil {
		return err
	}
	return r.opErr("symlink", name, r.root.Symlink(target, rel))
}

func (r *Root) checkLinkTarget(rel, name, target string) error {
	fail := func(reason string) error {
		return &SymlinkEscapeError{Purpose: r.purpose, Root: r.dir, Name: name, Target: target, Reason: reason}
	}
	if target == "" {
		return fail("the target is empty")
	}
	if filepath.IsAbs(target) || strings.HasPrefix(target, "/") {
		return fail("the target is absolute")
	}
	resolved := path.Join(path.Dir(rel), filepath.ToSlash(target))
	if resolved == ".." || strings.HasPrefix(path.Clean(resolved), "../") {
		return fail("the target climbs above the root")
	}
	return nil
}

// Readlink reports the target of a symbolic link inside the root.
func (r *Root) Readlink(name string) (string, error) {
	rel, err := r.rel(name)
	if err != nil {
		return "", err
	}
	target, err := r.root.Readlink(rel)
	return target, r.opErr("readlink", name, err)
}

// Chmod sets a file's permission bits inside the root.
func (r *Root) Chmod(name string, perm fs.FileMode) error {
	rel, err := r.rel(name)
	if err != nil {
		return err
	}
	return r.opErr("chmod", name, r.root.Chmod(rel, perm))
}

// Chtimes sets a file's access and modification times inside the root.
func (r *Root) Chtimes(name string, atime, mtime time.Time) error {
	rel, err := r.rel(name)
	if err != nil {
		return err
	}
	return r.opErr("chtimes", name, r.root.Chtimes(rel, atime, mtime))
}

// Empty removes everything under a directory inside the root, leaving the
// directory itself in place.
//
// Restores replace rather than merge, so the destination is emptied first. The
// entries are listed through the root and removed through the root one at a
// time; there is no path string handed to anything that could interpret it as
// something outside.
func (r *Root) Empty(name string) error {
	rel, err := r.rel(name)
	if err != nil {
		return err
	}
	entries, err := r.ReadDir(rel)
	if err != nil {
		return err
	}
	for _, e := range entries {
		child, err := r.join(rel, e.Name())
		if err != nil {
			return err
		}
		if err := r.RemoveAll(child); err != nil {
			return err
		}
	}
	return nil
}

// CountFiles reports how many regular files live under a directory inside the
// root.
func (r *Root) CountFiles(name string) (int, error) {
	n := 0
	err := r.walk(name, func(_ string, info fs.FileInfo) error {
		if info.Mode().IsRegular() {
			n++
		}
		return nil
	})
	if isNotExist(err) {
		return 0, nil
	}
	return n, err
}

// SizeOf reports the total apparent size of the regular files under a directory
// inside the root.
func (r *Root) SizeOf(name string) (int64, error) {
	var total int64
	err := r.walk(name, func(_ string, info fs.FileInfo) error {
		if info.Mode().IsRegular() {
			total += info.Size()
		}
		return nil
	})
	if isNotExist(err) {
		return 0, nil
	}
	return total, err
}

// walk visits every path under name, depth first, without following symlinks.
func (r *Root) walk(name string, fn func(rel string, info fs.FileInfo) error) error {
	rel, err := r.rel(name)
	if err != nil {
		return err
	}
	info, err := r.Lstat(rel)
	if err != nil {
		return err
	}
	if fnErr := fn(rel, info); fnErr != nil {
		return fnErr
	}
	if !info.IsDir() {
		return nil
	}
	entries, err := r.ReadDir(rel)
	if err != nil {
		return err
	}
	for _, e := range entries {
		child, err := r.join(rel, e.Name())
		if err != nil {
			return err
		}
		if err := r.walk(child, fn); err != nil {
			return err
		}
	}
	return nil
}

func sortEntries(entries []fs.DirEntry) {
	slices.SortFunc(entries, func(a, b fs.DirEntry) int {
		return strings.Compare(a.Name(), b.Name())
	})
}

// isNotExist reports whether err, possibly wrapped in a RootOpError, is a
// "does not exist" condition.
func isNotExist(err error) bool {
	return errors.Is(err, os.ErrNotExist)
}
