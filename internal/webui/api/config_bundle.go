package api

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"path"
	"sort"
	"strconv"
	"time"

	"github.com/jeremyhahn/go-netmon/internal/webui/storage"
)

// The bundle's fixed layout.
//
// One JSON document plus one directory of MIB source files, and nothing else.
// The document stays readable and diffable on its own — that is what an operator
// reviews and what a change request carries — while the directory beside it is
// what makes the archive self-sufficient, so restoring onto an empty instance is
// not a scavenger hunt for MIB files.
//
// The source files travel, not the parsed catalog. The catalog is derived: it is
// whatever this build's parser makes of these files, so carrying it would pin a
// restore to the parser that ran at export time and quietly reinstate every
// parsing bug fixed since. Re-parsing on import costs seconds and produces the
// catalog the target build would have produced anyway.
const (
	bundleConfigEntry = "config.json"
	bundleMIBDir      = "mibs"
)

// Bundle size limits. They exist so that a malformed or hostile archive is
// refused rather than decompressed into memory: a few megabytes of MIB text
// compresses to almost nothing, so the compressed size on its own says nothing
// about what unpacking it would cost.
const (
	maxBundleUploadSize = 64 << 20  // compressed bytes accepted from a client
	maxBundleBytes      = 256 << 20 // total decompressed bytes
	maxBundleEntries    = 8192      // files in one archive
	maxBundleFileBytes  = maxUploadSize
)

// ConfigBundle is a configuration document together with the MIB source files it
// depends on.
//
// MIBFiles is keyed by the document's ConfigMIB.File path. It is empty for a
// bare document, which is a valid thing to import as long as the target already
// holds every module the configuration references.
type ConfigBundle struct {
	Config   *storage.Config
	MIBFiles map[string][]byte
}

// BundleFormatError reports an upload that is not a configuration bundle at all.
type BundleFormatError struct {
	Reason string
}

func (e *BundleFormatError) Error() string {
	return "config bundle: " + e.Reason
}

// BundleEntryError reports one member of a bundle that could not be accepted.
type BundleEntryError struct {
	Entry  string
	Reason string
}

func (e *BundleEntryError) Error() string {
	return "config bundle: entry " + strconv.Quote(e.Entry) + ": " + e.Reason
}

// BundleChecksumError reports a MIB source file whose contents do not match what
// the document says they should be.
//
// It is fatal rather than a warning: importing a module from a file that is not
// the file the configuration was exported against would give the target a
// different set of objects under the same module name, which is exactly the
// silent under-collection this feature exists to prevent.
type BundleChecksumError struct {
	Module string
	Entry  string
	Want   string
	Got    string
}

func (e *BundleChecksumError) Error() string {
	return "config bundle: module " + strconv.Quote(e.Module) + " source " +
		strconv.Quote(e.Entry) + " does not match its recorded checksum (want " +
		e.Want + ", got " + e.Got + "); the bundle is damaged or has been modified"
}

// marshalConfig renders a configuration document as the bundle stores it:
// indented, with a trailing newline, so it reads and diffs as a text file.
func marshalConfig(doc *storage.Config) ([]byte, error) {
	encoded, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, &BundleFormatError{Reason: "encoding the configuration: " + err.Error()}
	}
	return append(encoded, '\n'), nil
}

// writeConfigBundle writes a gzipped tar archive of the bundle.
//
// Entries are written in a fixed order — the document first, then the MIB
// sources by name — so exporting the same configuration twice produces the same
// archive layout and a byte comparison means something.
func writeConfigBundle(w io.Writer, bundle *ConfigBundle) error {
	document, err := marshalConfig(bundle.Config)
	if err != nil {
		return err
	}

	gz := gzip.NewWriter(w)
	archive := tar.NewWriter(gz)

	modified := bundle.Config.ExportedAt
	if modified.IsZero() {
		modified = time.Now().UTC()
	}

	if err := writeBundleEntry(archive, bundleConfigEntry, document, modified); err != nil {
		return err
	}
	for _, name := range sortedKeys(bundle.MIBFiles) {
		if err := writeBundleEntry(archive, name, bundle.MIBFiles[name], modified); err != nil {
			return err
		}
	}

	if err := archive.Close(); err != nil {
		return &BundleFormatError{Reason: "finishing the archive: " + err.Error()}
	}
	if err := gz.Close(); err != nil {
		return &BundleFormatError{Reason: "compressing the archive: " + err.Error()}
	}
	return nil
}

// writeBundleEntry writes one archive member.
func writeBundleEntry(archive *tar.Writer, name string, content []byte, modified time.Time) error {
	header := &tar.Header{
		Name:     name,
		Mode:     0o644,
		Size:     int64(len(content)),
		ModTime:  modified,
		Typeflag: tar.TypeReg,
	}
	if err := archive.WriteHeader(header); err != nil {
		return &BundleEntryError{Entry: name, Reason: "writing header: " + err.Error()}
	}
	if _, err := archive.Write(content); err != nil {
		return &BundleEntryError{Entry: name, Reason: "writing content: " + err.Error()}
	}
	return nil
}

// parseConfigUpload reads whatever a client sent as a configuration to import.
//
// The bytes decide, not the Content-Type: a gzip archive is a bundle and
// anything else is expected to be a bare document. A client that mislabels its
// upload is a client whose import still works, and one that sends something
// else entirely gets told what it sent was not a configuration.
func parseConfigUpload(data []byte) (*ConfigBundle, error) {
	if len(data) == 0 {
		return nil, &BundleFormatError{Reason: "the upload is empty"}
	}
	if isGzip(data) {
		return readConfigBundle(data)
	}

	var doc storage.Config
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, &BundleFormatError{
			Reason: "the upload is neither a gzipped bundle nor a JSON configuration document: " +
				err.Error(),
		}
	}
	return &ConfigBundle{Config: &doc}, nil
}

