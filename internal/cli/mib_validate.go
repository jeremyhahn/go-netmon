package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-netmon/internal/mib/parser"
)

// "netmon mib validate" answers one question: would this directory work as the
// only MIB source a deployment has?
//
// It is deliberately local. Every other command here is an HTTP client of the
// web UI, but this one has to run against a directory before anything is
// deployed - checking a tree destined for a fresh host, or proving that the
// internal store is self-sufficient before a bind mount is removed - and a
// running server is exactly what it must not need.
//
// The check that matters is not "did it parse". gosmi resolves a module's
// IMPORTS against the search path and, when it cannot find one, reports
// success and returns a module with no nodes. A deployment then runs with a
// module that resolves nothing, and the first sign is metrics that never
// appear. So a module that loads but contributes no objects is a failure here,
// reported separately from one that could not be read at all.

// mibExtensions are the file names this walks. MIB files have no single
// convention; these are the four in use across the vendors here.
var mibExtensions = map[string]bool{
	".mib": true, ".my": true, ".txt": true, ".smi": true,
}

// moduleHeader matches the DEFINITIONS line that names a module. A file's name
// is not authoritative: mibs/asr1001/EVENT-MIB.my declares DISMAN-EVENT-MIB,
// and gosmi resolves by declared name.
var moduleHeader = regexp.MustCompile(`(?m)^\s*([A-Za-z0-9-]+)\s+DEFINITIONS\s*::=\s*BEGIN`)

// validation is the outcome for one module.
type validation struct {
	file    string
	module  string
	objects int
	err     error
	// empty marks a module that loaded but contributed no objects, which means
	// its imports did not resolve.
	empty bool
	// degraded is set when the module loaded but some of its IMPORTS did not
	// resolve, leaving objects without a type (#62). It is a warning: the
	// module is usable, just not whole.
	degraded error
	// skipped marks a file that is not a MIB module at all. A directory of MIBs
	// routinely holds a README or a notes file, and calling those failures
	// makes the report cry wolf on every real tree.
	skipped bool
}

// The env is unused: unlike its siblings this command talks to no server, by
// design — it has to work before anything is deployed. It is accepted so the
// command constructors stay uniform in the mib subtree.
func newMIBValidateCommand(_ *Env) *cobra.Command {
	var quiet bool

	cmd := &cobra.Command{
		Use:   "validate <dir>...",
		Short: "Check that a directory of MIBs parses and resolves on its own",
		Long: strings.TrimSpace(`
Parse every MIB in the given directories, using only those directories as the
search path, and report what a deployment would get from them.

Three outcomes are distinguished:

  ok        the module parsed and contributed objects
  EMPTY     the module parsed and contributed nothing, which means an IMPORTS
            clause did not resolve. gosmi reports this as success, so it is
            otherwise invisible until metrics fail to appear.
  FAILED    the module could not be read at all

Exits non-zero if anything is EMPTY or FAILED, so it can gate a deployment.`),
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMIBValidate(cmd.OutOrStdout(), args, quiet)
		},
	}
	cmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "Only report modules that are EMPTY or FAILED")
	return cmd
}

func runMIBValidate(out io.Writer, dirs []string, quiet bool) error {
	resolved := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return &MIBValidateError{Dir: dir, Err: err}
		}
		info, err := os.Stat(abs)
		if err != nil {
			return &MIBValidateError{Dir: dir, Err: err}
		}
		if !info.IsDir() {
			return &MIBValidateError{Dir: dir, Reason: "not a directory"}
		}
		resolved = append(resolved, abs)
	}

	files, err := collectMIBFiles(resolved)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return &MIBValidateError{Dir: strings.Join(dirs, ", "), Reason: "holds no MIB files"}
	}

	// The search path is exactly the directories given. Anything a module needs
	// has to be in them, which is the property being checked.
	p := parser.New(resolved...)
	if err := p.Init(); err != nil {
		return &MIBValidateError{Dir: strings.Join(dirs, ", "), Err: err}
	}
	defer p.Close()

	results := make([]validation, 0, len(files))
	for _, file := range files {
		results = append(results, validateModule(p, file))
	}

	return reportValidation(out, resolved, results, quiet)
}

// collectMIBFiles lists the MIB files in the given directories, sorted so the
// report is stable between runs.
func collectMIBFiles(dirs []string) ([]string, error) {
	var files []string
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, &MIBValidateError{Dir: dir, Err: err}
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			if mibExtensions[strings.ToLower(filepath.Ext(entry.Name()))] {
				files = append(files, filepath.Join(dir, entry.Name()))
			}
		}
	}
	sort.Strings(files)
	return files, nil
}

// validateModule loads and parses one file, recovering panics.
//
// A malformed construct can panic inside the parser rather than returning an
// error - an unintended participle bump made every module with an empty
// bit-string DEFVAL do exactly that. A validator that dies on the first such
// file cannot report the other two hundred, which is the moment its report is
// most wanted.
func validateModule(p *parser.Parser, file string) (result validation) {
	result = validation{file: file, module: declaredModule(file)}

	defer func() {
		if r := recover(); r != nil {
			result.err = fmt.Errorf("parser panicked: %v", r)
		}
	}()

	if result.module == "" {
		result.skipped = true
		return result
	}

	// Loaded by path, which is what the importer does. Loading by declared name
	// would be wrong: gosmi finds a module by FILE name, and the two differ
	// often enough to matter - mibs/asr1001/EVENT-MIB.my declares
	// DISMAN-EVENT-MIB. Asking for the declared name reports four working
	// modules as unloadable.
	info, err := p.LoadModuleFromFile(file)
	if err != nil {
		result.err = err
		return result
	}
	if info != nil && info.Name != "" {
		result.module = info.Name
	}

	oids, report, err := p.ParseModuleWithReport(result.module)
	if err != nil {
		result.err = err
		return result
	}

	result.objects = len(oids)
	result.degraded = report.Err()
	result.empty = result.objects == 0 && definesObjects(file)
	return result
}

