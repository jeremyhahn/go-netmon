package backup

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"hash"
	"io"
	"io/fs"
	"path"
)

// gzipLevel matches the shell tooling's "gzip -6": the default trade between
// archive size and the length of the outage a cold backup costs.
const gzipLevel = 6

// tarUnsupportedEntryError reports an archive entry this tool will not create or
// restore.
//
// Sockets, devices and hard links are refused rather than skipped. A restore
// that silently drops entries produces a tree that does not match the manifest's
// entry count, which is the one thing verification is supposed to catch.
type tarUnsupportedEntryError struct {
	Name string
	Mode fs.FileMode
}

func (e *tarUnsupportedEntryError) Error() string {
	return "backup: " + e.Name + " is a " + entryKind(e.Mode) +
		", which cannot be carried in an archive"
}

// entryKind names a file type in words.
//
// fs.FileMode.Type().String() renders the permission-string form -- a socket
// prints as "S---------" -- which tells an operator deciding whether a skipped
// entry matters almost nothing.
func entryKind(mode fs.FileMode) string {
	switch {
	case mode&fs.ModeSocket != 0:
		return "socket"
	case mode&fs.ModeNamedPipe != 0:
		return "named pipe"
	case mode&fs.ModeDevice != 0:
		if mode&fs.ModeCharDevice != 0 {
			return "character device"
		}
		return "block device"
	case mode&fs.ModeIrregular != 0:
		return "irregular file"
	default:
		return mode.Type().String()
	}
}

// digestWriter counts and hashes everything written through it.
type digestWriter struct {
	w       io.Writer
	hash    hash.Hash
	written int64
}

func newDigestWriter(w io.Writer) *digestWriter {
	return &digestWriter{w: w, hash: sha256.New()}
}

func (d *digestWriter) Write(p []byte) (int, error) {
	n, err := d.w.Write(p)
	d.written += int64(n)
	if n > 0 {
		d.hash.Write(p[:n])
	}
	return n, err
}

func (d *digestWriter) sum() string { return hex.EncodeToString(d.hash.Sum(nil)) }

// writeMember writes one compressed archive member into a root and reports its
// size and digest.
//
// The member is created through the root, so a member name that could name a
// location outside the backup directory fails here rather than writing there.
func writeMember(dst *Root, name string, body func(io.Writer) error) (int64, string, error) {
	f, err := dst.Create(name)
	if err != nil {
		return 0, "", err
	}
	// Durability is established below by checking gz.Close and f.Sync. This
	// deferred close is only the safety net for the error returns above them.
	defer func() { _ = f.Close() }()

	counted := newDigestWriter(f)
	gz, err := gzip.NewWriterLevel(counted, gzipLevel)
	if err != nil {
		return 0, "", &MemberError{Member: name, Check: "compression", Err: err}
	}
	if err := body(gz); err != nil {
		// The body already failed; the member is being abandoned, so a close
		// error here would replace the real cause with a symptom.
		_ = gz.Close()
		return 0, "", err
	}
	if err := gz.Close(); err != nil {
		return 0, "", &MemberError{Member: name, Check: "compression", Err: err}
	}
	if err := f.Sync(); err != nil {
		return 0, "", &MemberError{Member: name, Check: "durability", Err: err}
	}
	return counted.written, counted.sum(), nil
}

// digestFile reports the SHA-256 and size of a file inside a root.
func digestFile(r *Root, name string) (string, int64, error) {
	f, err := r.Open(name)
	if err != nil {
		return "", 0, err
	}
	// Read-only; a close error cannot change the bytes already hashed.
	defer func() { _ = f.Close() }()

	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, &MemberError{Member: name, Check: "read", Err: err}
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// countMemberEntries decompresses a member end to end and counts its entries.
//
// This is deliberately a second pass over the data: a member truncated at write
// time passes a "file exists" check and fails halfway through overwriting a live
// volume, which is the worst possible moment to find out.
func countMemberEntries(r *Root, name string) (int, error) {
	f, err := r.Open(name)
	if err != nil {
		return 0, err
	}
	// Read-only; a close error cannot change the entries already counted.
	defer func() { _ = f.Close() }()
	return countEntries(f)
}

// countEntries counts the entries in a gzipped tar stream.
func countEntries(r io.Reader) (int, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return 0, err
	}
	// A decompressor's close reports nothing a completed read has not.
	defer func() { _ = gz.Close() }()

	tr := tar.NewReader(gz)
	n := 0
	for {
		_, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return n, nil
		}
		if err != nil {
			return 0, err
		}
		n++
	}
}