// isGzip reports whether the bytes start with the gzip magic number.
func isGzip(data []byte) bool {
	return len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b
}

// readConfigBundle unpacks a gzipped tar bundle.
func readConfigBundle(data []byte) (*ConfigBundle, error) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, &BundleFormatError{Reason: "not a readable gzip archive: " + err.Error()}
	}
	// The archive has been fully read by the time this runs; a close error
	// cannot change what was decoded.
	defer func() { _ = gz.Close() }()

	reader := &bundleReader{
		archive: tar.NewReader(io.LimitReader(gz, maxBundleBytes+1)),
		files:   make(map[string][]byte),
	}
	if err := reader.readAll(); err != nil {
		return nil, err
	}
	return reader.bundle()
}

// bundleReader accumulates an archive's members under the size limits.
type bundleReader struct {
	archive  *tar.Reader
	document []byte
	files    map[string][]byte
	total    int64
	entries  int
}

// readAll consumes every member of the archive.
func (r *bundleReader) readAll() error {
	for {
		header, err := r.archive.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return &BundleFormatError{Reason: "reading the archive: " + err.Error()}
		}
		if err := r.readEntry(header); err != nil {
			return err
		}
	}
}

// readEntry accepts one member, refusing anything the layout does not allow.
func (r *bundleReader) readEntry(header *tar.Header) error {
	name := path.Clean(header.Name)
	if header.Typeflag == tar.TypeDir {
		return nil
	}
	if header.Typeflag != tar.TypeReg {
		return &BundleEntryError{Entry: header.Name, Reason: "is not a regular file"}
	}
	if !isSafeBundlePath(name) {
		return &BundleEntryError{Entry: header.Name, Reason: "is not a path this bundle may contain"}
	}

	r.entries++
	if r.entries > maxBundleEntries {
		return &BundleFormatError{
			Reason: "holds more than " + strconv.Itoa(maxBundleEntries) + " files",
		}
	}
	if header.Size > maxBundleFileBytes {
		return &BundleEntryError{Entry: name, Reason: "is larger than " +
			strconv.Itoa(maxBundleFileBytes/(1<<20)) + " MB"}
	}

	content, err := io.ReadAll(io.LimitReader(r.archive, maxBundleFileBytes+1))
	if err != nil {
		return &BundleEntryError{Entry: name, Reason: "could not be read: " + err.Error()}
	}
	if int64(len(content)) > maxBundleFileBytes {
		return &BundleEntryError{Entry: name, Reason: "is larger than " +
			strconv.Itoa(maxBundleFileBytes/(1<<20)) + " MB"}
	}

	r.total += int64(len(content))
	if r.total > maxBundleBytes {
		return &BundleFormatError{
			Reason: "unpacks to more than " + strconv.Itoa(maxBundleBytes/(1<<20)) + " MB",
		}
	}

	if name == bundleConfigEntry {
		r.document = content
		return nil
	}
	r.files[name] = content
	return nil
}

// bundle decodes the accumulated members into a configuration bundle.
func (r *bundleReader) bundle() (*ConfigBundle, error) {
	if r.document == nil {
		return nil, &BundleFormatError{
			Reason: "does not contain " + bundleConfigEntry + ", so it is not a configuration export",
		}
	}

	var doc storage.Config
	if err := json.Unmarshal(r.document, &doc); err != nil {
		return nil, &BundleEntryError{
			Entry:  bundleConfigEntry,
			Reason: "is not a JSON configuration document: " + err.Error(),
		}
	}
	return &ConfigBundle{Config: &doc, MIBFiles: r.files}, nil
}

// isSafeBundlePath reports whether a member name is one the layout permits.
//
// Only the document and files directly under mibs/ are accepted, which rules out
// absolute paths and parent-directory escapes without having to reason about
// what path.Clean left behind.
func isSafeBundlePath(name string) bool {
	if name == bundleConfigEntry {
		return true
	}
	dir, file := path.Split(name)
	return dir == bundleMIBDir+"/" && file != "" && file != "." && file != ".."
}

// verifyBundleSources checks that every module the document says the bundle
// carries is present and unmodified, and returns the sources by module name.
//
// A document entry with no File is a module the target is expected to hold
// already; it is not an error here, and the importer decides whether the target
// really does hold it.
func verifyBundleSources(bundle *ConfigBundle) (map[string]bundleSource, error) {
	sources := make(map[string]bundleSource, len(bundle.Config.MIBs))
	for _, m := range bundle.Config.MIBs {
		if m.File == "" {
			continue
		}

		content, carried := bundle.MIBFiles[m.File]
		if !carried {
			return nil, &BundleEntryError{
				Entry: m.File, Reason: "is named by module " + strconv.Quote(m.Name) +
					" but is not in the bundle",
			}
		}
		if got := checksum(content); m.SHA256 != "" && got != m.SHA256 {
			return nil, &BundleChecksumError{
				Module: m.Name, Entry: m.File, Want: m.SHA256, Got: got,
			}
		}

		sources[m.Name] = bundleSource{
			module:   m.Name,
			fileName: path.Base(m.File),
			content:  content,
			tags:     m.Tags,
		}
	}
	return sources, nil
}

// bundleSource is one MIB module's source file as it arrived in a bundle.
type bundleSource struct {
	module   string
	fileName string
	content  []byte
	tags     []string
}

// checksum renders the SHA-256 of a file the way the document records it.
func checksum(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// sortedKeys returns a map's keys in order, so archives are reproducible.
func sortedKeys(files map[string][]byte) []string {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
