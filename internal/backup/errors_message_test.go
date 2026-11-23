package backup

import (
	"errors"
	"io/fs"
	"strings"
	"testing"
)

// What an operator is told when a backup or a restore refuses.
//
// These messages are the whole interface at the moment something goes wrong.
// A restore that stops is doing its job; a restore that stops saying "operation
// failed" has made the situation worse, because the next move -- retry, fix a
// label, pick another archive, or stop and think -- depends entirely on which
// refusal this was.
//
// Every message is checked for the two things that make it actionable: the
// subject it is about (a volume, a path, a component, a file) and the reason.
// A wrapped cause has to survive too, or errors.As stops working for the code
// that branches on these.

func TestErrorMessagesNameTheirSubjectAndReason(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		// want are fragments the message must carry: what it is about, and why.
		want []string
	}{
		{
			"a path that leaves its root",
			&PathEscapeError{
				Purpose: "archive", Root: "/srv/backups", Path: "../../etc/passwd",
				Reason: "it resolves outside the root",
			},
			[]string{"../../etc/passwd", "archive", "/srv/backups", "resolves outside"},
		},
		{
			"a symlink pointing out of its root",
			&SymlinkEscapeError{
				Purpose: "restore", Root: "/srv/mibs", Name: "planted.my",
				Target: "/etc/shadow", Reason: "the target is outside the root",
			},
			[]string{"planted.my", "/etc/shadow", "restore", "/srv/mibs"},
		},
		{
			"a root that cannot be anchored",
			&RootOpenError{
				Purpose: "MIB tree", Dir: "/srv/mibs", Reason: "it is not a directory",
				Err: errors.New("not a directory"),
			},
			[]string{"MIB tree", "/srv/mibs", "not a directory"},
		},
		{
			"an operation inside a root",
			&RootOpError{Op: "create", Purpose: "archive", Root: "/srv/backups", Path: "manifest.json",
				Err: errors.New("permission denied")},
			[]string{"create", "manifest.json", "permission denied"},
		},
		{
			"a component nobody defines",
			&UnknownComponentError{Name: "metrix"},
			[]string{"metrix"},
		},
		{
			"only and skip given together",
			// Constructed as component.go constructs it: the reason is the
			// message, so an instance without one says nothing at all.
			&SelectionConflictError{Reason: "--only and --skip are mutually exclusive"},
			[]string{"--only", "--skip", "mutually exclusive"},
		},
		{
			"a selection that selects nothing",
			&EmptySelectionError{},
			[]string{"no components"},
		},
		{
			"a component the archive does not carry",
			&ComponentAbsentError{Name: ComponentMIBs, Available: []Component{ComponentMetrics}},
			[]string{"mibs", "metrics"},
		},
		{
			"a compose file that cannot be used",
			&ComposeFileError{Path: "/srv/docker-compose.yml", Reason: "no compose file was given"},
			[]string{"/srv/docker-compose.yml", "no compose file"},
		},
		{
			"a volume belonging to another project",
			&VolumeOwnershipError{Volume: "go-netmon_webui-data", Project: "go-netmon", Labeled: "someone-else"},
			[]string{"go-netmon_webui-data", "someone-else", "go-netmon", "refusing"},
		},
		{
			"a volume with no project label at all",
			&VolumeOwnershipError{Volume: "stray", Project: "go-netmon"},
			// An unlabeled volume must not read as one labeled with the empty
			// string; the message says <none> so the two are distinguishable.
			[]string{"stray", "<none>", "go-netmon"},
		},
		{
			"a volume the runtime does not know",
			&VolumeMissingError{Volume: "go-netmon_webui-data"},
			[]string{"go-netmon_webui-data", "not found"},
		},
		{
			"a volume the compose file does not define",
			&VolumeUndefinedError{Component: ComponentMetrics, Volume: "prometheus-data", File: "/srv/docker-compose.yml"},
			[]string{"prometheus-data", "/srv/docker-compose.yml"},
		},
		{
			"a name that is not a name",
			&InvalidNameError{Kind: "volume", Name: "../../etc"},
			[]string{"volume", "../../etc"},
		},
		{
			"a MIB tree that cannot be located",
			&TreeResolveError{Target: "/app/mibs", Sources: []string{"webui", "poller"}},
			[]string{"/app/mibs"},
		},
		{
			"a MIB tree resolving somewhere dangerous",
			&UnsafeTreeError{Dir: "/", Reason: "it is a system directory"},
			[]string{"/", "system directory"},
		},
		{
			"a MIB tree with nothing in it",
			&EmptyTreeError{Dir: "/srv/mibs"},
			[]string{"/srv/mibs"},
		},
		{
			"a manifest that cannot be read",
			&ManifestError{Path: "manifest.json", Reason: "is not valid JSON",
				Err: errors.New("unexpected end of input")},
			[]string{"manifest.json", "not valid JSON"},
		},
		{
			"an archive from a future schema",
			&SchemaError{Found: 3, Supported: 1},
			[]string{"3", "1"},
		},
		{
			"a file the checksums do not list",
			&UnlistedFileError{Path: "sneaked-in.tar"},
			[]string{"sneaked-in.tar"},
		},
		{
			"an archive that is already there",
			&ExistsError{Dir: "backups/2026-08-12"},
			[]string{"backups/2026-08-12"},
		},
		{
			"an archive from a different project",
			&ProjectMismatchError{Backup: "other-stack", Target: "go-netmon"},
			[]string{"other-stack", "go-netmon"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			message := tt.err.Error()

			if !strings.HasPrefix(message, "backup: ") {
				t.Errorf("message %q does not say which tool is refusing", message)
			}
			for _, fragment := range tt.want {
				if !strings.Contains(message, fragment) {
					t.Errorf("message %q does not mention %q", message, fragment)
				}
			}
			// A message that trails off, or that is only its prefix, tells an
			// operator nothing.
			if len(strings.TrimSpace(strings.TrimPrefix(message, "backup:"))) < 10 {
				t.Errorf("message %q is too short to act on", message)
			}
		})
	}
}

