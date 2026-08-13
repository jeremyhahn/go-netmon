package backup

import (
	"archive/tar"
	"errors"
	"io"
	"io/fs"
	"strings"
	"testing"
	"time"
)

// What happens when writing an archive out goes wrong halfway.
//
// A restore that fails is safe; a restore that fails and says nothing is not,
// because the next thing to run assumes the data is there. These cover the
// paths taken when the stream runs out, when the destination refuses a name,
// and when an entry is something a tar cannot faithfully carry -- each of which
// has to stop the extraction and say why.

// brokenReader fails part-way through, as a truncated archive would.
type brokenReader struct {
	before int
	read   int
}

var errStreamEnded = errors.New("unexpected EOF in archive member")

func (b *brokenReader) Read(p []byte) (int, error) {
	if b.read >= b.before {
		return 0, errStreamEnded
	}
	n := min(len(p), b.before-b.read)
	for i := range n {
		p[i] = 'x'
	}
	b.read += n
	return n, nil
}

func openTestRoot(t *testing.T) *Root {
	t.Helper()

	root, err := Open("test", t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return root
}

// TestATruncatedMemberStopsTheExtraction is the important one. A member whose
// stream ends early must fail rather than leave a short file that later reads
// as a complete one.
func TestATruncatedMemberStopsTheExtraction(t *testing.T) {
	t.Parallel()

	root := openTestRoot(t)
	err := writeEntryFile(root, "half-written.db", &brokenReader{before: 64}, 0o600)

	if err == nil {
		t.Fatal("a member whose stream ended early was extracted as if complete")
	}
	if !errors.Is(err, errStreamEnded) {
		t.Errorf("error was %v, want the read failure that caused it", err)
	}

	// Whatever landed on disk, the write was not reported as a success -- which
	// is the property that matters. A half file is evidence; a half file the
	// caller was told was complete is a corrupted restore.
	if _, existsErr := root.Exists("half-written.db"); existsErr != nil {
		t.Errorf("could not inspect the destination after a failed write: %v", existsErr)
	}
}

// TestAnEntryNamedOutsideTheRootIsRefused covers the containment boundary from
// the extraction side.
func TestAnEntryNamedOutsideTheRootIsRefused(t *testing.T) {
	t.Parallel()

	root := openTestRoot(t)

	if err := writeEntryFile(root, "../escaped.db", strings.NewReader("x"), 0o600); err == nil {
		t.Error("a member named outside the root was written")
	}

	if err := root.MkdirAll("restore", 0o755); err != nil {
		t.Fatalf("prepare base: %v", err)
	}
	hdr := &tar.Header{Name: "../../etc/passwd", Typeflag: tar.TypeReg, Mode: 0o600, Size: 1}
	if err := extractEntry(root, "restore", hdr, strings.NewReader("x")); err == nil {
		t.Error("extractEntry accepted a member that traverses out of the root")
	}
}

// TestExtractHandlesEachEntryKind covers the shapes an archive can carry and
// the one it cannot.
func TestExtractHandlesEachEntryKind(t *testing.T) {
	t.Parallel()

	root := openTestRoot(t)
	const base = "restore"
	if err := root.MkdirAll(base, 0o755); err != nil {
		t.Fatalf("prepare base: %v", err)
	}
	now := time.Now().Truncate(time.Second)

	dir := &tar.Header{Name: "data", Typeflag: tar.TypeDir, Mode: 0o755, ModTime: now}
	if err := extractEntry(root, base, dir, nil); err != nil {
		t.Fatalf("directory entry: %v", err)
	}

	file := &tar.Header{Name: "data/one.txt", Typeflag: tar.TypeReg, Mode: 0o640, Size: 5, ModTime: now}
	if err := extractEntry(root, base, file, strings.NewReader("hello")); err != nil {
		t.Fatalf("regular entry: %v", err)
	}
	content, err := root.ReadFile(base + "/data/one.txt")
	if err != nil || string(content) != "hello" {
		t.Errorf("extracted file = %q, %v; want its contents", content, err)
	}

	link := &tar.Header{Name: "data/link", Typeflag: tar.TypeSymlink, Linkname: "one.txt", ModTime: now}
	if linkErr := extractEntry(root, base, link, nil); linkErr != nil {
		t.Fatalf("symlink entry: %v", linkErr)
	}

	// A socket or device node cannot be carried, and the refusal names the kind
	// so an operator can judge whether it mattered.
	odd := &tar.Header{Name: "data/sock", Typeflag: tar.TypeFifo, Mode: int64(fs.ModeNamedPipe | 0o600)}
	err = extractEntry(root, base, odd, nil)
	if err == nil {
		t.Fatal("an entry a tar cannot carry was extracted anyway")
	}
	var unsupported *tarUnsupportedEntryError
	if !errors.As(err, &unsupported) {
		t.Fatalf("error was %T, want *tarUnsupportedEntryError", err)
	}
	if !strings.Contains(err.Error(), "data/sock") {
		t.Errorf("message %q does not name the entry", err.Error())
	}
}

// TestWriteEntryFileAppliesItsMode checks the permission the archive recorded
// actually lands: a restored file that is world-readable when it was not, or
// unreadable when it was, is a different file.
func TestWriteEntryFileAppliesItsMode(t *testing.T) {
	t.Parallel()

	root := openTestRoot(t)
	if err := writeEntryFile(root, "secret.yml", strings.NewReader("community: public"), 0o600); err != nil {
		t.Fatalf("writeEntryFile: %v", err)
	}

	info, err := root.Stat("secret.yml")
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %v, want 0600: config carrying credentials must not widen on restore", perm)
	}
}

