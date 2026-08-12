package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-netmon/pkg/client"
)

// newDeviceCommand builds the "netmon device" subtree.
func newDeviceCommand(env *Env) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "device",
		Aliases: []string{"devices", "dev"},
		Short:   "Manage monitored devices",
		Long: "Add, inspect and remove the SNMP devices go-netmon polls.\n\n" +
			"Commands that take a device accept either its name or its numeric ID.",
	}

	cmd.AddCommand(
		newDeviceListCommand(env),
		newDeviceShowCommand(env),
		newDeviceAddCommand(env),
		newDeviceUpdateCommand(env),
		newDeviceDeleteCommand(env),
		newDeviceTestCommand(env),
		newDeviceStatusCommand(env),
		newDeviceMIBCommand(env),
		newDeviceProfileCommand(env),
	)
	return cmd
}

func newDeviceListCommand(env *Env) *cobra.Command {
	filter := newDeviceListFilter()

	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List configured devices",
		Long: "List the devices go-netmon polls.\n\n" +
			"--search matches a device's name or address, ignoring case.\n\n" +
			"Example:\n" +
			"  netmon device list --search core",
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if err := filter.validate(); err != nil {
				return err
			}

			return env.runWithRefresh(func(ctx context.Context, out io.Writer) error {
				api, err := env.Client()
				if err != nil {
					return err
				}
				devices, err := api.ListDevices(ctx)
				if err != nil {
					return err
				}
				page := filter.apply(devices)

				if env.Opts.JSON {
					return writeJSON(out, page)
				}
				return renderDeviceTable(out, page)
			})
		},
	}

	filter.bind(cmd)
	return cmd
}

// newDeviceListFilter builds the search-and-cap filter for a device listing.
//
// It is uncapped by default. A device list is the inventory an operator built
// by hand, and it is read to confirm what is there; hiding part of it would
// cost more than the flood a cap prevents. --limit is there for the fleet that
// outgrows a screen.
func newDeviceListFilter() *listFilter[client.Device] {
	return &listFilter[client.Device]{
		noun:   "devices",
		fields: "name or address",
		matches: func(d client.Device, needle string) bool {
			return containsFold(needle, d.Name, d.Address)
		},
	}
}

func renderDeviceTable(out io.Writer, page listPage[client.Device]) error {
	if page.writeEmpty(out, "", "No devices configured. Add one with: netmon device add") {
		return nil
	}

	t := newTable(out, "ID", "NAME", "ADDRESS", "PORT", "VERSION", "INTERVAL", "ADDED")
	for _, d := range page.Items {
		t.Row(d.ID, d.Name, d.Address, d.Port, d.SNMPVersion,
			describePollInterval(d), relativeTime(d.CreatedAt))
	}
	if err := t.Flush(); err != nil {
		return err
	}

	page.note(out, "")
	return nil
}

func newDeviceShowCommand(env *Env) *cobra.Command {
	return &cobra.Command{
		Use:               "show <device>",
		Short:             "Show a device's configuration, MIBs and profiles",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: env.completeNames(deviceNames),
		RunE: func(_ *cobra.Command, args []string) error {
			return env.runWithRefresh(func(ctx context.Context, out io.Writer) error {
				api, err := env.Client()
				if err != nil {
					return err
				}
				id, err := api.ResolveDevice(ctx, args[0])
				if err != nil {
					return err
				}

				device, err := api.GetDevice(ctx, id)
				if err != nil {
					return err
				}
				// A device is only meaningful alongside what it polls, so
				// gather MIBs and profiles rather than making the user run
				// three commands.
				mibs, mibErr := api.DeviceMIBs(ctx, id)
				profiles, profErr := api.DeviceProfiles(ctx, id)

				if env.Opts.JSON {
					return writeJSON(out, map[string]any{
						"device":   device,
						"mibs":     mibs,
						"profiles": profiles,
					})
				}
				return renderDeviceDetail(out, device, mibs, mibErr, profiles, profErr)
			})
		},
	}
}