// TestWrappedCausesSurvive keeps errors.As working for the callers that branch
// on these. An error that swallows its cause forces the caller to match on
// message text, which is how a refusal starts being handled by accident.
func TestWrappedCausesSurvive(t *testing.T) {
	t.Parallel()

	cause := errors.New("permission denied")

	tests := []struct {
		name string
		err  error
	}{
		{"RootOpError", &RootOpError{Op: "create", Purpose: "archive", Root: "/srv", Path: "x", Err: cause}},
		{"ManifestError", &ManifestError{Path: "manifest.json", Reason: "unreadable", Err: cause}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !errors.Is(tt.err, cause) {
				t.Errorf("%s does not unwrap to the cause it was given", tt.name)
			}
		})
	}
}

// TestATreeOverwriteExplainsTheGate covers the one refusal an operator is
// expected to override. It has to say what would be replaced and how to say
// yes, or the only way past it is to read the source.
func TestATreeOverwriteExplainsTheGate(t *testing.T) {
	t.Parallel()

	err := &TreeOverwriteError{Dir: "/srv/mibs", Existing: 216, Incoming: 218}
	message := err.Error()

	for _, fragment := range []string{"/srv/mibs", "216"} {
		if !strings.Contains(message, fragment) {
			t.Errorf("message %q does not mention %q, so it does not say what is at stake", message, fragment)
		}
	}
}

// TestVerificationFailuresSayWhatDidNotMatch covers the messages a corrupted or
// tampered archive produces. "verification failed" is not actionable; which
// member, which check, and what was expected against what was found is.
func TestVerificationFailuresSayWhatDidNotMatch(t *testing.T) {
	t.Parallel()

	mismatch := (&MemberError{
		Member: "metrics.tar", Check: "sha256",
		Expected: "abc123", Actual: "def456",
	}).Error()
	for _, fragment := range []string{"metrics.tar", "sha256", "abc123", "def456"} {
		if !strings.Contains(mismatch, fragment) {
			t.Errorf("message %q does not mention %q", mismatch, fragment)
		}
	}

	// A member that could not be read at all has no digests to compare, and the
	// message must not invent an empty pair for them.
	unreadable := (&MemberError{
		Member: "metrics.tar", Check: "read", Err: errors.New("unexpected EOF"),
	}).Error()
	if strings.Contains(unreadable, "(expected ") {
		t.Errorf("message %q reports a comparison that was never made", unreadable)
	}
	if !strings.Contains(unreadable, "unexpected EOF") {
		t.Errorf("message %q loses the reason the member could not be read", unreadable)
	}
	if !errors.Is(&MemberError{Err: errUnreadable}, errUnreadable) {
		t.Error("MemberError does not unwrap to its cause")
	}

	// The checksums file itself: a line number is what makes a malformed one
	// findable.
	line := (&ChecksumFileError{Reason: "expected 'digest  name'", Line: 7}).Error()
	for _, fragment := range []string{"7", "expected"} {
		if !strings.Contains(line, fragment) {
			t.Errorf("message %q does not mention %q", line, fragment)
		}
	}
	// A fault in no particular line must not claim to be on line zero.
	whole := (&ChecksumFileError{Reason: "the file is empty"}).Error()
	if strings.Contains(whole, "line 0") {
		t.Errorf("message %q points at a line that does not exist", whole)
	}
}

