package backup

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// escapeFixture builds a sandbox with something worth stealing just outside it.
//
// Layout, all under t.TempDir():
//
//	base/outside.txt        the file every escape attempt is aiming at
//	base/outside-dir/       a directory an escape could empty
//	base/inside/            the root under test
//	base/inside/escape ->   ../                (symlink out of the root)
//	base/inside/absolute -> /etc               (absolute symlink)
func escapeFixture(t *testing.T) (root *Root, base string) {
	t.Helper()
	base = t.TempDir()

	outer, openErr := Open("fixture", base)
	if openErr != nil {
		t.Fatalf("open fixture root: %v", openErr)
	}
	t.Cleanup(func() { outer.Close() })

	if err := outer.WriteFile("outside.txt", []byte("do not touch"), 0o644); err != nil {
		t.Fatalf("seed outside file: %v", err)
	}
	if err := outer.MkdirAll("outside-dir/nested", 0o755); err != nil {
		t.Fatalf("seed outside dir: %v", err)
	}
	if err := outer.WriteFile("outside-dir/nested/keep.txt", []byte("keep"), 0o644); err != nil {
		t.Fatalf("seed outside dir file: %v", err)
	}
	if err := outer.MkdirAll("inside/sub", 0o755); err != nil {
		t.Fatalf("seed inside: %v", err)
	}

	// os.Symlink, not Root.Symlink: Root refuses to create a link that leaves
	// the root, which is itself part of the containment. Building the hostile
	// condition the kernel check exists for therefore has to go around the API.
	// It is the only filesystem call in this package's tests that does, and it
	// writes inside t.TempDir().
	if err := os.Symlink(base, filepath.Join(base, "inside", "escape")); err != nil {
		t.Fatalf("create escaping symlink: %v", err)
	}
	if err := os.Symlink("/etc", filepath.Join(base, "inside", "absolute")); err != nil {
		t.Fatalf("create absolute symlink: %v", err)
	}

	root, err := Open("sandbox", filepath.Join(base, "inside"))
	if err != nil {
		t.Fatalf("open sandbox root: %v", err)
	}
	t.Cleanup(func() { root.Close() })
	return root, base
}

// TestRootRefusesEveryEscape is the test this package exists for.
//
// The shell tooling it replaces expressed containment as path strings and a list
// of directories it hoped never to be pointed at; a test harness built on the
// same idea aimed "find -delete" at /usr on the operator's host. Here every path
// is resolved by the kernel relative to an open directory handle, so the three
// ways out - "..", an absolute path, and a symbolic link pointing outside - are
// not merely detected but impossible to follow.
//
// Each case asserts on the refusal AND on the target still being there
// afterwards: a test that only checked for an error would pass just as happily
// against an implementation that deleted the file and then complained.
func TestRootRefusesEveryEscape(t *testing.T) {
	root, base := escapeFixture(t)

	cases := []struct {
		name string
		path string
		why  string
	}{
		{"parent traversal", "../outside.txt", "\"..\" climbs above the root"},
		{"deep parent traversal", "sub/../../outside.txt", "a cleaned path still climbs above the root"},
		{"absolute path", "/etc/passwd", "an absolute path names a location outside the root"},
		{"absolute path into the sandbox's own parent", filepath.Join(base, "outside.txt"), "an absolute path is absolute even when it points nearby"},
		{"symlink to the parent directory", "escape/outside.txt", "the link resolves outside and the kernel refuses to follow it"},
		{"symlink to an absolute path", "absolute/passwd", "the link is absolute"},
		{"symlink used as a directory to write into", "escape/planted.txt", "writing through an escaping link is still an escape"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mutations := map[string]func() error{
				"open":      func() error { _, err := root.Open(tc.path); return err },
				"readfile":  func() error { _, err := root.ReadFile(tc.path); return err },
				"create":    func() error { _, err := root.Create(tc.path); return err },
				"writefile": func() error { return root.WriteFile(tc.path, []byte("planted"), 0o644) },
				"remove":    func() error { return root.Remove(tc.path) },
				"removeall": func() error { return root.RemoveAll(tc.path) },
				"mkdirall":  func() error { return root.MkdirAll(tc.path, 0o755) },
				"rename":    func() error { return root.Rename("sub", tc.path) },
				"chmod":     func() error { return root.Chmod(tc.path, 0o600) },
				"symlink":   func() error { return root.Symlink("x", tc.path) },
				"sub":       func() error { _, err := root.Sub("escaped", tc.path); return err },
			}
			for op, run := range mutations {
				if err := run(); err == nil {
					t.Fatalf("%s(%q) was allowed; %s", op, tc.path, tc.why)
				}
			}
		})
	}

	// Nothing outside the root may have been touched by any of the above.
	outer, err := Open("verification", base)
	if err != nil {
		t.Fatalf("reopen fixture: %v", err)
	}
	defer outer.Close()

	data, err := outer.ReadFile("outside.txt")
	if err != nil {
		t.Fatalf("the file outside the root is gone: %v", err)
	}
	if string(data) != "do not touch" {
		t.Fatalf("the file outside the root was modified: %q", data)
	}
	if _, err := outer.Stat("outside-dir/nested/keep.txt"); err != nil {
		t.Fatalf("the directory outside the root was disturbed: %v", err)
	}
}

