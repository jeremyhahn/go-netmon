package api

import (
	"errors"
	"os"
	"path/filepath"
)

// uploadedMIBDirName is the directory under the MIB storage path that holds
// every module file this server was given.
const uploadedMIBDirName = "uploaded"

// mibSourceExtensions are the suffixes a module's file may carry when it was
// not uploaded under its bare module name.
var mibSourceExtensions = []string{".my", ".mib", ".txt"}

// uploadedMIBDir is where this server keeps the MIB files it was given.
func (s *Server) uploadedMIBDir() string {
	return filepath.Join(s.mibStoragePath, uploadedMIBDirName)
}

// mibSourceCandidates lists the plain file names a module's source may have in the upload directory, in try order.
func mibSourceCandidates(module, storedPath string) []string {
	names := make([]string, 0, 2+len(mibSourceExtensions))
	names = append(names, filepath.Base(storedPath), module)
	for _, ext := range mibSourceExtensions {
		names = append(names, module+ext)
	}

	candidates := make([]string, 0, len(names))
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		if name == "" || name == "." || name != filepath.Base(name) || !filepath.IsLocal(name) || seen[name] {
			continue
		}
		seen[name] = true
		candidates = append(candidates, name)
	}
	return candidates
}

// readMIBSource reads a module's source from dir through an os.Root, returning its file name and content.
func readMIBSource(dir, module, storedPath string) (string, []byte, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return "", nil, &MIBUploadError{Op: "read source", Path: dir, Err: err}
	}
	// Read-only handle; nothing to flush.
	defer func() { _ = root.Close() }()

	for _, name := range mibSourceCandidates(module, storedPath) {
		content, readErr := root.ReadFile(name)
		if readErr == nil {
			return name, content, nil
		}
		if !errors.Is(readErr, os.ErrNotExist) {
			return "", nil, &MIBUploadError{
				Op: "read source", Path: filepath.Join(dir, name), Err: readErr,
			}
		}
	}
	return "", nil, &MIBUploadError{
		Op: "read source", Path: filepath.Join(dir, module), Err: os.ErrNotExist,
	}
}

// mibSourcePath is the path a module's source is read from: the same file
// the configuration export carries, or "" when there is none. The catalog's
// stored path is informational only and is never shown in its place.
func (s *Server) mibSourcePath(module, storedPath string) string {
	dir := s.uploadedMIBDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		return ""
	}
	// Read-only handle; nothing to flush.
	defer func() { _ = root.Close() }()

	for _, name := range mibSourceCandidates(module, storedPath) {
		if info, statErr := root.Stat(name); statErr == nil && info.Mode().IsRegular() {
			return filepath.Join(dir, name)
		}
	}
	return ""
}
