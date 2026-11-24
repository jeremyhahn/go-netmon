package backup

import "strconv"

// Archive is a backup directory whose integrity has been proved; restores accept only an Archive. The caller closes it.
type Archive struct {
	// Dir is the absolute backup directory.
	Dir string
	// Manifest describes what the directory holds.
	Manifest *Manifest

	root *Root
}

// Close releases the archive's root.
func (a *Archive) Close() error { return a.root.Close() }

// Verify checks the checksum list (complete, manifest included), each member's size and digest, and decompresses each member before returning an Archive.
func (s *Service) Verify(dir string) (*Archive, error) {
	resolved, err := ResolveDir("backup archive", dir)
	if err != nil {
		return nil, err
	}
	root, err := Open("backup archive", resolved)
	if err != nil {
		return nil, err
	}
	archive, err := s.verifyInto(root, resolved)
	if err != nil {
		// Verification already failed; the close error would replace the real
		// cause with a symptom.
		_ = root.Close()
		return nil, err
	}
	return archive, nil
}

func (s *Service) verifyInto(root *Root, dir string) (*Archive, error) {
	s.info("verifying backup: %s", dir)
	s.announce(root)

	data, err := root.ReadFile(ManifestFile)
	if err != nil {
		if isNotExist(err) {
			return nil, &ManifestError{Path: dir, Reason: "not a backup directory (no manifest.json)"}
		}
		return nil, err
	}
	manifest, err := decodeManifest(data)
	if err != nil {
		return nil, err
	}

	s.info("  checking file checksums")
	if err := verifyChecksums(root); err != nil {
		return nil, err
	}

	members := manifest.Members()
	s.info("  checking %d member(s)", len(members))
	for _, member := range members {
		if err := verifyMember(root, member); err != nil {
			return nil, err
		}
		s.info("    ok  %s  (%d entries, %s)", member.Name, member.Entries, humanBytes(member.Bytes))
	}

	s.info("backup verified")
	return &Archive{Dir: dir, Manifest: manifest, root: root}, nil
}

// verifyMember checks one archive member's presence, size, digest and shape.
func verifyMember(root *Root, member Member) error {
	sum, size, err := digestFile(root, member.Name)
	if err != nil {
		if isNotExist(err) {
			return &MemberError{Member: member.Name, Check: "presence", Err: err}
		}
		return err
	}
	if size != member.Bytes {
		return &MemberError{
			Member: member.Name, Check: "size",
			Expected: strconv.FormatInt(member.Bytes, 10),
			Actual:   strconv.FormatInt(size, 10),
		}
	}
	if sum != member.SHA256 {
		return &MemberError{Member: member.Name, Check: "SHA-256", Expected: member.SHA256, Actual: sum}
	}
	entries, err := countMemberEntries(root, member.Name)
	if err != nil {
		return &MemberError{Member: member.Name, Check: "decompression", Err: err}
	}
	if entries != member.Entries {
		return &MemberError{
			Member: member.Name, Check: "entry count",
			Expected: itoa(member.Entries), Actual: itoa(entries),
		}
	}
	return nil
}
