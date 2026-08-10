package cli

import (
	"context"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-netmon/internal/promname"
	"github.com/jeremyhahn/go-netmon/pkg/client"
)

// metricRanges are the query windows the server accepts. Checking the flag
// against this list turns a server-side 400 into an answer the user can act on.
var metricRanges = []string{"6h", "1d", "7d", "30d", "1y"}

// InvalidRangeError reports a --range value the server does not accept.
type InvalidRangeError struct {
	Value string
}

func (e *InvalidRangeError) Error() string {
	return "netmon: invalid --range " + strconv.Quote(e.Value) +
		"; accepted values are " + strings.Join(metricRanges, ", ")
}

// validateRange reports whether value is one of the server's query windows.
func validateRange(value string) error {
	for _, accepted := range metricRanges {
		if value == accepted {
			return nil
		}
	}
	return &InvalidRangeError{Value: value}
}

// newMetricsCommand builds the "netmon metrics" subtree.
func newMetricsCommand(env *Env) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "metrics",
		Aliases: []string{"metric"},
		Short:   "Query collected metrics",
		Long: "Read the samples the poller has stored.\n\n" +
			"\"query\" summarizes a metric over a window, \"instant\" evaluates a\n" +
			"PromQL expression against the newest samples, and \"system\" shows a\n" +
			"device's cached SNMP system group.",
	}

	cmd.AddCommand(
		newMetricsQueryCommand(env),
		newMetricsInstantCommand(env),
		newMetricsSystemCommand(env),
	)
	return cmd
}

func newMetricsQueryCommand(env *Env) *cobra.Command {
	var timeRange string
	filter := &listFilter[client.TimeSeries]{
		noun:         "series",
		fields:       "metric name or labels",
		defaultLimit: defaultSeriesLimit,
		matches: func(s client.TimeSeries, needle string) bool {
			return containsFold(needle, s.Metric, formatLabels(s.Labels))
		},
	}

	cmd := &cobra.Command{
		Use:   "query <metric>",
		Short: "Summarize a metric's series over a time window",
		Long: "Fetch a metric's stored samples and report one row per series.\n\n" +
			"--search matches a series' metric name or its rendered labels,\n" +
			"ignoring case, so \"--search Gi0/1\" narrows a metric to one\n" +
			"interface. A chatty device carries thousands of series, so the\n" +
			"listing is capped at " + strconv.Itoa(defaultSeriesLimit) + " by default.\n\n" +
			"Examples:\n" +
			"  netmon metrics query snmp_ifInOctets --range 1d\n" +
			"  netmon metrics query snmp_ifInOctets --search core-01 --limit 0",
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			// Validate before the first request so a typo fails immediately
			// instead of once per --refresh frame.
			if err := validateRange(timeRange); err != nil {
				return err
			}
			if err := filter.validate(); err != nil {
				return err
			}

			return env.runWithRefresh(func(ctx context.Context, out io.Writer) error {
				api, err := env.Client()
				if err != nil {
					return err
				}
				series, err := api.QueryRange(ctx, args[0], timeRange)
				if err != nil {
					return err
				}
				// Sort before selecting so --limit keeps the same series
				// between frames rather than whichever the server listed first.
				sortSeries(series)
				page := filter.apply(series)

				if env.Opts.JSON {
					return writeJSON(out, page)
				}
				return renderSeriesTable(out, page)
			})
		},
	}

	cmd.Flags().StringVar(&timeRange, "range", "6h",
		"query window ("+strings.Join(metricRanges, ", ")+")")
	filter.bind(cmd)
	return cmd
}

// defaultSeriesLimit caps a metric listing. One metric across one device's
// interfaces is already dozens of series, and a fleet-wide PromQL selector runs
// to thousands, so these listings are bounded by default.
const defaultSeriesLimit = 50