func renderDeviceDetail(
	out io.Writer,
	device *client.Device,
	mibs *client.DeviceMIBs, mibErr error,
	profiles []client.Profile, profErr error,
) error {
	err := keyValues(out,
		[2]string{"ID", fmt.Sprint(device.ID)},
		[2]string{"Name", device.Name},
		[2]string{"Address", device.Address},
		[2]string{"Port", fmt.Sprint(device.Port)},
		[2]string{"SNMP version", device.SNMPVersion},
		[2]string{"Community", dash(device.Community)},
		[2]string{"Poll interval", describePollInterval(*device)},
		[2]string{"Added", device.CreatedAt.Format("2006-01-02 15:04:05")},
	)
	if err != nil {
		return err
	}

	for _, warning := range device.Warnings {
		fmt.Fprintf(out, "\nWarning: %s\n", warning)
	}

	fmt.Fprintln(out, "\nProfiles:")
	switch {
	case profErr != nil:
		fmt.Fprintf(out, "  unavailable: %v\n", profErr)
	case len(profiles) == 0:
		fmt.Fprintln(out, "  (none)")
	default:
		for _, p := range profiles {
			fmt.Fprintf(out, "  %d  %s\n", p.ID, p.Name)
		}
	}

	fmt.Fprintln(out, "\nMIBs:")
	switch {
	case mibErr != nil:
		fmt.Fprintf(out, "  unavailable: %v\n", mibErr)
	case mibs == nil || len(mibs.AllMIBs) == 0:
		fmt.Fprintln(out, "  (none)")
	default:
		return renderDeviceMIBTable(out, wholeList(mibs.AllMIBs, "MIBs"), mibs.DirectMIBs, "  ")
	}
	return nil
}

// renderDeviceMIBTable lists a device's MIBs with where each one comes from and
// how it is polled. indent prefixes the first column so the same table can sit
// inside a detail view.
//
// The polling state comes from the listing itself, so showing it costs no extra
// request per module.
func renderDeviceMIBTable(
	out io.Writer,
	page listPage[client.AssignedMIB],
	directMIBs []client.AssignedMIB,
	indent string,
) error {
	if page.writeEmpty(out, indent, "No MIBs assigned.") {
		return nil
	}

	// Membership is decided against the device's whole direct set, not the
	// filtered view, so a search never mislabels a direct MIB as inherited.
	direct := make(map[int64]bool, len(directMIBs))
	for _, m := range directMIBs {
		direct[m.ID] = true
	}

	t := newTable(out, indent+"ID", "NAME", "OBJECTS", "SOURCE", "ENABLED", "FILTERS")
	for _, m := range page.Items {
		source := "profile"
		if direct[m.ID] {
			source = "direct"
		}
		t.Row(indent+fmt.Sprint(m.ID), m.Name, m.ObjectCount, source,
			yesNo(m.Assignment.Enabled), filterSummary(m.Assignment))
	}
	if err := t.Flush(); err != nil {
		return err
	}

	page.note(out, indent)
	return nil
}

func newDeviceAddCommand(env *Env) *cobra.Command {
	var (
		device       client.Device
		pollInterval int
	)

	cmd := &cobra.Command{
		Use:   "add",
		Short: "Add a device to monitor",
		Long: "Register a device with the poller.\n\n" +
			"A device with no --poll-interval is polled at the server's global\n" +
			"default and follows it if it changes.\n\n" +
			"Example:\n" +
			"  netmon device add --name core-01 --address 198.51.100.252 \\\n" +
			"      --version 2c --community public --poll-interval 60",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(context.Background(), env.Opts.Timeout)
			defer cancel()

			readV3Passwords(cmd, &device)
			applyPollInterval(cmd, &device, pollInterval)

			api, err := env.Client()
			if err != nil {
				return err
			}
			created, err := api.CreateDevice(ctx, device)
			if err != nil {
				return err
			}
			// A later resolve in this process must see the new name.
			api.InvalidateDevices()

			if env.Opts.JSON {
				return writeJSON(env.Out, created)
			}
			fmt.Fprintf(env.Out, "Added device %s (id %d) at %s, polled every %s\n",
				created.Name, created.ID, created.Address, describePollInterval(*created))
			writeDeviceWarnings(env.Out, created)
			return nil
		},
	}

	bindDeviceFlags(cmd, &device, &pollInterval)
	mustMarkRequired(cmd, "name", "address")
	return cmd
}

