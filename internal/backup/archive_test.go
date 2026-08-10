package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"strings"
	"testing"
)

func newTestRoot(t *testing.T, purpose string) *Root {
	t.Helper()
	root, err := Open(purpose, t.TempDir())
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	t.Cleanup(func() { root.Close() })
	return root
}

func seed(t *testing.T, root *Root, files map[string]string) {
	t.Helper()
	for name, content := range files {
		if i := strings.LastIndex(name, "/"); i > 0 {
			if err := root.MkdirAll(name[:i], 0o755); err != nil {
				t.Fatalf("mkdir for %s: %v", name, err)
			}
		}
		if err := root.WriteFile(name, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
}

func TestWriteAndExtractTreeRoundTrip(t *testing.T) {
	src := newTestRoot(t, "source")
	seed(t, src, map[string]string{
		"tree/a.txt":               "a",
		"tree/nested/b.txt":        "b",
		"tree/nested/deep/c.txt":   "c",
		"tree/nested/.hidden":      "a dotfile",
		"tree/empty-looking/x.txt": "",
	})
	if err := src.MkdirAll("tree/genuinely-empty", 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := src.Symlink("a.txt", "tree/link.txt"); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	var buf bytes.Buffer
	written, err := WriteTree(src, "tree", &buf)
	if err != nil {
		t.Fatalf("WriteTree: %v", err)
	}
	// Five files, four directories and one symlink: directories are carried so
	// that an empty one survives, which a naive file-only walk would drop.
	if written != 10 {
		t.Fatalf("wrote %d entries, want 10", written)
	}

	dst := newTestRoot(t, "destination")
	// Something that must not survive: a restore replaces, it does not merge.
	seed(t, dst, map[string]string{"tree/stale.txt": "left over from a newer tree"})

	read, err := ExtractTree(dst, "tree", bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("ExtractTree: %v", err)
	}
	if read != written {
		t.Fatalf("read %d entries, wrote %d", read, written)
	}
	if ok, _ := dst.Exists("tree/stale.txt"); ok {
		t.Error("the destination was merged into rather than replaced")
	}
	for name, want := range map[string]string{
		"tree/a.txt":             "a",
		"tree/nested/b.txt":      "b",
		"tree/nested/deep/c.txt": "c",
		"tree/nested/.hidden":    "a dotfile",
	} {
		got, err := dst.ReadFile(name)
		if err != nil || string(got) != want {
			t.Errorf("%s = %q, %v; want %q", name, got, err, want)
		}
	}
	if _, err := dst.Stat("tree/genuinely-empty"); err != nil {
		t.Errorf("an empty directory did not survive the round trip: %v", err)
	}
	if target, err := dst.Readlink("tree/link.txt"); err != nil || target != "a.txt" {
		t.Errorf("symlink = %q, %v", target, err)
	}
}

// TestExtractTreeRefusesEscapingEntries covers the archive equivalent of a path
// traversal: a member whose entry names point outside the destination. The
// entry name is attacker-controlled in a way the destination directory is not,
// so it is refused rather than trimmed into something plausible.
func TestExtractTreeRefusesEscapingEntries(t *testing.T) {
	for _, entry := range []string{
		"../escaped.txt",
		"../../escaped.txt",
		"/etc/passwd",
		"nested/../../escaped.txt",
	} {
		t.Run(entry, func(t *testing.T) {
			var buf bytes.Buffer
			tw := tar.NewWriter(&buf)
			body := []byte("planted")
			if err := tw.WriteHeader(&tar.Header{
				Typeflag: tar.TypeReg,
				Name:     entry,
				Mode:     0o644,
				Size:     int64(len(body)),
			}); err != nil {
				t.Fatalf("write header: %v", err)
			}
			if _, err := tw.Write(body); err != nil {
				t.Fatalf("write body: %v", err)
			}
			if err := tw.Close(); err != nil {
				t.Fatalf("close: %v", err)
			}

			dst := newTestRoot(t, "destination")
			_, err := ExtractTree(dst, "tree", bytes.NewReader(buf.Bytes()))
			if err == nil {
				t.Fatalf("an entry named %q was extracted", entry)
			}
			var escape *PathEscapeError
			if !errors.As(err, &escape) {
				t.Fatalf("want *PathEscapeError, got %T: %v", err, err)
			}
		})
	}
}

func TestExtractTreeRefusesAnEscapingSymlinkEntry(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeSymlink,
		Name:     "sneaky",
		Linkname: "../../../etc/passwd",
		Mode:     0o777,
	}); err != nil {
		t.Fatalf("write header: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	dst := newTestRoot(t, "destination")
	_, err := ExtractTree(dst, "tree", bytes.NewReader(buf.Bytes()))
	var escape *SymlinkEscapeError
	if !errors.As(err, &escape) {
		t.Fatalf("want *SymlinkEscapeError, got %T: %v", err, err)
	}
}

func TestExtractTreeRefusesAnEntryItCannotRestore(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeFifo, Name: "pipe", Mode: 0o644}); err != nil {
		t.Fatalf("write header: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	dst := newTestRoot(t, "destination")
	_, err := ExtractTree(dst, "tree", bytes.NewReader(buf.Bytes()))
	var unsupported *tarUnsupportedEntryError
	if !errors.As(err, &unsupported) {
		t.Fatalf("want *tarUnsupportedEntryError, got %T: %v", err, err)
	}
}

func TestWriteTreeRefusesADirectoryThatIsNotThere(t *testing.T) {
	src := newTestRoot(t, "source")
	if _, err := WriteTree(src, "absent", io.Discard); err == nil {
		t.Fatal("archiving a directory that does not exist should fail")
	}
}

func TestWriteMemberRecordsSizeAndDigest(t *testing.T) {
	dst := newTestRoot(t, "archive")
	if err := dst.MkdirAll("volumes", 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	payload := strings.Repeat("netmon", 1000)
	size, sum, err := writeMember(dst, "volumes/one.tar.gz", func(w io.Writer) error {
		tw := tar.NewWriter(w)
		if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: "f", Size: int64(len(payload)), Mode: 0o644}); err != nil {
			return err
		}
		if _, err := io.WriteString(tw, payload); err != nil {
			return err
		}
		return tw.Close()
	})
	if err != nil {
		t.Fatalf("writeMember: %v", err)
	}
	if size == 0 || sum == "" {
		t.Fatalf("writeMember reported size %d, digest %q", size, sum)
	}

	gotSum, gotSize, err := digestFile(dst, "volumes/one.tar.gz")
	if err != nil {
		t.Fatalf("digestFile: %v", err)
	}
	if gotSum != sum || gotSize != size {
		t.Fatalf("readback digest %s/%d, wrote %s/%d", gotSum, gotSize, sum, size)
	}
	entries, err := countMemberEntries(dst, "volumes/one.tar.gz")
	if err != nil || entries != 1 {
		t.Fatalf("countMemberEntries = %d, %v; want 1", entries, err)
	}
}

func TestWriteMemberRefusesAnEscapingMemberName(t *testing.T) {
	dst := newTestRoot(t, "archive")
	_, _, err := writeMember(dst, "../outside.tar.gz", func(io.Writer) error { return nil })
	var escape *PathEscapeError
	if !errors.As(err, &escape) {
		t.Fatalf("want *PathEscapeError, got %T: %v", err, err)
	}
}

func TestWriteMemberPropagatesABodyFailure(t *testing.T) {
	dst := newTestRoot(t, "archive")
	sentinel := errors.New("the runtime went away")
	_, _, err := writeMember(dst, "member.tar.gz", func(io.Writer) error { return sentinel })
	if !errors.Is(err, sentinel) {
		t.Fatalf("want the body's error back, got %v", err)
	}
}

func TestCountEntriesRejectsDamagedStreams(t *testing.T) {
	good := gzipTar(t, map[string]string{"a": "1", "b": "2"})

	if n, err := countEntries(bytes.NewReader(good)); err != nil || n != 2 {
		t.Fatalf("countEntries = %d, %v; want 2", n, err)
	}
	if _, err := countEntries(bytes.NewReader(good[:len(good)/2])); err == nil {
		t.Fatal("a truncated stream should not count clean")
	}
	if _, err := countEntries(strings.NewReader("not gzip at all")); err == nil {
		t.Fatal("a non-gzip stream should not count clean")
	}
}

func gzipTar(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, name := range sortedKeys(files) {
		body := files[name]
		if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: name, Size: int64(len(body)), Mode: 0o644}); err != nil {
			t.Fatalf("header: %v", err)
		}
		if _, err := io.WriteString(tw, body); err != nil {
			t.Fatalf("body: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	return buf.Bytes()
}

func TestRelativeTo(t *testing.T) {
	cases := []struct {
		base, rel, want string
		wantErr         bool
	}{
		{base: ".", rel: ".", want: ""},
		{base: ".", rel: "a/b", want: "a/b"},
		{base: "tree", rel: "tree", want: ""},
		{base: "tree", rel: "tree/a", want: "a"},
		{base: "tree", rel: "tree/a/b", want: "a/b"},
		{base: "tree", rel: "treehouse/a", wantErr: true},
		{base: "tree", rel: "other", wantErr: true},
	}
	for _, tc := range cases {
		got, err := relativeTo(tc.base, tc.rel)
		if tc.wantErr {
			if err == nil {
				t.Errorf("relativeTo(%q, %q) = %q, want an error", tc.base, tc.rel, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("relativeTo(%q, %q) = %q, %v; want %q", tc.base, tc.rel, got, err, tc.want)
		}
	}
}