func renderSeriesTable(out io.Writer, page listPage[client.TimeSeries]) error {
	if page.writeEmpty(out, "",
		"No samples. The metric may not exist, or the window may predate collection.") {
		return nil
	}

	t := newTable(out, "METRIC", "LABELS", "SAMPLES", "LATEST", "TIME")
	for _, s := range page.Items {
		value, when := "-", "-"
		if latest, ok := latestSample(s.Values); ok {
			value = formatValue(latest.Value)
			when = relativeTime(latest.Timestamp)
		}
		t.Row(dash(s.Metric), dash(truncate(formatLabels(s.Labels), 60)), len(s.Values), value, when)
	}
	if err := t.Flush(); err != nil {
		return err
	}

	page.note(out, "")
	return nil
}

// latestSample returns the most recent sample. The server orders samples
// ascending, but scanning is cheap and keeps the display honest if it does not.
func latestSample(samples []client.MetricSample) (client.MetricSample, bool) {
	var latest client.MetricSample
	found := false
	for _, s := range samples {
		if !found || s.Timestamp.After(latest.Timestamp) {
			latest, found = s, true
		}
	}
	return latest, found
}

// sortSeries orders series by name and labels so a refreshing view does not
// reshuffle its rows between frames.
func sortSeries(series []client.TimeSeries) {
	sort.SliceStable(series, func(i, j int) bool {
		if series[i].Metric != series[j].Metric {
			return series[i].Metric < series[j].Metric
		}
		return formatLabels(series[i].Labels) < formatLabels(series[j].Labels)
	})
}

func newMetricsInstantCommand(env *Env) *cobra.Command {
	filter := &listFilter[client.InstantValue]{
		noun:         "series",
		fields:       "metric name or labels",
		defaultLimit: defaultSeriesLimit,
		matches: func(v client.InstantValue, needle string) bool {
			return containsFold(needle, formatSeries(v.Metric))
		},
	}

	cmd := &cobra.Command{
		Use:     "instant <promql>",
		Aliases: []string{"now"},
		Short:   "Evaluate a PromQL expression against the newest samples",
		Long: "Run an instant query and print the current value of every series\n" +
			"it selects.\n\n" +
			"--search matches a series' rendered name and labels, ignoring case.\n" +
			"It narrows what came back rather than changing the query, so it is\n" +
			"the quick way to pick one interface out of a broad selector.\n\n" +
			"Examples:\n" +
			"  netmon metrics instant '{device=\"core-01\"}'\n" +
			"  netmon metrics instant '{device=\"core-01\"}' --search ifInOctets",
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if err := filter.validate(); err != nil {
				return err
			}

			return env.runWithRefresh(func(ctx context.Context, out io.Writer) error {
				api, err := env.Client()
				if err != nil {
					return err
				}
				values, err := api.QueryInstant(ctx, args[0])
				if err != nil {
					return err
				}
				// Sort before selecting so --limit keeps the same series
				// between frames rather than whichever the server listed first.
				sortInstantValues(values)
				page := filter.apply(values)

				if env.Opts.JSON {
					return writeJSON(out, page)
				}
				return renderInstantTable(out, page)
			})
		},
	}

	filter.bind(cmd)
	return cmd
}

func renderInstantTable(out io.Writer, page listPage[client.InstantValue]) error {
	if page.writeEmpty(out, "", "No series matched.") {
		return nil
	}

	t := newTable(out, "LABELS", "VALUE", "TIME")
	for _, v := range page.Items {
		t.Row(dash(truncate(formatSeries(v.Metric), 80)), formatValue(v.Value), relativeTime(v.Time))
	}
	if err := t.Flush(); err != nil {
		return err
	}

	page.note(out, "")
	return nil
}

// sortInstantValues orders values by their rendered series, which is what the
// reader scans, and keeps a refreshing view stable.
func sortInstantValues(values []client.InstantValue) {
	sort.SliceStable(values, func(i, j int) bool {
		return formatSeries(values[i].Metric) < formatSeries(values[j].Metric)
	})
}