func newDeviceUpdateCommand(env *Env) *cobra.Command {
	var (
		device       client.Device
		pollInterval int
	)

	cmd := &cobra.Command{
		Use:   "update <device>",
		Short: "Update a device's configuration",
		Long: "Change a device's settings. Only the flags you pass are changed;\n" +
			"everything else keeps its current value.\n\n" +
			"--poll-interval 0 clears a device's own schedule so that it follows\n" +
			"the server's global default again.",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: env.completeNames(deviceNames),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(context.Background(), env.Opts.Timeout)
			defer cancel()

			api, err := env.Client()
			if err != nil {
				return err
			}
			id, err := api.ResolveDevice(ctx, args[0])
			if err != nil {
				return err
			}

			// The API replaces the whole record, so start from the stored
			// device and overlay only the flags the user actually set.
			current, err := api.GetDevice(ctx, id)
			if err != nil {
				return err
			}
			readV3Passwords(cmd, &device)
			applyDeviceFlagOverrides(cmd, current, device)
			applyPollInterval(cmd, current, pollInterval)

			updated, err := api.UpdateDevice(ctx, id, *current)
			if err != nil {
				return err
			}
			// An update may have renamed the device.
			api.InvalidateDevices()

			if env.Opts.JSON {
				return writeJSON(env.Out, updated)
			}
			fmt.Fprintf(env.Out, "Updated device %s (id %d), polled every %s\n",
				updated.Name, updated.ID, describePollInterval(*updated))
			writeDeviceWarnings(env.Out, updated)
			return nil
		},
	}

	bindDeviceFlags(cmd, &device, &pollInterval)
	return cmd
}

// bindDeviceFlags declares the flags that describe a device.
//
// --poll-interval is bound to its own variable rather than to the device,
// because the device carries the interval as an optional value and 0 on the
// command line is a request to clear it, not the number zero. applyPollInterval
// does that translation.
func bindDeviceFlags(cmd *cobra.Command, device *client.Device, pollInterval *int) {
	f := cmd.Flags()
	f.StringVar(&device.Name, "name", "", "device name (unique)")
	f.StringVar(&device.Address, "address", "", "hostname or IP address")
	f.StringVar(&device.SNMPVersion, "version", "2c", "SNMP version (1, 2c or 3)")
	f.StringVar(&device.Community, "community", "public", "SNMP v1/v2c community string")

	// SNMPv3. A passphrase on a command line is visible in the shell history
	// and to anyone reading the process list, so both accept an environment
	// variable instead - see readV3Passwords.
	f.StringVar(&device.Username, "username", "", "SNMPv3 user name")
	f.StringVar(&device.AuthProtocol, "auth-protocol", "", "SNMPv3 authentication protocol (SHA or MD5)")
	f.StringVar(&device.AuthPassword, "auth-password", "",
		"SNMPv3 authentication passphrase (prefer NETMON_AUTH_PASSWORD)")
	f.StringVar(&device.PrivProtocol, "priv-protocol", "", "SNMPv3 privacy protocol (AES or DES)")
	f.StringVar(&device.PrivPassword, "priv-password", "",
		"SNMPv3 privacy passphrase (prefer NETMON_PRIV_PASSWORD)")
	f.IntVar(&device.Port, "port", 161, "SNMP port")
	f.IntVar(pollInterval, "poll-interval", 0,
		"seconds between polls of this device; 0 uses the server's global default")
}

// readV3Passwords fills the v3 passphrases from the environment when the flags
// were not given.
//
// A passphrase passed as a flag is in the shell's history and in the process
// list for anything on the host to read. The environment is not perfect either,
// but it keeps the secret off both.
func readV3Passwords(cmd *cobra.Command, device *client.Device) {
	if !cmd.Flags().Changed("auth-password") {
		if value := os.Getenv("NETMON_AUTH_PASSWORD"); value != "" {
			device.AuthPassword = value
		}
	}
	if !cmd.Flags().Changed("priv-password") {
		if value := os.Getenv("NETMON_PRIV_PASSWORD"); value != "" {
			device.PrivPassword = value
		}
	}
}

// applyPollInterval puts an explicitly-given --poll-interval onto a device.
//
// A flag that was not given leaves the device alone, so "update --name x" does
// not silently reset a schedule. 0 clears the device's own interval, which is
// how an operator hands it back to the global default — the same reading
// --limit 0 has elsewhere in this CLI.
func applyPollInterval(cmd *cobra.Command, device *client.Device, seconds int) {
	if !cmd.Flags().Changed("poll-interval") {
		return
	}
	if seconds == 0 {
		client.InheritPollInterval(device)
		return
	}
	client.SetPollInterval(device, seconds)
}

