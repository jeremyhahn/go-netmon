package backup

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// This file is a lint of this tool's own source, run as a test.
//
// The containment in root.go is only worth anything if nothing goes around it.
// A comment asking future contributors not to call os.RemoveAll is not a
// guarantee; a failing test is. Everything below is checked mechanically, so
// adding a call that reaches the filesystem outside a Root, or that hands a
// command line to a shell, breaks the build rather than shipping.

// sourceDirs are the packages this rule covers, relative to the repository root.
//
// They are read through a Root anchored on that repository root, so this file
// obeys the rule it enforces rather than being an exception to it.
var sourceDirs = []string{
	"internal/backup",
	"internal/backup/backuptest",
	"cmd/netmon-backup",
}

// pathFuncs are the os functions that take a path. Every one of them interprets
// its argument against the process's working directory and its own idea of what
// a symbolic link means, which is exactly the reasoning this package refuses to
// do. They belong only in root.go, where they are used to open a root.
var pathFuncs = map[string]bool{
	"Create": true, "CreateTemp": true, "Open": true, "OpenFile": true,
	"ReadFile": true, "WriteFile": true, "ReadDir": true,
	"Mkdir": true, "MkdirAll": true, "MkdirTemp": true,
	"Remove": true, "RemoveAll": true, "Rename": true, "Truncate": true,
	"Chmod": true, "Chown": true, "Lchown": true, "Chtimes": true,
	"Symlink": true, "Link": true, "Readlink": true,
	"Stat": true, "Lstat": true, "DirFS": true, "OpenRoot": true,
	"Chdir": true, "OpenInRoot": true,
}

// filepathFuncs walk or resolve real paths and so must not be used to decide
// what to touch.
var filepathFuncs = map[string]bool{
	"Walk": true, "WalkDir": true, "EvalSymlinks": true, "Glob": true,
}

// shellWords are argv elements that would hand a command line to an interpreter.
// The shell tooling this replaces ran "sh -c 'find /dst -mindepth 1 -delete'";
// its test harness executed that string on the host, and a wrong path in it was
// a delete aimed at the operator's filesystem. There is no shell here at all.
var shellWords = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "dash": true, "-c": true,
	"/bin/sh": true, "/bin/bash": true, "/usr/bin/sh": true, "/usr/bin/bash": true,
	"eval": true, "system": true,
}

// containmentExempt lists the files allowed to call the functions above, and why.
var containmentExempt = map[string]string{
	// root.go IS the containment: it opens roots, and opening one means naming
	// a directory to the os package exactly once, in one place, on purpose.
	"root.go": "defines the containment",
}

// execFile is the only file allowed to import os/exec.
const execFile = "dockercli.go"

// guardFile is this file, which necessarily spells out the words it forbids.
const guardFile = "containment_test.go"

func TestNothingBypassesTheContainment(t *testing.T) {
	source := repoSource(t)
	for _, pkg := range sourceDirs {
		for _, file := range parseDir(t, source, pkg, false) {
			name := filepath.Base(file.path)
			if _, exempt := containmentExempt[name]; exempt {
				continue
			}
			t.Run(pkg+"/"+name, func(t *testing.T) {
				assertNoDirectFilesystemCalls(t, file)
			})
		}
	}
}

// TestTestsAreConfinedToo applies the same rule to the tests, with one stated
// exception: constructing a symbolic link that leaves a root cannot be done
// through the API that refuses to create one, and proving the kernel refuses to
// follow such a link is the point of the escape test.
func TestTestsAreConfinedToo(t *testing.T) {
	allowed := map[string]bool{"Symlink": true}
	source := repoSource(t)
	for _, pkg := range sourceDirs {
		for _, file := range parseDir(t, source, pkg, true) {
			if !strings.HasSuffix(file.path, "_test.go") {
				continue
			}
			t.Run(pkg+"/"+filepath.Base(file.path), func(t *testing.T) {
				forEachCall(file.ast, func(pkgName, fn string, call *ast.CallExpr) {
					if pkgName == "os" && pathFuncs[fn] && !allowed[fn] {
						t.Errorf("%s calls os.%s; tests build fixtures through a Root anchored on t.TempDir()",
							file.path, fn)
					}
					if pkgName == "filepath" && filepathFuncs[fn] {
						t.Errorf("%s calls filepath.%s; tests must not walk real paths", file.path, fn)
					}
				})
			})
		}
	}
}

