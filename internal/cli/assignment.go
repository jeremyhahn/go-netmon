package cli

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-netmon/pkg/client"
)

// seriesConfirmThreshold is the projected series count above which an enable
// refuses to act without --yes.
//
// A module's object count understates its cost badly: every pollable column
// becomes one series per table row, so a few hundred objects routinely turn
// into tens of thousands of series. That is enough extra SNMP work to melt a
// modestly powered agent and enough cardinality to hurt Prometheus, and it is
// created by a command that otherwise looks trivial. 5000 sits above what any
// ordinary module costs and below what a runaway table costs, so the guard is
// silent in normal use and loud exactly when it matters.
const seriesConfirmThreshold = 5000

// SeriesBudgetError reports an enable refused because its projected series cost
// exceeds the guard.
type SeriesBudgetError struct {
	Series    int
	Threshold int
}

func (e *SeriesBudgetError) Error() string {
	return fmt.Sprintf(
		"refusing to add a projected %d series, above the %d series guard; re-run with --yes to proceed",
		e.Series, e.Threshold)
}

// assignmentScope binds the enable, disable and filter commands to one side of
// the API — devices or profiles — so both subtrees share one implementation.
type assignmentScope struct {
	// noun names the thing an assignment hangs off, for help text and errors.
	noun string

	// names lists the owners a shell can complete the first argument with.
	names nameLister

	resolve resolver
	get     func(context.Context, *client.Client, int64, int64) (*client.MIBAssignment, error)
	set     func(context.Context, *client.Client, int64, int64, client.MIBAssignment) (*client.MIBAssignment, error)

	// estimate prices enabling a MIB, and is nil where the server cannot answer:
	// a profile has no device whose tables would give the row count a cost
	// depends on.
	estimate func(context.Context, *client.Client, int64, int64) (*client.MIBSeriesEstimate, error)

	// objects explains what is polled from a MIB, object by object. Like
	// estimate it is device-only: a profile does not poll anything itself, so
	// there is no "right now" for it to report.
	//
	// The query narrows the plan on the server, which is the only place that
	// can page it without losing the poller's order.
	objects func(
		context.Context, *client.Client, int64, int64, client.PlanQuery,
	) (*client.MIBObjectPlan, error)
}

// deviceAssignmentScope operates on a device's own MIB assignments.
func deviceAssignmentScope() assignmentScope {
	return assignmentScope{
		noun:    "device",
		names:   deviceNames,
		resolve: resolveDeviceRef,
		get: func(ctx context.Context, api *client.Client, deviceID, mibID int64) (*client.MIBAssignment, error) {
			return api.DeviceMIBAssignment(ctx, deviceID, mibID)
		},
		set: func(
			ctx context.Context, api *client.Client, deviceID, mibID int64, a client.MIBAssignment,
		) (*client.MIBAssignment, error) {
			return api.SetDeviceMIBAssignment(ctx, deviceID, mibID, a)
		},
		estimate: func(
			ctx context.Context, api *client.Client, deviceID, mibID int64,
		) (*client.MIBSeriesEstimate, error) {
			return api.EstimateMIBSeries(ctx, deviceID, mibID)
		},
		objects: func(
			ctx context.Context,
			api *client.Client,
			deviceID, mibID int64,
			query client.PlanQuery,
		) (*client.MIBObjectPlan, error) {
			return api.SearchDeviceMIBObjects(ctx, deviceID, mibID, query)
		},
	}
}

// profileAssignmentScope operates on the assignments inside a profile, which
// reach every device using it.
func profileAssignmentScope() assignmentScope {
	return assignmentScope{
		noun:    "profile",
		names:   profileNames,
		resolve: resolveProfileRef,
		get: func(ctx context.Context, api *client.Client, profileID, mibID int64) (*client.MIBAssignment, error) {
			return api.ProfileMIBAssignment(ctx, profileID, mibID)
		},
		set: func(
			ctx context.Context, api *client.Client, profileID, mibID int64, a client.MIBAssignment,
		) (*client.MIBAssignment, error) {
			return api.SetProfileMIBAssignment(ctx, profileID, mibID, a)
		},
	}
}

