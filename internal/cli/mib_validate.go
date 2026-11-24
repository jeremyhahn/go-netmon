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

// "netmon mib validate" checks locally that a directory works as a deployment's only MIB source; a module that loads with no objects fails.

// mibExtensions are the file names this walks. MIB files have no single
// convention; these are the four in use across the vendors here.
var mibExtensions = map[string]bool{
	".mib": true, ".my": true, ".txt": true, ".smi": true,
}

// moduleHeader matches the DEFINITIONS line that names a module; file names are not authoritative.
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
	// degraded: the module loaded but some IMPORTS did not resolve.
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

// validateModule loads and parses one file, recovering parser panics.
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

	// Load by path, as the importer does; gosmi finds modules by file name.
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

// definesObjects reports whether a module's source declares any object; TC-only modules declare none.
func definesObjects(path string) bool {
	content, err := os.ReadFile(path) // #nosec G304 -- an operator-supplied path is the argument of this command
	if err != nil {
		// Unreadable is reported by the load itself; assume it should have
		// objects so the emptiness is not silently excused.
		return true
	}
	return objectDefinition.Match(stripMIBComments(content))
}

// objectDefinition matches constructs that produce an OID node, anchored to the identifier.
var objectDefinition = regexp.MustCompile(
	`(?m)^\s*[a-zA-Z][a-zA-Z0-9-]*\s+(OBJECT-TYPE|OBJECT-IDENTITY|OBJECT\s+IDENTIFIER\s*::=)`)

// mibComment matches an ASN.1 comment: "--" to the end of the line.
var mibComment = regexp.MustCompile(`--[^\n]*`)

// stripMIBComments removes MIB comments so prose is not mistaken for a definition.
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
