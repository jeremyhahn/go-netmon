package cli

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-netmon/internal/promname"
	"github.com/jeremyhahn/go-netmon/pkg/client"
)

// deviceLabel is the label the exporter attaches the device name to on every
// sample it writes, and the label the web UI queries by
// (internal/webui/api/metrics.go).
const deviceLabel = promname.DeviceLabel

// defaultMonitorTop bounds the detail view. A chatty device exposes thousands
// of series and a screenful is what an operator can actually read.
const defaultMonitorTop = 20

// newMonitorCommand builds the "netmon monitor" live view.
func newMonitorCommand(env *Env) *cobra.Command {
	var (
		top int
		raw bool
	)

	cmd := &cobra.Command{
		Use:     "monitor [device]",
		Aliases: []string{"watch"},
		Short:   "Live view of the fleet, or of one device",
		Long: "Show polling health at a glance.\n\n" +
			"With no argument, print one row per configured device. With a device,\n" +
			"print its poller status followed by its current metric values, named\n" +
			"through the device's MIBs — \"ifInOctets[10]\" rather than the OID the\n" +
			"samples are stored under.\n\n" +
			"This is the command --refresh exists for:\n" +
			"  netmon monitor --refresh 2\n" +
			"  netmon monitor core-01 --refresh 2 --top 40",
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: env.completeNames(deviceNames),
		RunE: func(_ *cobra.Command, args []string) error {
			return env.runWithRefresh(func(ctx context.Context, out io.Writer) error {
				api, err := env.Client()
				if err != nil {
					return err
				}
				if len(args) == 1 {
					return env.monitorDevice(ctx, out, api, args[0], top, raw)
				}
				return env.monitorFleet(ctx, out, api)
			})
		},
	}

	cmd.Flags().IntVar(&top, "top", defaultMonitorTop,
		"show at most N series in the device view; 0 shows all")
	cmd.Flags().BoolVar(&raw, "raw", false,
		"show raw metric names and labels instead of resolving them through the device's MIBs")
	return cmd
}

// monitorFleet renders one row per configured device.
func (e *Env) monitorFleet(ctx context.Context, out io.Writer, api *client.Client) error {
	statuses, err := collectStatuses(ctx, api)
	if err != nil {
		return err
	}
	if e.Opts.JSON {
		return writeJSON(out, statuses)
	}
	return renderFleet(out, statuses)
}

// collectStatuses pairs every configured device with the poller's view of it.
// A device the poller has not picked up yet still gets a row carrying the
// lookup error, so one unknown device cannot blank the whole view.
func collectStatuses(ctx context.Context, api *client.Client) ([]client.DeviceStatus, error) {
	devices, err := api.ListDevices(ctx)
	if err != nil {
		return nil, err
	}

	statuses := make([]client.DeviceStatus, 0, len(devices))
	for _, d := range devices {
		status, statusErr := api.DeviceStatus(ctx, d.ID)
		if statusErr != nil {
			statuses = append(statuses, client.DeviceStatus{
				Name: d.Name, Address: d.Address, LastError: statusErr.Error(),
			})
			continue
		}
		// The poller does not know the address; carry it over from the
		// device record so the table can show it.
		status.Address = d.Address
		statuses = append(statuses, *status)
	}
	return statuses, nil
}

func renderFleet(out io.Writer, statuses []client.DeviceStatus) error {
	// The fleet view is bounded by the device count and is the whole point of
	// --refresh, so it is never capped; "device status" is the same listing
	// with --search and --limit for a fleet that has outgrown a screen.
	if len(statuses) == 0 {
		return renderStatusTable(out, wholeList(statuses, "devices"))
	}

	// A one-line summary answers "is anything broken?" without reading every
	// row, which is the point of a view that redraws.
	running, failing := 0, 0
	for _, s := range statuses {
		if s.Connected {
			running++
		}
		if s.LastError != "" {
			failing++
		}
	}
	fmt.Fprintf(out, "%d devices — %d polling, %d with errors\n\n",
		len(statuses), running, failing)

	return renderStatusTable(out, wholeList(statuses, "devices"))
}

// monitorDevice renders one device's poller status and its current samples.
func (e *Env) monitorDevice(
	ctx context.Context,
	out io.Writer,
	api *client.Client,
	ref string,
	top int,
	raw bool,
) error {
	id, err := api.ResolveDevice(ctx, ref)
	if err != nil {
		return err
	}
	device, err := api.GetDevice(ctx, id)
	if err != nil {
		return err
	}

	// The poller may not have picked the device up yet. That belongs in the
	// header rather than aborting a view whose metrics are still useful.
	status, statusErr := api.DeviceStatus(ctx, id)

	values, err := api.QueryInstant(ctx, deviceSeriesQuery(device.Name))
	if err != nil {
		return err
	}

	if e.Opts.JSON {
		return writeJSON(out, map[string]any{
			"device": device,
			"status": status,
			"series": values,
		})
	}
	if err := renderMonitorHeader(out, device, status, statusErr); err != nil {
		return err
	}

	// Naming the series costs a request per MIB module, so it is only worth
	// doing for the table that is about to be printed — never for --json,
	// which returns above with the series untouched, nor for --raw.
	var index *oidIndex
	if !raw {
		index = e.deviceOIDIndex(ctx, api, id)
	}
	return renderMonitorSeries(out, values, top, index)
}