// newMIBAssignmentCommands builds the subcommands that control how an owner
// polls the MIBs already attached to it.
func newMIBAssignmentCommands(env *Env, scope assignmentScope) []*cobra.Command {
	cmds := []*cobra.Command{
		newMIBEnableCommand(env, scope),
		newMIBDisableCommand(env, scope),
		newMIBFilterCommand(env, scope),
	}
	if scope.estimate != nil {
		cmds = append(cmds, newMIBCostCommand(env, scope))
	}
	if scope.objects != nil {
		cmds = append(cmds, newDeviceMIBObjectsCommand(env, scope))
	}
	return cmds
}

func newMIBEnableCommand(env *Env, scope assignmentScope) *cobra.Command {
	var yes bool

	cmd := &cobra.Command{
		Use:   "enable <" + scope.noun + "> <mib>...",
		Short: "Resume polling MIBs, keeping their filters",
		Long: "Resume polling MIBs that are already assigned.\n\n" +
			"Filters are left untouched, so a MIB paused with 'disable' comes back\n" +
			"polling exactly what it polled before.",
		Args:              cobra.MinimumNArgs(2),
		ValidArgsFunction: env.completeNamesRepeating(scope.names, mibNames),
		RunE: func(_ *cobra.Command, args []string) error {
			return env.setMIBsEnabled(scope, args[0], args[1:], true, yes)
		},
	}

	if scope.estimate != nil {
		cmd.Long += "\n\nThe projected series cost is printed before anything changes.\n" +
			"A total above " + strconv.Itoa(seriesConfirmThreshold) +
			" series is refused unless you pass --yes."
		cmd.Flags().BoolVar(&yes, "yes", false,
			"enable even when the projected series cost exceeds the guard")
	}
	return cmd
}

func newMIBDisableCommand(env *Env, scope assignmentScope) *cobra.Command {
	return &cobra.Command{
		Use:   "disable <" + scope.noun + "> <mib>...",
		Short: "Stop polling MIBs without unassigning them",
		Long: "Stop polling MIBs while leaving them attached.\n\n" +
			"The assignment and its filters survive, so this pauses a module\n" +
			"rather than forgetting how it was configured.",
		Args:              cobra.MinimumNArgs(2),
		ValidArgsFunction: env.completeNamesRepeating(scope.names, mibNames),
		RunE: func(_ *cobra.Command, args []string) error {
			return env.setMIBsEnabled(scope, args[0], args[1:], false, true)
		},
	}
}

// setMIBsEnabled turns a set of MIBs on or off, preserving their filters.
//
// Every MIB is resolved and, when enabling, priced before the first write, so a
// command that the guard refuses has changed nothing at all.
func (e *Env) setMIBsEnabled(
	scope assignmentScope,
	ownerRef string,
	mibRefs []string,
	enabled, yes bool,
) error {
	ctx, cancel := context.WithTimeout(context.Background(), e.Opts.Timeout)
	defer cancel()

	api, err := e.Client()
	if err != nil {
		return err
	}
	ownerID, err := scope.resolve(ctx, api, ownerRef)
	if err != nil {
		return err
	}
	targets, err := resolveMIBRefs(ctx, api, mibRefs)
	if err != nil {
		return err
	}

	if enabled && scope.estimate != nil {
		if err := e.previewSeriesCost(ctx, api, scope, ownerID, targets, yes); err != nil {
			return err
		}
	}

	verb := "Enabled"
	if !enabled {
		verb = "Disabled"
	}

	changes := make([]changeReport, 0, len(targets))
	for _, target := range targets {
		current, getErr := scope.get(ctx, api, ownerID, target.id)
		if getErr != nil {
			return &ChangeError{Ref: target.ref, Err: getErr}
		}

		current.Enabled = enabled
		if _, setErr := scope.set(ctx, api, ownerID, target.id, *current); setErr != nil {
			return &ChangeError{Ref: target.ref, Err: setErr}
		}

		changes = append(changes, changeReport{
			Action: strings.ToLower(verb), Kind: "mib", Ref: target.ref, ID: target.id,
		})
		if !e.Opts.JSON {
			fmt.Fprintf(e.Out, "%s %s\n", verb, target.ref)
		}
	}

	if e.Opts.JSON {
		return writeJSON(e.Out, changes)
	}
	return nil
}