// applyDeviceFlagOverrides copies only explicitly-set flags onto current.
func applyDeviceFlagOverrides(cmd *cobra.Command, current *client.Device, flags client.Device) {
	if cmd.Flags().Changed("name") {
		current.Name = flags.Name
	}
	if cmd.Flags().Changed("address") {
		current.Address = flags.Address
	}
	if cmd.Flags().Changed("version") {
		current.SNMPVersion = flags.SNMPVersion
	}
	if cmd.Flags().Changed("community") {
		current.Community = flags.Community
	}
	if cmd.Flags().Changed("port") {
		current.Port = flags.Port
	}

	if cmd.Flags().Changed("username") {
		current.Username = flags.Username
	}
	if cmd.Flags().Changed("auth-protocol") {
		current.AuthProtocol = flags.AuthProtocol
	}
	if cmd.Flags().Changed("priv-protocol") {
		current.PrivProtocol = flags.PrivProtocol
	}

	// A passphrase can arrive from the environment as well as from a flag, so
	// these are overlaid on whether a value is present rather than on whether
	// the flag was given. A passphrase left out is left alone, which is what
	// makes "update --address x" on a v3 device keep working.
	if flags.AuthPassword != "" {
		current.AuthPassword = flags.AuthPassword
	}
	if flags.PrivPassword != "" {
		current.PrivPassword = flags.PrivPassword
	}
}

// describePollInterval renders how often a device is polled.
//
// A device with no interval of its own is labeled as inheriting and still
// shows the number, because "default" alone does not tell an operator whether
// the device is polled every 30 seconds or every 10 minutes.
func describePollInterval(d client.Device) string {
	effective := d.EffectivePollInterval
	seconds, own := client.PollIntervalOf(d)
	if own {
		return formatInterval(seconds)
	}
	if effective <= 0 {
		return "default"
	}
	return formatInterval(effective) + " (default)"
}

// writeDeviceWarnings prints what the server accepted but flagged.
func writeDeviceWarnings(out io.Writer, d *client.Device) {
	for _, warning := range d.Warnings {
		fmt.Fprintf(out, "Warning: %s\n", warning)
	}
}

func newDeviceDeleteCommand(env *Env) *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:               "delete <device>",
		Aliases:           []string{"rm", "remove"},
		Short:             "Remove a device",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: env.completeNames(deviceNames),
		RunE: func(_ *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(context.Background(), env.Opts.Timeout)
			defer cancel()

			api, err := env.Client()
			if err != nil {
				return err
			}
			id, err := api.ResolveDevice(ctx, args[0])
			if err != nil {
				return err
			}
			device, err := api.GetDevice(ctx, id)
			if err != nil {
				return err
			}

			// Deleting stops collection and drops the device's assignments, so
			// require an explicit confirmation unless the caller opted out.
			if !force {
				return fmt.Errorf(
					"refusing to delete device %s (id %d) without --force", device.Name, device.ID)
			}
			if err := api.DeleteDevice(ctx, id); err != nil {
				return err
			}
			// The name is gone; a later resolve must not answer from memory.
			api.InvalidateDevices()

			if env.Opts.JSON {
				return writeJSON(env.Out, changeReport{
					Action: "deleted", Kind: "device", Ref: device.Name, ID: device.ID,
				})
			}
			fmt.Fprintf(env.Out, "Deleted device %s (id %d)\n", device.Name, device.ID)
			return nil
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "confirm removal")
	return cmd
}

