package cli

import (
	"context"
	"fmt"
	"io"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-netmon/pkg/client"
)

// objectPlanFilter narrows a device's object plan for display. A large MIB has
// hundreds of objects and most of them are covered by a table walk, so the
// interesting view is usually the polled ones.
type objectPlanFilter struct {
	listFilter[client.MIBObjectDecision]

	polledOnly bool
}

// newObjectPlanFilter builds the filter "device mib objects" is driven by.
func newObjectPlanFilter() *objectPlanFilter {
	return &objectPlanFilter{
		listFilter: listFilter[client.MIBObjectDecision]{
			noun:         "objects",
			fields:       "name, OID or description",
			defaultLimit: defaultObjectLimit,
			paged:        true,
			matches: func(o client.MIBObjectDecision, needle string) bool {
				return objectMatches(o.MIBObject, needle)
			},
		},
	}
}

// apply drops what --polled-only excludes, then searches and pages what is
// left, keeping the order the server gave — which is the order a max_oids cap
// was spent in, so re-sorting would change which objects look capped.
func (f *objectPlanFilter) apply(objects []client.MIBObjectDecision) listPage[client.MIBObjectDecision] {
	if f.polledOnly {
		polled := make([]client.MIBObjectDecision, 0, len(objects))
		for _, o := range objects {
			if o.Polled {
				polled = append(polled, o)
			}
		}
		objects = polled
	}
	return f.listFilter.apply(objects)
}

// query is the narrowing to ask the server for. The server holds the plan and
// its order, so it is also the only place that can page it.
func (f *objectPlanFilter) query() client.PlanQuery {
	return client.PlanQuery{
		ListQuery:  f.listQuery(),
		PolledOnly: f.polledOnly,
	}
}

// bind declares the flags that drive an objectPlanFilter.
func (f *objectPlanFilter) bind(cmd *cobra.Command) {
	f.listFilter.bind(cmd)
	cmd.Flags().BoolVar(&f.polledOnly, "polled-only", false,
		"only objects this device is actually polling")
}

// objectPlanReport is the --json form of "device mib objects": the plan exactly
// as the server computed it, its objects narrowed to what the filter selected,
// and a listing block saying what was held back.
type objectPlanReport struct {
	client.MIBObjectPlan

	Listing listCounts `json:"listing"`
}

func newDeviceMIBObjectsCommand(env *Env, scope assignmentScope) *cobra.Command {
	filter := newObjectPlanFilter()

	cmd := &cobra.Command{
		Use:     "objects <" + scope.noun + "> <mib>",
		Aliases: []string{"oids"},
		Short:   "Show which of a MIB's objects a " + scope.noun + " polls",
		Long: "List a MIB's objects with, for each, whether this " + scope.noun + " is polling\n" +
			"it right now and why not when it is not.\n\n" +
			"The verdict comes from the server, computed by the same code the poller\n" +
			"runs, so it is what is actually collected rather than what the filters\n" +
			"appear to say. Reasons are:\n\n" +
			"  polled                 fetched every poll\n" +
			"  covered-by-table-walk  its table is walked, which returns it already\n" +
			"  not-accessible         the agent can never return it\n" +
			"  not-current            deprecated or obsolete in the MIB\n" +
			"  excluded-by-filter     an exclude pattern removed it\n" +
			"  not-included-by-filter no include pattern matched it\n" +
			"  capped-by-max-oids     the module's max_oids was already spent\n" +
			"  module-disabled        the MIB is assigned but not being polled\n\n" +
			"--search matches an object's name, OID or description, ignoring\n" +
			"case, and is applied before --limit. Both run on the server, which\n" +
			"holds the plan, so only the objects printed cross the wire and\n" +
			"--offset pages through the rest in the poller's own order.\n\n" +
			"Example:\n" +
			"  netmon " + scope.noun + " mib objects core-01 IF-MIB --polled-only",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: env.completeNames(scope.names, mibNames),
		RunE: func(_ *cobra.Command, args []string) error {
			if err := filter.validate(); err != nil {
				return err
			}

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

				plan, err := scope.objects(ctx, api, ownerID, mibID, filter.query())
				if err != nil {
					return err
				}
				page := narrowedPage(plan.Objects, plan.Page,
					filter.noun, filter.search, filter.limit, filter.apply)

				if env.Opts.JSON {
					// Emit the plan with its selected objects rather than both
					// the full and the filtered set, which would only confuse
					// a consumer.
					report := objectPlanReport{MIBObjectPlan: *plan, Listing: page.counts()}
					report.Objects = page.Items
					return writeJSON(out, &report)
				}
				if err := renderObjectPlanSummary(out, plan, page.Shown); err != nil {
					return err
				}
				fmt.Fprintln(out, "\nObjects:")
				return renderObjectPlanTable(out, page)
			})
		},
	}

	filter.bind(cmd)
	return cmd
}

// renderObjectPlanSummary prints what the plan says about the module as a
// whole, which is the part an operator reads before scanning the list.
func renderObjectPlanSummary(out io.Writer, plan *client.MIBObjectPlan, matched int) error {
	pairs := [][2]string{
		{"Device", plan.Device},
		{"MIB", plan.MIB},
		{"Assignment", assignmentState(plan)},
		{"Objects", fmt.Sprintf("%d polled of %d (%d shown by filter)", plan.Polled, plan.Total, matched)},
	}
	if plan.Estimate != nil {
		pairs = append(pairs, [2]string{"Series", seriesCount(plan.Estimate)})
	}
	return keyValues(out, pairs...)
}

// assignmentState describes the rule in force and where it comes from, for
// example "enabled, filtered (from profile)". An operator about to change a
// filter needs to know whether it is shared with other devices.
func assignmentState(plan *client.MIBObjectPlan) string {
	state := "disabled"
	if plan.Assignment.Enabled {
		state = "enabled"
	}
	if filters := filterSummary(plan.Assignment); filters != "-" {
		state += ", " + filters
	}
	if plan.Source != "" {
		state += " (from " + plan.Source + ")"
	}
	return state
}

// seriesCount renders a module's projected series, marking an estimate with a
// tilde so it is not mistaken for a measurement.
func seriesCount(estimate *client.MIBSeriesEstimate) string {
	if estimate.Exact {
		return strconv.Itoa(estimate.Series)
	}
	return "~" + strconv.Itoa(estimate.Series)
}

// renderObjectPlanTable prints the per-object verdicts the filter selected,
// saying so when --limit held rows back.
func renderObjectPlanTable(out io.Writer, page listPage[client.MIBObjectDecision]) error {
	if page.writeEmpty(out, "  ", "(no matching objects)") {
		return nil
	}

	t := newTable(out, "  OID", "NAME", "TYPE", "KIND", "ACCESS", "POLLED", "SERIES", "REASON")
	for _, o := range page.Items {
		t.Row("  "+o.OID, o.Name, dash(o.Type), dash(o.Kind), dash(o.Access),
			yesNo(o.Polled), strconv.Itoa(o.Series), dash(o.Reason))
	}
	if err := t.Flush(); err != nil {
		return err
	}

	page.note(out, "  ")
	return nil
}