// previewSeriesCost prints what each MIB would cost and refuses a total above
// the guard.
//
// An estimate the server cannot produce is reported and skipped: not knowing
// the price is not a reason to block a change the operator is entitled to make.
func (e *Env) previewSeriesCost(
	ctx context.Context,
	api *client.Client,
	scope assignmentScope,
	ownerID int64,
	targets []mibTarget,
	yes bool,
) error {
	// The preview is prose, which would corrupt a JSON document; the guard it
	// exists to enforce still applies either way.
	out := e.Out
	if e.Opts.JSON {
		out = io.Discard
	}

	total := 0
	for _, target := range targets {
		estimate, err := scope.estimate(ctx, api, ownerID, target.id)
		if err != nil {
			fmt.Fprintf(out, "%s: series cost unavailable (%v)\n", target.ref, err)
			continue
		}
		total += estimate.Series
		fmt.Fprintln(out, formatSeriesCost(target.ref, estimate))
	}

	if total > seriesConfirmThreshold && !yes {
		return &SeriesBudgetError{Series: total, Threshold: seriesConfirmThreshold}
	}
	return nil
}

// formatSeriesCost renders one line of the cost preview, for example
//
//	IF-MIB: ~960 series (estimated from a median of 24 instances across 3 tables)
//
// A count Prometheus confirmed drops the tilde, because it is not a guess.
func formatSeriesCost(ref string, estimate *client.MIBSeriesEstimate) string {
	approx := "~"
	if estimate.Exact {
		approx = ""
	}
	return fmt.Sprintf("%s: %s%d series (%s)",
		firstNonEmpty(estimate.MIB, ref), approx, estimate.Series, estimate.Explanation)
}

func newMIBFilterCommand(env *Env, scope assignmentScope) *cobra.Command {
	var (
		flags        client.MIBAssignment
		clearFilters bool
	)

	cmd := &cobra.Command{
		Use:   "filter <" + scope.noun + "> <mib>",
		Short: "Narrow which objects of a MIB are polled",
		Long: "Restrict a MIB to the objects worth collecting.\n\n" +
			"Patterns are Go regular expressions matched against an object's name\n" +
			"or OID; excludes win over includes. Each flag replaces the stored list\n" +
			"for its field, and fields you do not name keep their current value.\n\n" +
			"Example:\n" +
			"  netmon " + scope.noun + " mib filter core-01 IF-MIB \\\n" +
			"      --include-names '^ifHC(In|Out)Octets$' --max-oids 200",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: env.completeNames(scope.names, mibNames),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Refuse a malformed pattern here, before the assignment is even
			// read, so a typo costs no round trip and reaches no server.
			if err := flags.Validate(); err != nil {
				return err
			}
			return env.setMIBFilters(cmd, scope, args[0], args[1], flags, clearFilters)
		},
	}

	// These take regular expressions, so they must be StringArray rather than
	// StringSlice: StringSlice splits its value on commas, which silently
	// tears a pattern like "if{2,4}" into two invalid ones. Repeat the flag to
	// supply several patterns.
	f := cmd.Flags()
	f.StringArrayVar(&flags.IncludeNames, "include-names", nil,
		"poll only objects whose name matches this regexp (repeat for more)")
	f.StringArrayVar(&flags.ExcludeNames, "exclude-names", nil,
		"never poll objects whose name matches this regexp (repeat for more)")
	f.StringArrayVar(&flags.IncludeOIDs, "include-oids", nil,
		"poll only objects whose OID matches this regexp (repeat for more)")
	f.StringArrayVar(&flags.ExcludeOIDs, "exclude-oids", nil,
		"never poll objects whose OID matches this regexp (repeat for more)")
	f.IntVar(&flags.MaxOIDs, "max-oids", 0,
		"cap how many objects this MIB contributes (0 for no cap)")
	f.BoolVar(&clearFilters, "clear", false,
		"remove every filter, leaving the enabled state alone")
	return cmd
}