func newDeviceTestCommand(env *Env) *cobra.Command {
	var (
		address      string
		version      string
		community    string
		port         int
		timeout      int
		username     string
		authProtocol string
		authPassword string
		privProtocol string
		privPassword string
	)

	cmd := &cobra.Command{
		Use:   "test [device]",
		Short: "Test SNMP connectivity",
		Long: "Probe a device's SNMP agent and print its system group.\n\n" +
			"Pass a configured device to test it with its stored credentials,\n" +
			"or use --address to probe an address that is not yet configured.",
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: env.completeNames(deviceNames),
		RunE: func(_ *cobra.Command, args []string) error {
			if len(args) == 0 && address == "" {
				return fmt.Errorf("specify a configured device or --address")
			}

			return env.runWithRefresh(func(ctx context.Context, out io.Writer) error {
				api, err := env.Client()
				if err != nil {
					return err
				}

				if authPassword == "" {
					authPassword = os.Getenv("NETMON_AUTH_PASSWORD")
				}
				if privPassword == "" {
					privPassword = os.Getenv("NETMON_PRIV_PASSWORD")
				}

				var result *client.SNMPTestResponse
				if len(args) == 1 {
					id, resolveErr := api.ResolveDevice(ctx, args[0])
					if resolveErr != nil {
						return resolveErr
					}
					result, err = api.TestDeviceSNMP(ctx, id)
				} else {
					result, err = api.TestSNMP(ctx, client.SNMPTestRequest{
						Address:      address,
						SNMPVersion:  version,
						Community:    community,
						Port:         port,
						Timeout:      timeout,
						Username:     username,
						AuthProtocol: authProtocol,
						AuthPassword: authPassword,
						PrivProtocol: privProtocol,
						PrivPassword: privPassword,
					})
				}
				if err != nil {
					return err
				}

				if env.Opts.JSON {
					return writeJSON(out, result)
				}
				return renderSNMPTest(out, result)
			})
		},
	}

	f := cmd.Flags()
	f.StringVar(&address, "address", "", "probe this address instead of a configured device")
	f.StringVar(&version, "version", "2c", "SNMP version for --address (1, 2c or 3)")
	f.StringVar(&community, "community", "public", "community string for --address")
	f.IntVar(&port, "port", 161, "SNMP port for --address")
	f.IntVar(&timeout, "snmp-timeout", 10, "SNMP timeout in seconds")

	// Testing an address is how v3 credentials get checked before they are
	// stored on a device, so the same five settings are available here.
	f.StringVar(&username, "username", "", "SNMPv3 user name for --address")
	f.StringVar(&authProtocol, "auth-protocol", "", "SNMPv3 authentication protocol (SHA, MD5, or empty for none)")
	f.StringVar(&authPassword, "auth-password", "",
		"SNMPv3 authentication passphrase (prefer NETMON_AUTH_PASSWORD)")
	f.StringVar(&privProtocol, "priv-protocol", "", "SNMPv3 privacy protocol (AES, DES, or empty for none)")
	f.StringVar(&privPassword, "priv-password", "",
		"SNMPv3 privacy passphrase (prefer NETMON_PRIV_PASSWORD)")
	return cmd
}

func renderSNMPTest(out io.Writer, r *client.SNMPTestResponse) error {
	if !r.Success {
		fmt.Fprintf(out, "FAILED: %s\n", dash(firstNonEmpty(r.Error, r.Message)))
		return nil
	}

	fmt.Fprintf(out, "OK (%s)\n\n", r.Duration)
	if err := keyValues(out,
		[2]string{"System name", dash(r.SysName)},
		[2]string{"Description", dash(truncate(r.SysDescr, 100))},
		[2]string{"Uptime", dash(r.SysUpTime)},
		[2]string{"Contact", dash(r.SysContact)},
		[2]string{"Location", dash(r.SysLocation)},
	); err != nil {
		return err
	}

	if len(r.Results) > 0 {
		fmt.Fprintln(out, "\nValues:")
		t := newTable(out, "  OID", "NAME", "TYPE", "VALUE")
		for _, v := range r.Results {
			t.Row("  "+v.OID, v.Name, v.Type, truncate(v.Value, 60))
		}
		return t.Flush()
	}
	return nil
}

func newDeviceStatusCommand(env *Env) *cobra.Command {
	filter := &listFilter[client.DeviceStatus]{
		noun:   "devices",
		fields: "name, address or last error",
		matches: func(s client.DeviceStatus, needle string) bool {
			return containsFold(needle, s.Name, s.Address, s.LastError)
		},
	}

	cmd := &cobra.Command{
		Use:   "status [device]",
		Short: "Show polling status",
		Long: "Report what the poller knows about a device: whether it is running,\n" +
			"when it last polled, and any error it hit. With no argument, show\n" +
			"every configured device.\n\n" +
			"--search and --limit narrow the all-devices listing; --search\n" +
			"matches a device's name, address or last error, ignoring case, so\n" +
			"\"--search timeout\" finds the devices failing the same way.",
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: env.completeNames(deviceNames),
		RunE: func(_ *cobra.Command, args []string) error {
			if err := filter.validate(); err != nil {
				return err
			}

			return env.runWithRefresh(func(ctx context.Context, out io.Writer) error {
				api, err := env.Client()
				if err != nil {
					return err
				}

				if len(args) == 1 {
					id, resolveErr := api.ResolveDevice(ctx, args[0])
					if resolveErr != nil {
						return resolveErr
					}
					status, statusErr := api.DeviceStatus(ctx, id)
					if statusErr != nil {
						return statusErr
					}
					// The poller's status carries no address, so fill it in
					// from the device record for consistency with the
					// all-devices listing.
					if device, deviceErr := api.GetDevice(ctx, id); deviceErr == nil {
						status.Address = device.Address
					}
					// One named device is not a listing, so the filter does
					// not apply and the document keeps its existing shape.
					if env.Opts.JSON {
						return writeJSON(out, status)
					}
					return renderStatusTable(out, wholeList([]client.DeviceStatus{*status}, "devices"))
				}

				statuses, err := collectStatuses(ctx, api)
				if err != nil {
					return err
				}
				page := filter.apply(statuses)

				if env.Opts.JSON {
					return writeJSON(out, page)
				}
				return renderStatusTable(out, page)
			})
		},
	}

	filter.bind(cmd)
	return cmd
}