// TestHeaderForDescribesWhatItIsGiven covers the writing side's shapes.
func TestHeaderForDescribesWhatItIsGiven(t *testing.T) {
	t.Parallel()

	src := openTestRoot(t)
	if err := src.WriteFile("plain.txt", []byte("body"), 0o640); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := src.Symlink("plain.txt", "link"); err != nil {
		t.Fatalf("seed link: %v", err)
	}

	plain, err := src.Lstat("plain.txt")
	if err != nil {
		t.Fatalf("lstat: %v", err)
	}
	hdr, err := headerFor(src, "plain.txt", "plain.txt", plain)
	if err != nil {
		t.Fatalf("headerFor: %v", err)
	}
	if hdr.Typeflag != tar.TypeReg || hdr.Size != 4 {
		t.Errorf("header = %+v, want a 4-byte regular file", hdr)
	}

	linkInfo, err := src.Lstat("link")
	if err != nil {
		t.Fatalf("lstat link: %v", err)
	}
	linkHdr, err := headerFor(src, "link", "link", linkInfo)
	if err != nil {
		t.Fatalf("headerFor link: %v", err)
	}
	if linkHdr.Typeflag != tar.TypeSymlink || linkHdr.Linkname != "plain.txt" {
		t.Errorf("link header = %+v, want a symlink to plain.txt", linkHdr)
	}

	// A socket is refused rather than written as something it is not.
	if _, err := headerFor(src, "sock", "sock", fakeInfo{mode: fs.ModeSocket}); err == nil {
		t.Error("a socket was given a tar header")
	}
}

// fakeInfo is an fs.FileInfo for a kind that cannot easily be created portably.
type fakeInfo struct{ mode fs.FileMode }

func (f fakeInfo) Name() string       { return "sock" }
func (f fakeInfo) Size() int64        { return 0 }
func (f fakeInfo) Mode() fs.FileMode  { return f.mode }
func (f fakeInfo) ModTime() time.Time { return time.Time{} }
func (f fakeInfo) IsDir() bool        { return false }
func (f fakeInfo) Sys() any           { return nil }

var _ io.Reader = (*brokenReader)(nil)

// TestACorruptArchiveIsRefusedNotPartiallyApplied is the failure a restore is
// most likely to meet in the field: a member that was truncated in transit or
// on disk.
//
// The extraction must stop and report. What it must never do is return the
// entries it managed before the stream broke as though the restore succeeded,
// because the caller then starts the stack on half its data.
func TestACorruptArchiveIsRefusedNotPartiallyApplied(t *testing.T) {
	t.Parallel()

	// A well-formed archive, then cut short.
	src := openTestRoot(t)
	if err := src.MkdirAll("payload", 0o755); err != nil {
		t.Fatalf("seed: %v", err)
	}
	for _, name := range []string{"one.txt", "two.txt", "three.txt"} {
		if err := src.WriteFile("payload/"+name, []byte(strings.Repeat("data", 512)), 0o640); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}

	var whole strings.Builder
	if _, err := WriteTree(src, "payload", &whole); err != nil {
		t.Fatalf("WriteTree: %v", err)
	}
	full := whole.String()
	if len(full) < 2048 {
		t.Fatalf("archive is %d bytes, too small to truncate meaningfully", len(full))
	}
	truncated := full[:len(full)/2]

	dst := openTestRoot(t)
	entries, err := ExtractTree(dst, "restored", strings.NewReader(truncated))
	if err == nil {
		t.Fatalf("a truncated archive extracted cleanly, reporting %d entries", entries)
	}

	// And the intact one round-trips, so the refusal above is about the damage
	// and not about the format.
	fresh := openTestRoot(t)
	if _, freshErr := ExtractTree(fresh, "restored", strings.NewReader(full)); freshErr != nil {
		t.Fatalf("the intact archive was refused too: %v", freshErr)
	}
	content, err := fresh.ReadFile("restored/one.txt")
	if err != nil || len(content) != 2048 {
		t.Errorf("restored file = %d bytes, %v; want the 2048 written", len(content), err)
	}
}

// TestExtractingIntoAnOccupiedBaseReplacesIt covers the emptying step. A
// restore replaces contents; a merge would leave files the backup does not
// contain, which is the "silently half-matches" outcome this must not produce.
func TestExtractingIntoAnOccupiedBaseReplacesIt(t *testing.T) {
	t.Parallel()

	src := openTestRoot(t)
	if err := src.MkdirAll("payload", 0o755); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := src.WriteFile("payload/kept.txt", []byte("new"), 0o640); err != nil {
		t.Fatalf("seed: %v", err)
	}

	var archive strings.Builder
	if _, err := WriteTree(src, "payload", &archive); err != nil {
		t.Fatalf("WriteTree: %v", err)
	}

	dst := openTestRoot(t)
	if err := dst.MkdirAll("restored", 0o755); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if err := dst.WriteFile("restored/stale.txt", []byte("old"), 0o640); err != nil {
		t.Fatalf("prepare: %v", err)
	}

	if _, err := ExtractTree(dst, "restored", strings.NewReader(archive.String())); err != nil {
		t.Fatalf("ExtractTree: %v", err)
	}

	if exists, _ := dst.Exists("restored/stale.txt"); exists {
		t.Error("a file the archive does not contain survived the restore; " +
			"a restore replaces contents rather than merging into them")
	}
	if exists, _ := dst.Exists("restored/kept.txt"); !exists {
		t.Error("the archive's own file is missing after the restore")
	}
}