// definesObjects reports whether a module's source declares anything that
// should become an object.
//
// Contributing no objects is the signature of an IMPORTS clause that did not
// resolve - but it is also correct for a module that defines only textual
// conventions or compliance statements. SNMPv2-TC and SNMPv2-CONF are both in
// the standard set and both legitimately produce nothing, so flagging every
// empty module would cry wolf on two of the most common files there are.
func definesObjects(path string) bool {
	content, err := os.ReadFile(path) // #nosec G304 -- an operator-supplied path is the argument of this command
	if err != nil {
		// Unreadable is reported by the load itself; assume it should have
		// objects so the emptiness is not silently excused.
		return true
	}
	return objectDefinition.Match(stripMIBComments(content))
}

// objectDefinition matches the constructs that produce a node with an OID. Each
// is anchored to the identifier being defined, so the words alone - which occur
// throughout ordinary prose in a DESCRIPTION - do not match.
var objectDefinition = regexp.MustCompile(
	`(?m)^\s*[a-zA-Z][a-zA-Z0-9-]*\s+(OBJECT-TYPE|OBJECT-IDENTITY|OBJECT\s+IDENTIFIER\s*::=)`)

// mibComment matches an ASN.1 comment: "--" to the end of the line.
var mibComment = regexp.MustCompile(`--[^\n]*`)

// stripMIBComments removes comments so a construct named in prose is not
// mistaken for a definition.
//
// SNMPv2-CONF defines only compliance macros and correctly produces no objects,
// but it explains itself with the line
//
//	-- or if not, in object's OBJECT-TYPE macro
//
// which is enough for an unanchored search to conclude it should have had some,
// and to report a perfectly good standard module as broken.
func stripMIBComments(content []byte) []byte {
	return mibComment.ReplaceAll(content, nil)
}

// declaredModule reads the module name a file declares, or "" if it declares
// none.
func declaredModule(path string) string {
	// Only the header is needed, but MIB files are small enough that reading
	// one whole is simpler than managing a partial read.
	content, err := os.ReadFile(path) // #nosec G304 -- an operator-supplied path is the argument of this command
	if err != nil {
		return ""
	}
	if match := moduleHeader.FindSubmatch(content); match != nil {
		return string(match[1])
	}
	return ""
}

// reportValidation prints the outcome and returns an error if the directories
// would not stand on their own.
func reportValidation(out io.Writer, dirs []string, results []validation, quiet bool) error {
	var empty, failed, skipped, degraded int

	for _, r := range results {
		name := r.module
		if name == "" {
			name = filepath.Base(r.file)
		}
		switch {
		case r.skipped:
			skipped++
			if !quiet {
				fmt.Fprintf(out, "skip    %-40s not a MIB module\n", name)
			}
		case r.err != nil:
			failed++
			fmt.Fprintf(out, "FAILED  %-40s %s\n", name, r.err)
		case r.empty:
			empty++
			fmt.Fprintf(out, "EMPTY   %-40s imports did not resolve; contributed no objects\n", name)
		case r.degraded != nil:
			// Shown even when quiet: the module works, but not completely.
			degraded++
			fmt.Fprintf(out, "WARN    %-40s %d objects; %s\n", name, r.objects, r.degraded)
		case !quiet:
			fmt.Fprintf(out, "ok      %-40s %d objects\n", name, r.objects)
		}
	}

	modules := len(results) - skipped
	fmt.Fprintf(out, "\n%d modules in %s: %d ok, %d empty, %d failed",
		modules, strings.Join(dirs, ", "), modules-empty-failed, empty, failed)
	if degraded > 0 {
		fmt.Fprintf(out, " (%d ok with unresolved imports)", degraded)
	}
	if skipped > 0 {
		fmt.Fprintf(out, " (%d non-MIB files skipped)", skipped)
	}
	fmt.Fprintln(out)

	if empty > 0 || failed > 0 {
		return &MIBValidateResultError{Empty: empty, Failed: failed, Total: modules}
	}
	return nil
}

// MIBValidateError reports a directory that could not be validated.
type MIBValidateError struct {
	Dir    string
	Reason string
	Err    error
}

func (e *MIBValidateError) Error() string {
	if e.Err != nil {
		return "mib validate: " + e.Dir + ": " + e.Err.Error()
	}
	return "mib validate: " + e.Dir + ": " + e.Reason
}

func (e *MIBValidateError) Unwrap() error { return e.Err }

// MIBValidateResultError reports modules that would not work as a set.
type MIBValidateResultError struct {
	Empty  int
	Failed int
	Total  int
}

func (e *MIBValidateResultError) Error() string {
	return fmt.Sprintf(
		"mib validate: %d of %d modules would not resolve (%d empty, %d failed)",
		e.Empty+e.Failed, e.Total, e.Empty, e.Failed)
}