var errUnreadable = errors.New("unexpected EOF")

// TestAnUnarchivableEntryNamesItsKind covers the refusal for things a tar
// cannot faithfully carry -- a socket or a device node in a volume. Naming the
// kind is what tells an operator whether it matters.
func TestAnUnarchivableEntryNamesItsKind(t *testing.T) {
	t.Parallel()

	message := (&tarUnsupportedEntryError{Name: "run/docker.sock", Mode: fs.ModeSocket}).Error()
	for _, fragment := range []string{"run/docker.sock", "socket"} {
		if !strings.Contains(message, fragment) {
			t.Errorf("message %q does not mention %q", message, fragment)
		}
	}
}

// TestTheTwoRestoreOutcomesThatAreNotFailures covers the pair an operator meets
// most often, and which must not read as crashes.
//
// A dry run that changed nothing has to exit non-zero -- otherwise a script
// treats "I did not do it" as "I did it" -- while saying plainly that nothing
// happened and how to actually do it. A failed safety backup has to say what it
// was protecting and how to proceed anyway.
func TestTheTwoRestoreOutcomesThatAreNotFailures(t *testing.T) {
	t.Parallel()

	dry := (&DryRunError{Dir: "backups/2026-08-12"}).Error()
	if !strings.Contains(dry, "nothing was changed") || !strings.Contains(dry, "--apply") {
		t.Errorf("dry-run message %q does not say both that nothing happened and how to proceed", dry)
	}

	cause := errors.New("no space left on device")
	safety := &SafetyBackupError{Err: cause}
	message := safety.Error()
	for _, fragment := range []string{"refusing to overwrite", "--no-safety-backup", "no space left"} {
		if !strings.Contains(message, fragment) {
			t.Errorf("safety-backup message %q does not mention %q", message, fragment)
		}
	}
	if !errors.Is(safety, cause) {
		t.Error("SafetyBackupError does not unwrap to the failure that caused it")
	}
}

// TestATreeThatCannotBeLocatedSaysWhichWay covers both halves of the MIB-tree
// resolution failure: nothing mounts the target, or several services disagree
// about what does. They need different fixes, so they must read differently.
func TestATreeThatCannotBeLocatedSaysWhichWay(t *testing.T) {
	t.Parallel()

	none := (&TreeResolveError{Target: "/app/mibs"}).Error()
	if !strings.Contains(none, "no service bind-mounts") || !strings.Contains(none, "--skip mibs") {
		t.Errorf("message %q does not say nothing mounts it, nor how to proceed without it", none)
	}

	disagree := (&TreeResolveError{
		Target: "/app/mibs", Sources: []string{"/srv/mibs", "/opt/mibs"},
	}).Error()
	if !strings.Contains(disagree, "disagree") {
		t.Errorf("message %q does not distinguish a conflict from an absence", disagree)
	}
	for _, source := range []string{"/srv/mibs", "/opt/mibs"} {
		if !strings.Contains(disagree, source) {
			t.Errorf("message %q does not name the conflicting path %q", disagree, source)
		}
	}
}

// TestEveryEntryKindIsNamedInWords covers the file types a volume can contain
// that a tar cannot carry. Each has to come back as a word: "S---------" tells
// an operator nothing about whether a skipped entry mattered.
func TestEveryEntryKindIsNamedInWords(t *testing.T) {
	t.Parallel()

	tests := map[fs.FileMode]string{
		fs.ModeSocket:                     "socket",
		fs.ModeNamedPipe:                  "named pipe",
		fs.ModeDevice:                     "block device",
		fs.ModeDevice | fs.ModeCharDevice: "character device",
		fs.ModeIrregular:                  "irregular file",
	}

	for mode, want := range tests {
		if got := entryKind(mode); got != want {
			t.Errorf("entryKind(%v) = %q, want %q", mode, got, want)
		}
	}
}
