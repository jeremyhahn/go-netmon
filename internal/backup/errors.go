package backup

import (
	"strconv"
	"strings"
)

// --- containment ----------------------------------------------------------

// PathEscapeError reports a path that cannot be expressed inside a root.
//
// It is returned before any system call is attempted. The kernel-level check in
// os.Root refuses the same paths; this type exists so the refusal carries the
// root, its stated purpose and the reason, which a bare syscall error does not.
type PathEscapeError struct {
	// Purpose is the reason the root was opened, as given to Open or Create.
	Purpose string
	// Root is the absolute directory the root is anchored on.
	Root string
	// Path is the offending path, exactly as the caller supplied it.
	Path string
	// Reason explains why the path cannot be used.
	Reason string
}

func (e *PathEscapeError) Error() string {
	return "backup: " + strconv.Quote(e.Path) + " cannot be used inside the " +
		e.Purpose + " root " + e.Root + ": " + e.Reason
}

// SymlinkEscapeError reports a symbolic link whose target leaves the root.
type SymlinkEscapeError struct {
	Purpose string
	Root    string
	Name    string
	Target  string
	Reason  string
}

func (e *SymlinkEscapeError) Error() string {
	return "backup: symlink " + strconv.Quote(e.Name) + " -> " + strconv.Quote(e.Target) +
		" leaves the " + e.Purpose + " root " + e.Root + ": " + e.Reason
}

// RootOpenError reports a failure to anchor a root on a directory.
type RootOpenError struct {
	Purpose string
	Dir     string
	Reason  string
	Err     error
}