// setMIBFilters rewrites one MIB's filters.
func (e *Env) setMIBFilters(
	cmd *cobra.Command,
	scope assignmentScope,
	ownerRef, mibRef string,
	flags client.MIBAssignment,
	clearFilters bool,
) error {
	ctx, cancel := context.WithTimeout(context.Background(), e.Opts.Timeout)
	defer cancel()

	api, err := e.Client()
	if err != nil {
		return err
	}
	ownerID, err := scope.resolve(ctx, api, ownerRef)
	if err != nil {
		return err
	}
	mibID, err := api.ResolveMIB(ctx, mibRef)
	if err != nil {
		return err
	}

	// The API replaces the whole assignment, so start from the stored one and
	// overlay only the fields the user actually named. Enabled is never touched
	// here: filtering a MIB is not a statement about whether it is polled.
	current, err := scope.get(ctx, api, ownerID, mibID)
	if err != nil {
		return err
	}
	applyFilterFlagOverrides(cmd, current, flags, clearFilters)

	stored, err := scope.set(ctx, api, ownerID, mibID, *current)
	if err != nil {
		return err
	}
	if e.Opts.JSON {
		return writeJSON(e.Out, stored)
	}
	return renderAssignment(e.Out, mibRef, stored)
}

// applyFilterFlagOverrides copies only explicitly-set flags onto current.
//
// A list flag replaces the stored list rather than adding to it, so one command
// states the whole rule for the fields it names. --clear is applied first, which
// makes "--clear --include-names X" mean "these filters and nothing else".
func applyFilterFlagOverrides(
	cmd *cobra.Command,
	current *client.MIBAssignment,
	flags client.MIBAssignment,
	clearFilters bool,
) {
	if clearFilters {
		current.IncludeOIDs = nil
		current.ExcludeOIDs = nil
		current.IncludeNames = nil
		current.ExcludeNames = nil
		current.MaxOIDs = 0
	}

	if cmd.Flags().Changed("include-names") {
		current.IncludeNames = flags.IncludeNames
	}
	if cmd.Flags().Changed("exclude-names") {
		current.ExcludeNames = flags.ExcludeNames
	}
	if cmd.Flags().Changed("include-oids") {
		current.IncludeOIDs = flags.IncludeOIDs
	}
	if cmd.Flags().Changed("exclude-oids") {
		current.ExcludeOIDs = flags.ExcludeOIDs
	}
	if cmd.Flags().Changed("max-oids") {
		current.MaxOIDs = flags.MaxOIDs
	}
}

// renderAssignment reports how a MIB is polled after a change.
func renderAssignment(out io.Writer, mibRef string, a *client.MIBAssignment) error {
	fmt.Fprintf(out, "%s assignment:\n", mibRef)
	return keyValues(out,
		[2]string{"  Enabled", yesNo(a.Enabled)},
		[2]string{"  Include names", dash(strings.Join(a.IncludeNames, ", "))},
		[2]string{"  Exclude names", dash(strings.Join(a.ExcludeNames, ", "))},
		[2]string{"  Include OIDs", dash(strings.Join(a.IncludeOIDs, ", "))},
		[2]string{"  Exclude OIDs", dash(strings.Join(a.ExcludeOIDs, ", "))},
		[2]string{"  Max OIDs", maxOIDs(a.MaxOIDs)},
	)
}

