package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-netmon/pkg/client"
)

// The services a working go-netmon depends on, named as an operator would name
// them in an incident channel.
const (
	managementAPIService = "management-api"
	pollerService        = "poller"
	prometheusService    = "prometheus"
)

// serviceState is the outcome of one readiness probe.
//
// Unknown is a third state on purpose: an install with no devices gives the
// poller check nothing to ask about, and calling that "down" would make a fresh
// install look broken.
type serviceState string

const (
	serviceUp      serviceState = "up"
	serviceDown    serviceState = "down"
	serviceUnknown serviceState = "unknown"
)

// healthCheck is one service's readiness and the evidence behind it.
type healthCheck struct {
	Service string       `json:"service"`
	State   serviceState `json:"state"`
	Detail  string       `json:"detail"`
}

// healthReport is the --json document, whose top-level "healthy" is the same
// answer as the command's exit status.
type healthReport struct {
	Healthy bool          `json:"healthy"`
	Checks  []healthCheck `json:"checks"`
}

// UnhealthyError reports that at least one service failed its readiness check.
// Each service's detail is already printed, so this only carries the names that
// decide the command's exit status.
type UnhealthyError struct {
	Services []string
}

func (e *UnhealthyError) Error() string {
	return "unreachable: " + strings.Join(e.Services, ", ")
}

// newHealthCommand builds "netmon health".
func newHealthCommand(env *Env) *cobra.Command {
	return &cobra.Command{
		Use:   "health",
		Short: "Check that the server, the poller and Prometheus are reachable",
		Long: "Probe the three services go-netmon needs and report each one's state.\n\n" +
			"Every probe goes through the management API, because that is the only\n" +
			"address the CLI is given: the poller is reached through the device\n" +
			"status it serves, and Prometheus through a query.\n\n" +
			"The exit status is non-zero when any service is down, so this can gate\n" +
			"a deployment or a health cron:\n" +
			"  netmon health || page-someone",
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(context.Background(), env.Opts.Timeout)
			defer cancel()

			api, err := env.Client()
			if err != nil {
				return err
			}
			checks := checkHealth(ctx, api)
			down := downServices(checks)

			if env.Opts.JSON {
				err = writeJSON(env.Out, healthReport{Healthy: len(down) == 0, Checks: checks})
			} else {
				err = renderHealth(env.Out, checks)
			}
			if err != nil {
				return err
			}

			// Report the failure after the detail, so the operator sees which
			// service is down before the line that sets the exit status.
			if len(down) > 0 {
				return &UnhealthyError{Services: down}
			}
			return nil
		},
	}
}

// checkHealth probes every service, in dependency order: nothing else can be
// judged when the management API itself is unreachable, because every other
// probe travels through it.
func checkHealth(ctx context.Context, api *client.Client) []healthCheck {
	devices, err := api.ListDevices(ctx)
	if err != nil {
		const notProbed = "not probed: the management API is unreachable"
		return []healthCheck{
			{Service: managementAPIService, State: serviceDown, Detail: err.Error()},
			{Service: pollerService, State: serviceUnknown, Detail: notProbed},
			{Service: prometheusService, State: serviceUnknown, Detail: notProbed},
		}
	}

	return []healthCheck{
		{
			Service: managementAPIService,
			State:   serviceUp,
			Detail:  fmt.Sprintf("%d device(s) configured", len(devices)),
		},
		checkPoller(ctx, api, devices),
		checkPrometheus(ctx, api),
	}
}

// checkPoller probes the poller through the only route the management API
// proxies to it: a device's polling status. That route answers 503 when the
// poller cannot be reached, which is exactly the distinction being reported.
//
// With no devices configured there is nothing to ask about, so the state is
// unknown rather than down.
func checkPoller(ctx context.Context, api *client.Client, devices []client.Device) healthCheck {
	check := healthCheck{Service: pollerService}
	if len(devices) == 0 {
		check.State = serviceUnknown
		check.Detail = "no devices configured to probe with"
		return check
	}

	device := devices[0]
	status, err := api.DeviceStatus(ctx, device.ID)
	switch {
	case err == nil:
		check.State = serviceUp
		check.Detail = "polling " + device.Name + ", last poll " + relativeTime(status.LastPollTime)
		if status.Unreachable {
			// The poller is up either way — this is about the device it is
			// pointed at — but an operator reading "last poll 14m ago" needs to
			// know it is 14m ago because the device stopped answering.
			check.Detail += fmt.Sprintf(" (%s is unreachable after %d failed polls, retrying %s)",
				device.Name, status.ConsecutiveFailures, untilPhrase(status.NextPollTime))
		}
	case pollerAnswered(err):
		// The poller replied; it simply has not picked this device up. That
		// says nothing about its health.
		check.State = serviceUp
		check.Detail = device.Name + " is not registered with the poller yet"
	default:
		check.State = serviceDown
		check.Detail = err.Error()
	}
	return check
}

// pollerAnswered reports whether a device-status failure came from the poller
// itself rather than from failing to reach it. A 404 is the poller saying it
// does not know that device.
func pollerAnswered(err error) bool {
	var apiErr *client.APIError
	return errors.As(err, &apiErr) && apiErr.NotFound()
}

// prometheusProbe is the cheapest query that proves Prometheus answered: "up"
// is the scrape-health series every Prometheus records for its own targets.
const prometheusProbe = "up"

// checkPrometheus probes Prometheus through the management API's query
// endpoint. An empty result still means Prometheus answered, so only an error
// counts as down.
func checkPrometheus(ctx context.Context, api *client.Client) healthCheck {
	check := healthCheck{Service: prometheusService}

	values, err := api.QueryInstant(ctx, prometheusProbe)
	if err != nil {
		check.State = serviceDown
		check.Detail = err.Error()
		return check
	}
	check.State = serviceUp
	check.Detail = fmt.Sprintf("%d scrape target(s) reporting", len(values))
	return check
}

// downServices names the services that failed, in the order they were probed.
func downServices(checks []healthCheck) []string {
	var down []string
	for _, c := range checks {
		if c.State == serviceDown {
			down = append(down, c.Service)
		}
	}
	return down
}

// renderHealth prints one row per service. The detail is never truncated: it
// carries the reason a service is down and what to check about it, which is the
// whole reason to run this command.
func renderHealth(out io.Writer, checks []healthCheck) error {
	t := newTable(out, "SERVICE", "STATE", "DETAIL")
	for _, c := range checks {
		t.Row(c.Service, string(c.State), dash(c.Detail))
	}
	return t.Flush()
}