func newMetricsSystemCommand(env *Env) *cobra.Command {
	var fields []string

	cmd := &cobra.Command{
		Use:     "system <device>",
		Aliases: []string{"sysinfo"},
		Short:   "Show a device's cached SNMP system group",
		Long: "Print the sysName, sysDescr, sysContact and sysLocation values the\n" +
			"poller last collected, without touching the device itself.\n\n" +
			"Example:\n" +
			"  netmon metrics system core-01 --fields sys_name,sys_location",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: env.completeNames(deviceNames),
		RunE: func(_ *cobra.Command, args []string) error {
			return env.runWithRefresh(func(ctx context.Context, out io.Writer) error {
				api, err := env.Client()
				if err != nil {
					return err
				}

				// System info is keyed by the name the poller labels samples
				// with, so resolve the reference to the stored device first: a
				// numeric ID or a differently-cased name would otherwise match
				// nothing and look like missing data.
				id, err := api.ResolveDevice(ctx, args[0])
				if err != nil {
					return err
				}
				device, err := api.GetDevice(ctx, id)
				if err != nil {
					return err
				}

				info, err := api.SystemInfo(ctx, device.Name, fields)
				if err != nil {
					return err
				}
				if env.Opts.JSON {
					return writeJSON(out, info)
				}
				return renderSystemInfo(out, device.Name, info.Values)
			})
		},
	}

	cmd.Flags().StringSliceVar(&fields, "fields", nil,
		"limit output to these fields, e.g. sys_name,sys_descr (default: all)")
	return cmd
}

// systemInfoFields is the display order of the SNMP system group. The server
// returns a map, and alphabetical order would bury the name under the
// description.
var systemInfoFields = []string{
	"sys_name", "sys_descr", "sys_object_id", "sys_contact", "sys_location",
}

func renderSystemInfo(out io.Writer, device string, info map[string]string) error {
	if len(info) == 0 {
		fmt.Fprintf(out, "No system information cached for %s yet.\n", device)
		return nil
	}

	pairs := make([][2]string, 0, len(info)+1)
	pairs = append(pairs, [2]string{"Device", device})
	for _, field := range orderSystemFields(info) {
		// Field names are printed verbatim because they are also what --fields
		// accepts.
		pairs = append(pairs, [2]string{field, dash(truncate(info[field], 100))})
	}
	return keyValues(out, pairs...)
}

// orderSystemFields lists the well-known fields first, then anything else the
// server returned, so an added field still shows up.
func orderSystemFields(info map[string]string) []string {
	ordered := make([]string, 0, len(info))
	known := make(map[string]bool, len(systemInfoFields))

	for _, field := range systemInfoFields {
		if _, ok := info[field]; ok {
			ordered = append(ordered, field)
			known[field] = true
		}
	}

	rest := make([]string, 0, len(info))
	for field := range info {
		if !known[field] {
			rest = append(rest, field)
		}
	}
	sort.Strings(rest)
	return append(ordered, rest...)
}

// seriesNameLabel is the label Prometheus keeps a metric's name in.
const seriesNameLabel = promname.NameLabel

// formatSeries renders a label set the way PromQL writes it, as
// name{key="value",...}.
func formatSeries(labels map[string]string) string {
	name := labels[seriesNameLabel]
	inner := formatLabels(labels)
	if inner == "" {
		return name
	}
	return name + "{" + inner + "}"
}

// formatLabels renders a label set as sorted key="value" pairs, omitting the
// metric name because callers show it separately. Sorting keeps successive
// frames of a refreshing view identical.
func formatLabels(labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		if k == seriesNameLabel {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)

	pairs := make([]string, 0, len(keys))
	for _, k := range keys {
		pairs = append(pairs, k+"="+strconv.Quote(labels[k]))
	}
	return strings.Join(pairs, ",")
}

// formatValue renders a sample value for a table cell. Counters are whole
// numbers and are much easier to read without a trailing ".000", while gauges
// keep enough precision to be meaningful.
func formatValue(v float64) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return strconv.FormatFloat(v, 'g', -1, 64)
	}
	if v == math.Trunc(v) && math.Abs(v) < 1e15 {
		return strconv.FormatFloat(v, 'f', 0, 64)
	}
	return strconv.FormatFloat(v, 'f', 3, 64)
}