func renderStatusTable(out io.Writer, page listPage[client.DeviceStatus]) error {
	if page.writeEmpty(out, "", "No devices configured.") {
		return nil
	}

	t := newTable(out, "NAME", "ADDRESS", "CONNECTED", "LAST POLL", "OK", "METRICS", "INTERVAL", "LAST ERROR")
	for _, s := range page.Items {
		t.Row(
			s.Name, dash(s.Address), yesNo(s.Connected), relativeTime(s.LastPollTime),
			yesNo(s.LastPollSuccess), s.MetricsCollected, dash(s.PollInterval),
			dash(truncate(s.LastError, 40)),
		)
	}
	if err := t.Flush(); err != nil {
		return err
	}

	page.note(out, "")
	return nil
}

func newDeviceMIBCommand(env *Env) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "mib",
		Aliases: []string{"mibs"},
		Short:   "Manage a device's directly assigned MIBs",
	}

	cmd.AddCommand(
		newDeviceMIBListCommand(env),
		&cobra.Command{
			Use:               "add <device> <mib>...",
			Short:             "Assign MIBs to a device",
			Aliases:           []string{"assign"},
			Args:              cobra.MinimumNArgs(2),
			ValidArgsFunction: env.completeNamesRepeating(deviceNames, mibNames),
			RunE: func(_ *cobra.Command, args []string) error {
				return env.runBulk(deviceMIBOp("Assigned", func(
					ctx context.Context, api *client.Client, deviceID, mibID int64,
				) error {
					return api.AssignMIBToDevice(ctx, deviceID, mibID)
				}), args[0], args[1:])
			},
		},
		&cobra.Command{
			Use:               "remove <device> <mib>...",
			Aliases:           []string{"rm"},
			Short:             "Unassign MIBs from a device",
			Args:              cobra.MinimumNArgs(2),
			ValidArgsFunction: env.completeNamesRepeating(deviceNames, mibNames),
			RunE: func(_ *cobra.Command, args []string) error {
				return env.runBulk(deviceMIBOp("Removed", func(
					ctx context.Context, api *client.Client, deviceID, mibID int64,
				) error {
					return api.RemoveMIBFromDevice(ctx, deviceID, mibID)
				}), args[0], args[1:])
			},
		},
	)
	cmd.AddCommand(newMIBAssignmentCommands(env, deviceAssignmentScope())...)
	return cmd
}

func newDeviceMIBListCommand(env *Env) *cobra.Command {
	filter := newAssignedMIBFilter()

	cmd := &cobra.Command{
		Use:     "list <device>",
		Aliases: []string{"ls"},
		Short:   "List a device's MIBs",
		Long: "List every MIB a device polls, whether assigned to it directly or\n" +
			"inherited from one of its profiles.\n\n" +
			"--search matches a module's name, description or tags, ignoring\n" +
			"case. The SOURCE column is always decided against the device's full\n" +
			"direct set, so a search never relabels an assignment.\n\n" +
			"Example:\n" +
			"  netmon device mib list core-01 --search cisco",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: env.completeNames(deviceNames),
		RunE: func(_ *cobra.Command, args []string) error {
			if err := filter.validate(); err != nil {
				return err
			}

			return env.runWithRefresh(func(ctx context.Context, out io.Writer) error {
				api, err := env.Client()
				if err != nil {
					return err
				}
				id, err := api.ResolveDevice(ctx, args[0])
				if err != nil {
					return err
				}
				mibs, err := api.DeviceMIBs(ctx, id)
				if err != nil {
					return err
				}
				page := filter.apply(mibs.AllMIBs)

				if env.Opts.JSON {
					report := deviceMIBsReport{DeviceMIBs: *mibs, Listing: page.counts()}
					report.AllMIBs = page.Items
					return writeJSON(out, &report)
				}
				return renderDeviceMIBTable(out, page, mibs.DirectMIBs, "")
			})
		},
	}

	filter.bind(cmd)
	return cmd
}