func (e *RootOpenError) Error() string {
	msg := "backup: cannot open the " + e.Purpose + " root on " + e.Dir + ": " + e.Reason
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

func (e *RootOpenError) Unwrap() error { return e.Err }

// RootOpError reports a filesystem operation that failed inside a root.
type RootOpError struct {
	Op      string
	Purpose string
	Root    string
	Path    string
	Err     error
}

func (e *RootOpError) Error() string {
	return "backup: " + e.Op + " " + strconv.Quote(e.Path) + " in the " +
		e.Purpose + " root " + e.Root + ": " + e.Err.Error()
}

func (e *RootOpError) Unwrap() error { return e.Err }

// --- component selection --------------------------------------------------

// UnknownComponentError reports a component name this tool does not know.
type UnknownComponentError struct {
	Name  string
	Valid []Component
}

func (e *UnknownComponentError) Error() string {
	names := make([]string, len(e.Valid))
	for i, c := range e.Valid {
		names[i] = string(c)
	}
	return "backup: unknown component " + strconv.Quote(e.Name) +
		" (valid: " + strings.Join(names, ", ") + ")"
}

// SelectionConflictError reports mutually exclusive selection options.
type SelectionConflictError struct {
	Reason string
}

func (e *SelectionConflictError) Error() string {
	return "backup: " + e.Reason
}

// EmptySelectionError reports a selection that resolves to no components.
type EmptySelectionError struct{}

func (e *EmptySelectionError) Error() string {
	return "backup: no components selected"
}

// ComponentAbsentError reports a component the archive does not carry.
type ComponentAbsentError struct {
	Name      Component
	Available []Component
}

func (e *ComponentAbsentError) Error() string {
	names := make([]string, len(e.Available))
	for i, c := range e.Available {
		names[i] = string(c)
	}
	return "backup: component " + strconv.Quote(string(e.Name)) +
		" is not present in this backup (has: " + strings.Join(names, ", ") + ")"
}

// --- preflight ------------------------------------------------------------

// ComposeFileError reports a missing or unusable compose file.
type ComposeFileError struct {
	Path   string
	Reason string
}

func (e *ComposeFileError) Error() string {
	return "backup: compose file " + e.Path + ": " + e.Reason
}

// ForeignProjectError reports containers in the target compose project that the
// configured compose file did not create.
type ForeignProjectError struct {
	Project     string
	ComposeFile string
	Containers  []Container
}

func (e *ForeignProjectError) Error() string {
	var b strings.Builder
	b.WriteString("backup: compose project ")
	b.WriteString(strconv.Quote(e.Project))
	b.WriteString(" holds containers ")
	b.WriteString(e.ComposeFile)
	b.WriteString(" did not create, refusing to operate on it:")
	for _, c := range e.Containers {
		b.WriteString("\n  ")
		b.WriteString(c.Name)
		b.WriteString("  (")
		b.WriteString(c.ConfigFiles)
		b.WriteString(")")
	}
	return b.String()
}

// VolumeOwnershipError reports a volume that does not carry the pinned
// project's compose label.
type VolumeOwnershipError struct {
	Volume  string
	Project string
	Labeled string
}

func (e *VolumeOwnershipError) Error() string {
	labeled := e.Labeled
	if labeled == "" {
		labeled = "<none>"
	}
	return "backup: volume " + strconv.Quote(e.Volume) + " is labeled project " +
		strconv.Quote(labeled) + ", not " + strconv.Quote(e.Project) +
		"; refusing to touch it"
}

// VolumeMissingError reports a volume the runtime does not know.
type VolumeMissingError struct {
	Volume string
}

func (e *VolumeMissingError) Error() string {
	return "backup: volume not found: " + e.Volume
}

// VolumeUndefinedError reports a component whose volume the target compose file
// does not define.
type VolumeUndefinedError struct {
	Component Component
	Volume    string
	File      string
}

func (e *VolumeUndefinedError) Error() string {
	return "backup: compose file " + e.File + " does not define volume " +
		strconv.Quote(e.Volume) + " needed by component " + strconv.Quote(string(e.Component))
}

// InvalidNameError reports a Docker object name that would not be safe to pass
// on a command line.
type InvalidNameError struct {
	Kind string
	Name string
}

func (e *InvalidNameError) Error() string {
	return "backup: refusing to use " + e.Kind + " name " + strconv.Quote(e.Name) +
		": names must match [A-Za-z0-9][A-Za-z0-9_.-]*"
}

// --- MIB source tree ------------------------------------------------------

// TreeResolveError reports a MIB source tree that cannot be located, or that
// more than one service disagrees about.
type TreeResolveError struct {
	Target  string
	Sources []string
}

func (e *TreeResolveError) Error() string {
	if len(e.Sources) == 0 {
		return "backup: no service bind-mounts a host directory at " + e.Target +
			", so the MIB source tree cannot be located; pass --skip mibs or --mib-tree DIR"
	}
	return "backup: services disagree about the host directory mounted at " + e.Target +
		" (" + strings.Join(e.Sources, ", ") + "); resolve the compose file or pass --mib-tree DIR"
}

// UnsafeTreeError reports a MIB source tree path that must never be emptied.
type UnsafeTreeError struct {
	Dir    string
	Reason string
}

func (e *UnsafeTreeError) Error() string {
	return "backup: refusing to use " + e.Dir + " as the MIB source tree: " + e.Reason
}

// EmptyTreeError reports a MIB source tree with nothing in it to back up.
type EmptyTreeError struct {
	Dir string
}

func (e *EmptyTreeError) Error() string {
	return "backup: MIB source tree holds no files: " + e.Dir
}

// TreeOverwriteError reports a restore that would replace a populated MIB
// source tree without the explicit opt-in that requires.
type TreeOverwriteError struct {
	Dir      string
	Existing int
	Incoming int
}

func (e *TreeOverwriteError) Error() string {
	return "backup: restoring 'mibs' would delete the " + strconv.Itoa(e.Existing) +
		" file(s) in " + e.Dir + " and replace them with " + strconv.Itoa(e.Incoming) +
		" from the backup; that directory is part of your working tree, not a Docker volume" +
		" - pass --allow-tree-overwrite to replace it or --skip mibs to leave it alone"
}

// --- archive integrity ----------------------------------------------------

// ManifestError reports a manifest that cannot be read or does not describe a
// usable backup.
type ManifestError struct {
	Path   string
	Reason string
	Err    error
}

func (e *ManifestError) Error() string {
	msg := "backup: manifest " + e.Path + ": " + e.Reason
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

func (e *ManifestError) Unwrap() error { return e.Err }

// SchemaError reports an archive written by a newer tool than this one.
type SchemaError struct {
	Found     int
	Supported int
}

func (e *SchemaError) Error() string {
	return "backup: archive schema_version " + strconv.Itoa(e.Found) +
		" is newer than this tool supports (" + strconv.Itoa(e.Supported) + ")"
}

// ChecksumFileError reports a missing or malformed checksums.sha256.
type ChecksumFileError struct {
	Reason string
	Line   int
}

func (e *ChecksumFileError) Error() string {
	if e.Line > 0 {
		return "backup: checksums.sha256 line " + strconv.Itoa(e.Line) + ": " + e.Reason
	}
	return "backup: checksums.sha256: " + e.Reason
}

// MemberError reports a member of the archive that failed verification.
//
// Every member check - presence, size, digest and a full decompression - reports
// through this one type so a caller can treat "the archive is not trustworthy"
// as a single condition.
type MemberError struct {
	Member   string
	Check    string
	Expected string
	Actual   string
	Err      error
}

func (e *MemberError) Error() string {
	msg := "backup: member " + e.Member + " failed its " + e.Check + " check"
	if e.Expected != "" || e.Actual != "" {
		msg += " (expected " + e.Expected + ", got " + e.Actual + ")"
	}
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

func (e *MemberError) Unwrap() error { return e.Err }

// UnlistedFileError reports a file present in the archive that
// checksums.sha256 does not cover.
type UnlistedFileError struct {
	Path string
}

func (e *UnlistedFileError) Error() string {
	return "backup: " + e.Path + " is present in the archive but not listed in checksums.sha256"
}

// --- backup and restore ---------------------------------------------------

// ExistsError reports a destination directory that is already in use.
type ExistsError struct {
	Dir string
}

func (e *ExistsError) Error() string {
	return "backup: destination already exists: " + e.Dir
}

// ProjectMismatchError reports a backup taken from a different compose project.
type ProjectMismatchError struct {
	Backup string
	Target string
}

func (e *ProjectMismatchError) Error() string {
	return "backup: this archive was taken from compose project " + strconv.Quote(e.Backup) +
		" and the target is " + strconv.Quote(e.Target) +
		"; restoring across projects writes one deployment's data into another" +
		" - pass --force-project if that is genuinely intended"
}

// DryRunError reports a restore that printed its plan and changed nothing.
//
// It is an error so that a scripted "restore" without the explicit opt-in exits
// non-zero rather than looking like it succeeded.
type DryRunError struct {
	Dir string
}

func (e *DryRunError) Error() string {
	return "backup: nothing was changed; re-run with --apply to perform this restore"
}

// SafetyBackupError reports a failure to preserve the current state before
// overwriting it.
type SafetyBackupError struct {
	Err error
}

func (e *SafetyBackupError) Error() string {
	return "backup: safety backup failed, refusing to overwrite data that is not backed up" +
		" (pass --no-safety-backup to override): " + e.Err.Error()
}

func (e *SafetyBackupError) Unwrap() error { return e.Err }

// --- docker ---------------------------------------------------------------

// DockerError reports a failed Docker invocation.
type DockerError struct {
	Args   []string
	Stderr string
	Err    error
}

func (e *DockerError) Error() string {
	msg := "backup: docker " + strings.Join(e.Args, " ") + " failed: " + e.Err.Error()
	if s := strings.TrimSpace(e.Stderr); s != "" {
		msg += ": " + s
	}
	return msg
}

func (e *DockerError) Unwrap() error { return e.Err }

// DockerOutputError reports Docker output this tool cannot make sense of.
//
// Unexpected output is fatal rather than something to guess at: every decision
// made from it decides what gets overwritten.
type DockerOutputError struct {
	Command string
	Reason  string
	Output  string
}

func (e *DockerOutputError) Error() string {
	return "backup: cannot interpret the output of docker " + e.Command + ": " +
		e.Reason + ": " + strconv.Quote(truncate(e.Output, 200))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// LogicalFetchError reports a failed request during the logical export.
//
// The export runs against a live web UI before anything is stopped, and every
// one of its failures is survivable: the backup records
// logical.status=unavailable and carries on with the physical copy.
type LogicalFetchError struct {
	Path   string
	Status int
	Err    error
}

func (e *LogicalFetchError) Error() string {
	if e.Err != nil {
		return "backup: logical export: GET " + e.Path + ": " + e.Err.Error()
	}
	return "backup: logical export: GET " + e.Path + ": unexpected status " +
		strconv.Itoa(e.Status)
}

func (e *LogicalFetchError) Unwrap() error { return e.Err }

// LogicalDecodeError reports a response the logical export could not read.
type LogicalDecodeError struct {
	Path string
	Err  error
}

func (e *LogicalDecodeError) Error() string {
	return "backup: logical export: cannot read the response to " + e.Path + ": " + e.Err.Error()
}

func (e *LogicalDecodeError) Unwrap() error { return e.Err }
