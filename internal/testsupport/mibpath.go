// Package testsupport provides shared test helpers, including locating repository MIB fixtures.
package testsupport

import (
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

var (
	rootOnce sync.Once
	rootDir  string
	rootErr  error
)

// RepoRoot returns the absolute path to the repository root, located by walking
// up from this source file until go.mod is found.
func RepoRoot() (string, error) {
	rootOnce.Do(func() {
		_, thisFile, _, ok := runtime.Caller(0)
		if !ok {
			rootErr = &RepoRootError{Reason: "cannot determine source location"}
			return
		}

		dir := filepath.Dir(thisFile)
		for {
			if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
				rootDir = dir
				return
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				rootErr = &RepoRootError{Reason: "go.mod not found above " + filepath.Dir(thisFile)}
				return
			}
			dir = parent
		}
	})
	return rootDir, rootErr
}

// MIBDir returns the absolute path to a directory under mibs/; it panics if the repository root cannot be found.
func MIBDir(name string) string {
	root, err := RepoRoot()
	if err != nil {
		panic(err)
	}
	return filepath.Join(root, "mibs", name)
}

// MIBDirs returns absolute paths for several MIB directories at once, in the
// order given. Parsers take a variadic search path, so this keeps call sites
// to a single expression.
func MIBDirs(names ...string) []string {
	paths := make([]string, len(names))
	for i, name := range names {
		paths[i] = MIBDir(name)
	}
	return paths
}