// deviceMIBsReport is the --json form of "device mib list". The listing block
// describes all_mibs, which is the list the command prints and the only one
// --search and --limit act on; direct_mibs stays whole because the SOURCE
// column is decided against it.
type deviceMIBsReport struct {
	client.DeviceMIBs

	Listing listCounts `json:"listing"`
}

// deviceMIBOp describes attaching or detaching a device's MIBs.
func deviceMIBOp(
	verb string,
	apply func(context.Context, *client.Client, int64, int64) error,
) bulkOp {
	return bulkOp{
		kind:          "mib",
		verb:          verb,
		resolveOwner:  resolveDeviceRef,
		resolveTarget: resolveMIBRef,
		apply:         apply,
	}
}

func newDeviceProfileCommand(env *Env) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "profile",
		Aliases: []string{"profiles"},
		Short:   "Manage a device's profile assignments",
	}

	cmd.AddCommand(
		newDeviceProfileListCommand(env),
		&cobra.Command{
			Use:               "add <device> <profile>...",
			Aliases:           []string{"assign"},
			Short:             "Assign profiles to a device",
			Args:              cobra.MinimumNArgs(2),
			ValidArgsFunction: env.completeNamesRepeating(deviceNames, profileNames),
			RunE: func(_ *cobra.Command, args []string) error {
				return env.runBulk(deviceProfileOp("Assigned", func(
					ctx context.Context, api *client.Client, deviceID, profileID int64,
				) error {
					return api.AssignProfileToDevice(ctx, deviceID, profileID)
				}), args[0], args[1:])
			},
		},
		&cobra.Command{
			Use:               "remove <device> <profile>...",
			Aliases:           []string{"rm"},
			Short:             "Unassign profiles from a device",
			Args:              cobra.MinimumNArgs(2),
			ValidArgsFunction: env.completeNamesRepeating(deviceNames, profileNames),
			RunE: func(_ *cobra.Command, args []string) error {
				return env.runBulk(deviceProfileOp("Removed", func(
					ctx context.Context, api *client.Client, deviceID, profileID int64,
				) error {
					return api.RemoveProfileFromDevice(ctx, deviceID, profileID)
				}), args[0], args[1:])
			},
		},
	)
	return cmd
}

func newDeviceProfileListCommand(env *Env) *cobra.Command {
	filter := newProfileListFilter()

	cmd := &cobra.Command{
		Use:     "list <device>",
		Aliases: []string{"ls"},
		Short:   "List a device's profiles",
		Long: "List the profiles assigned to a device.\n\n" +
			"--search matches a profile's name or description, ignoring case.",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: env.completeNames(deviceNames),
		RunE: func(_ *cobra.Command, args []string) error {
			if err := filter.validate(); err != nil {
				return err
			}

			return env.runWithRefresh(func(ctx context.Context, out io.Writer) error {
				api, err := env.Client()
				if err != nil {
					return err
				}
				id, err := api.ResolveDevice(ctx, args[0])
				if err != nil {
					return err
				}
				profiles, err := api.DeviceProfiles(ctx, id)
				if err != nil {
					return err
				}
				page := filter.apply(profiles)

				if env.Opts.JSON {
					return writeJSON(out, page)
				}
				return renderProfileTable(out, page)
			})
		},
	}

	filter.bind(cmd)
	return cmd
}

// deviceProfileOp describes attaching or detaching a device's profiles.
func deviceProfileOp(
	verb string,
	apply func(context.Context, *client.Client, int64, int64) error,
) bulkOp {
	return bulkOp{
		kind:          "profile",
		verb:          verb,
		resolveOwner:  resolveDeviceRef,
		resolveTarget: resolveProfileRef,
		apply:         apply,
	}
}

// firstNonEmpty returns the first non-empty string.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