// maxOIDs renders an object cap, naming the absence of one rather than showing
// a zero that reads like "poll nothing".
func maxOIDs(n int) string {
	if n <= 0 {
		return "no cap"
	}
	return strconv.Itoa(n)
}

func newMIBCostCommand(env *Env, scope assignmentScope) *cobra.Command {
	return &cobra.Command{
		Use:     "cost <" + scope.noun + "> <mib>",
		Aliases: []string{"estimate"},
		Short:   "Estimate the series a MIB would cost on a device",
		Long: "Report how many Prometheus series polling a MIB adds to a device.\n\n" +
			"Object count is the wrong number to reason with: every pollable table\n" +
			"column costs one series per row, so a module with a few hundred objects\n" +
			"can produce tens of thousands of series. The MIB need not be assigned —\n" +
			"asking before committing is the point.",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: env.completeNames(scope.names, mibNames),
		RunE: func(_ *cobra.Command, args []string) error {
			return env.runWithRefresh(func(ctx context.Context, out io.Writer) error {
				api, err := env.Client()
				if err != nil {
					return err
				}
				ownerID, err := scope.resolve(ctx, api, args[0])
				if err != nil {
					return err
				}
				mibID, err := api.ResolveMIB(ctx, args[1])
				if err != nil {
					return err
				}

				estimate, err := scope.estimate(ctx, api, ownerID, mibID)
				if err != nil {
					return err
				}
				if env.Opts.JSON {
					return writeJSON(out, estimate)
				}
				return renderSeriesEstimate(out, estimate)
			})
		},
	}
}

// renderSeriesEstimate prints a cost estimate and the reasoning behind it.
func renderSeriesEstimate(out io.Writer, e *client.MIBSeriesEstimate) error {
	series := strconv.Itoa(e.Series)
	if !e.Exact {
		series = "~" + series
	}

	err := keyValues(out,
		[2]string{"MIB", e.MIB},
		[2]string{"Device", e.Device},
		[2]string{"Series", series},
		[2]string{"Exact", yesNo(e.Exact)},
		[2]string{"Pollable scalars", strconv.Itoa(e.Scalars)},
		[2]string{"Pollable columns", strconv.Itoa(e.Columns)},
		[2]string{"Instances per table", strconv.Itoa(e.Instances)},
	)
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "\n%s\n", e.Explanation)
	return nil
}

// mibTarget is a resolved MIB reference: the ID to act on, and the text the
// user typed, which is what messages about it should name.
type mibTarget struct {
	ref string
	id  int64
}

// resolveMIBRefs resolves every reference before any of them is acted on, so a
// typo in the last argument does not leave the first ones half applied.
func resolveMIBRefs(ctx context.Context, api *client.Client, refs []string) ([]mibTarget, error) {
	targets := make([]mibTarget, 0, len(refs))
	for _, ref := range refs {
		id, err := api.ResolveMIB(ctx, ref)
		if err != nil {
			return nil, err
		}
		targets = append(targets, mibTarget{ref: ref, id: id})
	}
	return targets, nil
}

// filterSummary describes an assignment's filters for a table cell, for example
// "2 include, 1 exclude, max 50". An unfiltered assignment renders as "-".
func filterSummary(a client.MIBAssignment) string {
	parts := make([]string, 0, 3)
	if n := len(a.IncludeNames) + len(a.IncludeOIDs); n > 0 {
		parts = append(parts, fmt.Sprintf("%d include", n))
	}
	if n := len(a.ExcludeNames) + len(a.ExcludeOIDs); n > 0 {
		parts = append(parts, fmt.Sprintf("%d exclude", n))
	}
	if a.MaxOIDs > 0 {
		parts = append(parts, fmt.Sprintf("max %d", a.MaxOIDs))
	}
	return dash(strings.Join(parts, ", "))
}
