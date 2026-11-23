package backup

import (
	"io/fs"
	"sort"
	"strings"
)

// checksumsTemp is where the digest list is built before it is moved into place,
// so that the list is not itself a member of the tree it covers.
const checksumsTemp = ".checksums.sha256.partial"

// writeChecksums records the SHA-256 of every file in a backup.
//
// The list covers the manifest too, so tampering or bit rot anywhere in the
// archive is detectable with "sha256sum -c checksums.sha256" alone, without this
// tool. The format is sha256sum's own, and the paths keep its "./" prefix, so
// that stays true.
func writeChecksums(root *Root) error {
	paths, err := archiveFiles(root)
	if err != nil {
		return err
	}
	var b strings.Builder
	for _, p := range paths {
		sum, _, err := digestFile(root, p)
		if err != nil {
			return err
		}
		b.WriteString(sum)
		b.WriteString("  ./")
		b.WriteString(p)
		b.WriteString("\n")
	}
	if err := root.WriteFile(checksumsTemp, []byte(b.String()), 0o644); err != nil {
		return err
	}
	return root.Rename(checksumsTemp, ChecksumFile)
}

// verifyChecksums checks every digest in a backup's checksum list and refuses
// an archive holding a file the list does not cover.
//
// The extra-file check is not in sha256sum: a member added to an archive after
// the fact passes "sha256sum -c" untouched, and a restore must not read anything
// nobody vouched for.
func verifyChecksums(root *Root) error {
	data, err := root.ReadFile(ChecksumFile)
	if err != nil {
		if isNotExist(err) {
			return &ChecksumFileError{Reason: "is missing; refusing to restore an unverifiable archive"}
		}
		return err
	}
	listed, err := parseChecksums(string(data))
	if err != nil {
		return err
	}
	for _, path := range sortedKeys(listed) {
		sum, _, digestFileErr := digestFile(root, path)
		if digestFileErr != nil {
			return digestFileErr
		}
		if sum != listed[path] {
			return &MemberError{Member: path, Check: "SHA-256", Expected: listed[path], Actual: sum}
		}
	}
	present, err := archiveFiles(root)
	if err != nil {
		return err
	}
	for _, path := range present {
		if path == ChecksumFile {
			continue
		}
		if _, ok := listed[path]; !ok {
			return &UnlistedFileError{Path: path}
		}
	}
	return nil
}

// parseChecksums reads sha256sum output into a path-to-digest map.
func parseChecksums(text string) (map[string]string, error) {
	out := make(map[string]string)
	for i, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		sum, rest, ok := strings.Cut(line, "  ")
		if !ok {
			return nil, &ChecksumFileError{Reason: "is not in sha256sum format", Line: i + 1}
		}
		if len(sum) != 64 || !isHex(sum) {
			return nil, &ChecksumFileError{Reason: "does not start with a SHA-256 digest", Line: i + 1}
		}
		path := strings.TrimPrefix(strings.TrimPrefix(rest, "*"), "./")
		if path == "" {
			return nil, &ChecksumFileError{Reason: "names no file", Line: i + 1}
		}
		out[path] = sum
	}
	if len(out) == 0 {
		return nil, &ChecksumFileError{Reason: "lists no files"}
	}
	return out, nil
}

func isHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

// archiveFiles lists every regular file in a backup directory, in sorted order,
// excluding the partial checksum list.
func archiveFiles(root *Root) ([]string, error) {
	var paths []string
	err := root.walk(".", func(rel string, info fs.FileInfo) error {
		if !info.Mode().IsRegular() || rel == checksumsTemp {
			return nil
		}
		paths = append(paths, rel)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	return paths, nil
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