// WriteTree streams a directory inside a root to w as an uncompressed tar and
// reports how many entries it wrote.
//
// Ownership is deliberately not carried. These are the operator's own files on
// the operator's own machine, and the uid that took the backup is not
// necessarily the uid that restores it; content, layout and mode are what
// matters. Docker volumes are the opposite case and are streamed by the runtime
// with numeric ownership intact.
func WriteTree(src *Root, base string, w io.Writer) (int, error) {
	tw := tar.NewWriter(w)
	entries := 0

	err := src.walk(base, func(rel string, info fs.FileInfo) error {
		name, err := relativeTo(base, rel)
		if err != nil {
			return err
		}
		if name == "" {
			return nil // the tree root itself
		}
		hdr, err := headerFor(src, rel, name, info)
		if err != nil {
			return err
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		entries++
		if !info.Mode().IsRegular() {
			return nil
		}
		return copyFileInto(tw, src, rel)
	})
	if err != nil {
		return 0, err
	}
	if err := tw.Close(); err != nil {
		return 0, err
	}
	return entries, nil
}

func headerFor(src *Root, rel, name string, info fs.FileInfo) (*tar.Header, error) {
	switch {
	case info.IsDir():
		return &tar.Header{
			Typeflag: tar.TypeDir,
			Name:     name + "/",
			Mode:     int64(info.Mode().Perm()),
			ModTime:  info.ModTime(),
		}, nil
	case info.Mode().IsRegular():
		return &tar.Header{
			Typeflag: tar.TypeReg,
			Name:     name,
			Mode:     int64(info.Mode().Perm()),
			Size:     info.Size(),
			ModTime:  info.ModTime(),
		}, nil
	case info.Mode()&fs.ModeSymlink != 0:
		target, err := src.Readlink(rel)
		if err != nil {
			return nil, err
		}
		return &tar.Header{
			Typeflag: tar.TypeSymlink,
			Name:     name,
			Linkname: target,
			Mode:     int64(info.Mode().Perm()),
			ModTime:  info.ModTime(),
		}, nil
	default:
		return nil, &tarUnsupportedEntryError{Name: name, Mode: info.Mode()}
	}
}

func copyFileInto(w io.Writer, src *Root, rel string) error {
	f, err := src.Open(rel)
	if err != nil {
		return err
	}
	// Read-only; the copy's own error is the one that matters.
	defer func() { _ = f.Close() }()
	_, err = io.Copy(w, f)
	return err
}

// ExtractTree replaces a directory inside a root with the contents of a tar
// stream and reports how many entries it read.
//
// Every entry lands through the root, so an archive carrying "../etc/passwd" or
// "/etc/passwd" as an entry name is refused instead of being sanitized into
// something plausible and written anyway.
func ExtractTree(dst *Root, base string, r io.Reader) (int, error) {
	if err := dst.MkdirAll(base, 0o755); err != nil {
		return 0, err
	}
	if err := dst.Empty(base); err != nil {
		return 0, err
	}

	tr := tar.NewReader(r)
	entries := 0
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return entries, nil
		}
		if err != nil {
			return 0, err
		}
		if err := extractEntry(dst, base, hdr, tr); err != nil {
			return 0, err
		}
		entries++
	}
}

func extractEntry(dst *Root, base string, hdr *tar.Header, r io.Reader) error {
	name, err := dst.join(base, path.Clean(hdr.Name))
	if err != nil {
		return err
	}
	mode := hdr.FileInfo().Mode().Perm()
	switch hdr.Typeflag {
	case tar.TypeDir:
		if err := dst.MkdirAll(name, 0o755); err != nil {
			return err
		}
		return dst.Chmod(name, mode)
	case tar.TypeReg:
		if err := dst.MkdirAll(path.Dir(name), 0o755); err != nil {
			return err
		}
		if err := writeEntryFile(dst, name, r, mode); err != nil {
			return err
		}
		return dst.Chtimes(name, hdr.ModTime, hdr.ModTime)
	case tar.TypeSymlink:
		if err := dst.MkdirAll(path.Dir(name), 0o755); err != nil {
			return err
		}
		return dst.Symlink(hdr.Linkname, name)
	default:
		return &tarUnsupportedEntryError{Name: hdr.Name, Mode: hdr.FileInfo().Mode()}
	}
}

func writeEntryFile(dst *Root, name string, r io.Reader, mode fs.FileMode) error {
	f, err := dst.Create(name)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		// The write already failed; the success path below checks Close, which
		// is where a truncated file would be caught.
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return dst.Chmod(name, mode)
}

// relativeTo expresses rel as a path below base, in slash form.
func relativeTo(base, rel string) (string, error) {
	base = path.Clean(base)
	rel = path.Clean(rel)
	if base == "." {
		if rel == "." {
			return "", nil
		}
		return rel, nil
	}
	if rel == base {
		return "", nil
	}
	if len(rel) > len(base)+1 && rel[:len(base)] == base && rel[len(base)] == '/' {
		return rel[len(base)+1:], nil
	}
	return "", &PathEscapeError{
		Purpose: "archive",
		Root:    base,
		Path:    rel,
		Reason:  "the path is not below the directory being archived",
	}
}