// TestRootEscapeErrorsAreTyped proves the refusals are a condition callers can
// recognize, not an opaque syscall error.
func TestRootEscapeErrorsAreTyped(t *testing.T) {
	root, _ := escapeFixture(t)

	var escape *PathEscapeError
	if err := root.WriteFile("../outside.txt", nil, 0o644); !errors.As(err, &escape) {
		t.Fatalf("want *PathEscapeError, got %T: %v", err, err)
	}
	if !strings.Contains(escape.Error(), root.Path()) {
		t.Fatalf("the error should name the root it refused to leave: %v", escape)
	}
	if escape.Purpose != "sandbox" {
		t.Fatalf("the error should carry the root's stated purpose, got %q", escape.Purpose)
	}

	// The symlink case cannot be caught by inspecting the path: "escape/x" is a
	// perfectly ordinary relative path. It is the kernel that refuses it, and
	// that is what makes containment real rather than advisory.
	err := root.WriteFile("escape/planted.txt", []byte("x"), 0o644)
	if errors.As(err, &escape) {
		t.Fatalf("the symlink case must be refused by the kernel, not by path inspection: %v", err)
	}
	var opErr *RootOpError
	if !errors.As(err, &opErr) {
		t.Fatalf("want *RootOpError from the kernel refusal, got %T: %v", err, err)
	}
	if !strings.Contains(opErr.Error(), "escapes") {
		t.Fatalf("want the kernel's escape refusal, got %v", opErr)
	}
}