func TestOnlyOneFileMayExecuteAnything(t *testing.T) {
	source := repoSource(t)
	for _, pkg := range sourceDirs {
		for _, file := range parseDir(t, source, pkg, true) {
			name := filepath.Base(file.path)
			if !importsPackage(file.ast, "os/exec") {
				continue
			}
			if name != execFile {
				t.Errorf("%s/%s imports os/exec; only %s may run a process", pkg, name, execFile)
			}
		}
	}
}

func TestNoArgumentVectorNamesAShell(t *testing.T) {
	source := repoSource(t)
	for _, pkg := range sourceDirs {
		for _, file := range parseDir(t, source, pkg, true) {
			if filepath.Base(file.path) == guardFile {
				continue
			}
			ast.Inspect(file.ast, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				value, err := strconv.Unquote(lit.Value)
				if err != nil || !shellWords[value] {
					return true
				}
				t.Errorf("%s/%s contains the literal %q; nothing here may name a shell or hand it a command line",
					pkg, filepath.Base(file.path), value)
				return true
			})
		}
	}
}

func assertNoDirectFilesystemCalls(t *testing.T, file parsedFile) {
	t.Helper()
	forEachCall(file.ast, func(pkgName, fn string, call *ast.CallExpr) {
		switch {
		case pkgName == "os" && pathFuncs[fn]:
			t.Errorf("%s calls os.%s; every path this package touches goes through a Root", file.path, fn)
		case pkgName == "filepath" && filepathFuncs[fn]:
			t.Errorf("%s calls filepath.%s; resolving real paths belongs in root.go", file.path, fn)
		case pkgName == "ioutil":
			t.Errorf("%s uses ioutil.%s", file.path, fn)
		}
	})
}

type parsedFile struct {
	path string
	ast  *ast.File
}

// repoSource opens the repository root that every source directory is read
// through.
func repoSource(t *testing.T) *Root {
	t.Helper()

	root, err := Open("repository source tree", "../..")
	if err != nil {
		t.Fatalf("open the repository source tree: %v", err)
	}
	t.Cleanup(func() { root.Close() })
	return root
}

// parseDir parses one source directory, read through the root rather than off
// the filesystem directly.
//
// parser.ParseDir would be the obvious call and is deprecated: it groups files
// into packages without consulting build tags, and it names paths to the os
// package itself. This rule is per-file and needs neither.
func parseDir(t *testing.T, root *Root, dir string, includeTests bool) []parsedFile {
	t.Helper()
	fset := token.NewFileSet()

	entries, err := root.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}

	var files []parsedFile
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		if !includeTests && strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(dir, name)
		src, readErr := root.ReadFile(path)
		if readErr != nil {
			t.Fatalf("read %s: %v", path, readErr)
		}
		file, parseErr := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
		if parseErr != nil {
			t.Fatalf("parse %s: %v", path, parseErr)
		}
		files = append(files, parsedFile{path: path, ast: file})
	}
	if len(files) == 0 {
		t.Fatalf("no Go files found in %s; the rule below would silently pass", dir)
	}
	return files
}

// forEachCall visits every call of the form pkg.Fn(...).
func forEachCall(file *ast.File, fn func(pkgName, name string, call *ast.CallExpr)) {
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		ident, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		fn(ident.Name, sel.Sel.Name, call)
		return true
	})
}

func importsPackage(file *ast.File, want string) bool {
	for _, imp := range file.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err == nil && path == want {
			return true
		}
	}
	return false
}