// snmpMetricPrefix is the prefix the exporter gives every metric derived from
// an SNMP object. go-netmon's own operational metrics — poll counts, durations
// — also carry a device label, so selecting on that label alone would bury the
// device's data under the poller's telemetry.
const snmpMetricPrefix = promname.Prefix

// deviceSeriesQuery selects the SNMP series the exporter emits for a device.
// The samples carry the device's configured name, not its ID, which is why the
// caller must pass the stored name rather than whatever the user typed.
func deviceSeriesQuery(name string) string {
	return "{" + deviceLabel + "=" + strconv.Quote(name) +
		`,__name__=~"` + snmpMetricPrefix + `.*"}`
}

func renderMonitorHeader(
	out io.Writer,
	device *client.Device,
	status *client.DeviceStatus,
	statusErr error,
) error {
	pairs := [][2]string{
		{"Device", device.Name},
		{"Address", fmt.Sprintf("%s:%d", device.Address, device.Port)},
	}

	if statusErr != nil {
		pairs = append(pairs, [2]string{"Status", "unavailable: " + statusErr.Error()})
	} else {
		pairs = append(pairs,
			[2]string{"Connected", yesNo(status.Connected)},
			[2]string{"Last poll", relativeTime(status.LastPollTime)},
			[2]string{"Last poll ok", yesNo(status.LastPollSuccess)},
			[2]string{"Metrics collected", fmt.Sprint(status.MetricsCollected)},
			[2]string{"Poll interval", dash(status.PollInterval)},
			[2]string{"Last error", dash(truncate(status.LastError, 80))},
		)
		// Only for a device that has stopped answering entirely. A device that
		// answers some objects and errors on others has a last error and is
		// still healthy, and saying otherwise would send someone to the wrong
		// rack.
		if status.Unreachable {
			pairs = append(pairs,
				[2]string{"Unreachable", fmt.Sprintf("yes, after %d failed polls", status.ConsecutiveFailures)},
				[2]string{"Next poll", untilTime(status.NextPollTime)},
			)
		}
	}
	return keyValues(out, pairs...)
}

// renderMonitorSeries prints a device's current values, naming each series
// through index. A nil index — what --raw asks for — leaves every series under
// the metric name and labels the samples are actually stored with.
func renderMonitorSeries(
	out io.Writer,
	values []client.InstantValue,
	top int,
	index *oidIndex,
) error {
	fmt.Fprintf(out, "\nCurrent values (%d series):\n", len(values))
	if len(values) == 0 {
		fmt.Fprintln(out, "  none — the device may not have completed a poll yet")
		return nil
	}

	// Resolve before sorting so the rows are ordered by the name the reader
	// sees, and sort before truncating so --top keeps the same series between
	// frames instead of whichever ones the map iteration happened to yield.
	rows := resolveMonitorSeries(values, index)

	hidden := 0
	if top > 0 && len(rows) > top {
		hidden = len(rows) - top
		rows = rows[:top]
	}

	nameHeader := "NAME"
	if index == nil {
		nameHeader = "LABELS"
	}
	t := newTable(out, nameHeader, "VALUE", "TIME")
	for _, row := range rows {
		t.Row(dash(truncate(row.name, 80)), dash(row.value), relativeTime(row.time))
	}
	if err := t.Flush(); err != nil {
		return err
	}

	if hidden > 0 {
		fmt.Fprintf(out, "\n%d more series hidden; raise --top to see them\n", hidden)
	}
	return nil
}

// monitorRow is one rendered line of the device detail view.
type monitorRow struct {
	name  string
	value string
	time  time.Time
}

// resolveMonitorSeries names and formats every series, ordered by the name it
// will be shown under.
//
// A series no loaded MIB accounts for keeps its full PromQL identity — name
// and labels — because that is the only thing left that identifies it, and
// because the OID must stay readable when the MIBs are incomplete.
func resolveMonitorSeries(values []client.InstantValue, index *oidIndex) []monitorRow {
	rows := make([]monitorRow, 0, len(values))
	for _, v := range values {
		name, meta, _ := index.Describe(v.Metric)
		rows = append(rows, monitorRow{
			name:  name,
			value: formatSeriesValue(meta, v.Value),
			time:  v.Time,
		})
	}

	sort.SliceStable(rows, func(i, j int) bool { return rows[i].name < rows[j].name })
	return rows
}