// TestRootSymlinkTargetsAreConfined covers links this package would create while
// restoring a tree: a link inside the root is fine, one pointing out is not.
func TestRootSymlinkTargetsAreConfined(t *testing.T) {
	root, _ := escapeFixture(t)

	if err := root.WriteFile("sub/real.txt", []byte("real"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := root.Symlink("real.txt", "sub/link.txt"); err != nil {
		t.Fatalf("a link inside the root should be allowed: %v", err)
	}
	target, err := root.Readlink("sub/link.txt")
	if err != nil || target != "real.txt" {
		t.Fatalf("readlink = %q, %v", target, err)
	}

	var escape *SymlinkEscapeError
	if err := root.Symlink("../../outside.txt", "sub/out.txt"); !errors.As(err, &escape) {
		t.Fatalf("want *SymlinkEscapeError for a climbing target, got %T: %v", err, err)
	}
	if err := root.Symlink("/etc/passwd", "sub/abs.txt"); !errors.As(err, &escape) {
		t.Fatalf("want *SymlinkEscapeError for an absolute target, got %T: %v", err, err)
	}
	if err := root.Symlink("", "sub/empty.txt"); !errors.As(err, &escape) {
		t.Fatalf("want *SymlinkEscapeError for an empty target, got %T: %v", err, err)
	}
}

// TestRootEmptyRemovesOnlyItsOwnContents proves the operation a restore performs
// on the MIB source tree cannot reach past the directory it was given, and that
// an escaping symlink inside is removed as a link rather than followed.
func TestRootEmptyRemovesOnlyItsOwnContents(t *testing.T) {
	root, base := escapeFixture(t)

	if err := root.WriteFile("sub/a.txt", []byte("a"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := root.MkdirAll("sub/deep/deeper", 0o755); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := root.WriteFile("sub/deep/deeper/b.txt", []byte("b"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := root.Empty("."); err != nil {
		t.Fatalf("Empty: %v", err)
	}
	entries, err := root.ReadDir(".")
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("the root should be empty, still holds %d entries", len(entries))
	}

	outer, err := Open("verification", base)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer outer.Close()
	if _, err := outer.Stat("outside.txt"); err != nil {
		t.Fatalf("Empty followed the escaping symlink and deleted outside the root: %v", err)
	}
	if _, err := outer.Stat("outside-dir/nested/keep.txt"); err != nil {
		t.Fatalf("Empty reached a sibling directory: %v", err)
	}
}

// TestRootRemoveAllRefusesTheRootItself makes sure "empty it" and "delete it"
// stay different operations.
func TestRootRemoveAllRefusesTheRootItself(t *testing.T) {
	root, _ := escapeFixture(t)

	var escape *PathEscapeError
	for _, name := range []string{".", "./", "sub/.."} {
		if err := root.RemoveAll(name); !errors.As(err, &escape) {
			t.Fatalf("RemoveAll(%q) should be refused, got %T: %v", name, err, err)
		}
	}
	if _, err := root.Stat("."); err != nil {
		t.Fatalf("the root itself should still be there: %v", err)
	}
}

func TestOpenRejectsBadDirectories(t *testing.T) {
	base := t.TempDir()
	root, err := Open("fixture", base)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer root.Close()
	if err := root.WriteFile("file.txt", []byte("x"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	cases := map[string]string{
		"missing directory": filepath.Join(base, "nope"),
		"a regular file":    filepath.Join(base, "file.txt"),
		"empty path":        "",
		"whitespace only":   "   ",
	}
	for name, dir := range cases {
		t.Run(name, func(t *testing.T) {
			var openErr *RootOpenError
			if _, err := Open("target", dir); !errors.As(err, &openErr) {
				t.Fatalf("want *RootOpenError, got %T: %v", err, err)
			}
		})
	}
}

func TestCreateRefusesAnExistingDirectory(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "backup")

	root, err := Create("backup destination", dir)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	root.Close()

	var exists *ExistsError
	if _, err := Create("backup destination", dir); !errors.As(err, &exists) {
		t.Fatalf("want *ExistsError on the second create, got %T: %v", err, err)
	}
}

func TestCreateRefusesAMissingParent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "missing", "child")
	var openErr *RootOpenError
	if _, err := Create("backup destination", dir); !errors.As(err, &openErr) {
		t.Fatalf("want *RootOpenError when the parent is absent, got %T: %v", err, err)
	}
}

func TestEnsureCreatesOnceAndReopens(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "backups")

	first, err := Ensure("backup destination parent", dir)
	if err != nil {
		t.Fatalf("first Ensure: %v", err)
	}
	if byteErr := first.WriteFile("marker", []byte("1"), 0o644); byteErr != nil {
		t.Fatalf("write: %v", byteErr)
	}
	first.Close()

	second, err := Ensure("backup destination parent", dir)
	if err != nil {
		t.Fatalf("second Ensure: %v", err)
	}
	defer second.Close()
	if data, err := second.ReadFile("marker"); err != nil || string(data) != "1" {
		t.Fatalf("Ensure should reopen the same directory, got %q, %v", data, err)
	}
}

func TestRootFileOperationsRoundTrip(t *testing.T) {
	root, err := Open("sandbox", t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer root.Close()

	if mkdirAllErr := root.MkdirAll("a/b", 0o755); mkdirAllErr != nil {
		t.Fatalf("mkdirall: %v", mkdirAllErr)
	}
	if byteErr := root.WriteFile("a/b/one.txt", []byte("one"), 0o644); byteErr != nil {
		t.Fatalf("writefile: %v", byteErr)
	}
	if byteErr := root.WriteFile("a/two.txt", []byte("two!"), 0o600); byteErr != nil {
		t.Fatalf("writefile: %v", byteErr)
	}

	if got, readFileErr := root.ReadFile("a/b/one.txt"); readFileErr != nil || string(got) != "one" {
		t.Fatalf("readfile = %q, %v", got, readFileErr)
	}
	if ok, existsErr := root.Exists("a/two.txt"); existsErr != nil || !ok {
		t.Fatalf("Exists = %v, %v", ok, existsErr)
	}
	if ok, existsErr := root.Exists("a/three.txt"); existsErr != nil || ok {
		t.Fatalf("Exists on a missing file = %v, %v", ok, existsErr)
	}

	n, err := root.CountFiles(".")
	if err != nil || n != 2 {
		t.Fatalf("CountFiles = %d, %v; want 2", n, err)
	}
	size, err := root.SizeOf(".")
	if err != nil || size != 7 {
		t.Fatalf("SizeOf = %d, %v; want 7", size, err)
	}

	if renameErr := root.Rename("a/two.txt", "a/b/two.txt"); renameErr != nil {
		t.Fatalf("rename: %v", renameErr)
	}
	if ok, _ := root.Exists("a/two.txt"); ok {
		t.Fatal("rename left the source behind")
	}

	when := time.Date(2024, 3, 1, 12, 0, 0, 0, time.UTC)
	if chtimesErr := root.Chtimes("a/b/one.txt", when, when); chtimesErr != nil {
		t.Fatalf("chtimes: %v", chtimesErr)
	}
	info, err := root.Stat("a/b/one.txt")
	if err != nil || !info.ModTime().Equal(when) {
		t.Fatalf("chtimes did not take: %v, %v", info.ModTime(), err)
	}

	if err := root.Remove("a/b/one.txt"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := root.RemoveAll("a"); err != nil {
		t.Fatalf("removeall: %v", err)
	}
	if n, err := root.CountFiles("."); err != nil || n != 0 {
		t.Fatalf("CountFiles after removal = %d, %v", n, err)
	}
}

func TestRootCountersToleratePathsThatAreNotThere(t *testing.T) {
	root, err := Open("sandbox", t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer root.Close()

	if n, err := root.CountFiles("absent"); err != nil || n != 0 {
		t.Fatalf("CountFiles on a missing directory = %d, %v; want 0, nil", n, err)
	}
	if size, err := root.SizeOf("absent"); err != nil || size != 0 {
		t.Fatalf("SizeOf on a missing directory = %d, %v; want 0, nil", size, err)
	}
	if _, err := root.ReadDir("absent"); err == nil {
		t.Fatal("ReadDir on a missing directory should fail")
	}
}

func TestResolveDirFollowsSymlinksAndTolerantOfMissingLeaf(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.Symlink(real, filepath.Join(base, "link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	root, err := Open("fixture", base)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer root.Close()
	if mkdirErr := root.Mkdir("real", 0o755); mkdirErr != nil {
		t.Fatalf("mkdir: %v", mkdirErr)
	}

	resolved, err := ResolveDir("tree", filepath.Join(base, "link"))
	if err != nil {
		t.Fatalf("ResolveDir: %v", err)
	}
	if filepath.Base(resolved) != "real" {
		t.Fatalf("ResolveDir should follow the link, got %q", resolved)
	}

	future := filepath.Join(base, "real", "not-yet")
	resolved, err = ResolveDir("tree", future)
	if err != nil {
		t.Fatalf("ResolveDir on a missing leaf: %v", err)
	}
	if filepath.Base(resolved) != "not-yet" {
		t.Fatalf("ResolveDir = %q", resolved)
	}

	if _, err := ResolveDir("tree", filepath.Join(base, "gone", "deeper")); err == nil {
		t.Fatal("ResolveDir should fail when the parent is missing too")
	}
}
