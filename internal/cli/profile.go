package cli

import (
	"context"
	"fmt"
	"io"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-netmon/pkg/client"
)

// newProfileCommand builds the "netmon profile" subtree.
func newProfileCommand(env *Env) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "profile",
		Aliases: []string{"profiles"},
		Short:   "Manage device profiles",
		Long: "A profile is a named bundle of MIBs. Assigning one profile to many\n" +
			"devices keeps a fleet polling the same objects without repeating the\n" +
			"MIB assignments device by device.\n\n" +
			"Commands that take a profile accept either its name or its numeric ID.",
	}

	cmd.AddCommand(
		newProfileListCommand(env),
		newProfileShowCommand(env),
		newProfileCreateCommand(env),
		newProfileUpdateCommand(env),
		newProfileDeleteCommand(env),
		newProfileMIBCommand(env),
		newProfileDevicesCommand(env),
	)
	return cmd
}

func newProfileListCommand(env *Env) *cobra.Command {
	filter := newProfileListFilter()

	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List device profiles",
		Long: "List the profiles devices can be assigned to.\n\n" +
			"--search matches a profile's name or description, ignoring case.\n\n" +
			"Example:\n" +
			"  netmon profile list --search switch",
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
				profiles, err := api.ListProfiles(ctx)
				if err != nil {
					return err
				}
				page := filter.apply(profiles)

				if env.Opts.JSON {
					return writeJSON(out, page)
				}
				// A profile's size is the first thing an operator wants, and
				// the listing endpoint does not carry it. Only the profiles
				// being printed are counted, so a search also cuts the
				// per-profile requests it costs.
				return renderProfileRows(out, page, profileMIBCounts(ctx, api, page.Items))
			})
		},
	}

	filter.bind(cmd)
	return cmd
}

// newProfileListFilter builds the search-and-cap filter for a profile listing.
//
// It is uncapped by default for the same reason the device listing is: a
// profile list is short, hand-curated, and read to confirm what exists.
func newProfileListFilter() *listFilter[client.Profile] {
	return &listFilter[client.Profile]{
		noun:   "profiles",
		fields: "name or description",
		matches: func(p client.Profile, needle string) bool {
			return containsFold(needle, p.Name, p.Description)
		},
	}
}

// renderProfileTable lists profiles for call sites that have not gathered MIB
// counts, such as "netmon device profile list".
func renderProfileTable(out io.Writer, page listPage[client.Profile]) error {
	return renderProfileRows(out, page, nil)
}

// renderProfileRows lists profiles, adding a MIB count column when counts is
// non-nil. counts maps a profile ID to its display value so an unreadable
// profile can show "?" instead of a misleading zero.
func renderProfileRows(out io.Writer, page listPage[client.Profile], counts map[int64]string) error {
	if page.writeEmpty(out, "",
		"No profiles defined. Create one with: netmon profile create --name <name>") {
		return nil
	}

	if counts == nil {
		t := newTable(out, "ID", "NAME", "DESCRIPTION", "CREATED")
		for _, p := range page.Items {
			t.Row(p.ID, p.Name, dash(truncate(p.Description, 48)), relativeTime(p.CreatedAt))
		}
		if err := t.Flush(); err != nil {
			return err
		}
		page.note(out, "")
		return nil
	}

	t := newTable(out, "ID", "NAME", "DESCRIPTION", "MIBS", "CREATED")
	for _, p := range page.Items {
		t.Row(p.ID, p.Name, dash(truncate(p.Description, 48)), counts[p.ID], relativeTime(p.CreatedAt))
	}
	if err := t.Flush(); err != nil {
		return err
	}

	page.note(out, "")
	return nil
}

// profileMIBCounts reads each profile's MIB count. A profile whose MIBs cannot
// be read shows "?" rather than failing the whole listing.
func profileMIBCounts(ctx context.Context, api *client.Client, profiles []client.Profile) map[int64]string {
	counts := make(map[int64]string, len(profiles))
	for _, p := range profiles {
		mibs, err := api.ProfileMIBs(ctx, p.ID)
		if err != nil {
			counts[p.ID] = "?"
			continue
		}
		counts[p.ID] = strconv.Itoa(len(mibs))
	}
	return counts
}

func newProfileShowCommand(env *Env) *cobra.Command {
	return &cobra.Command{
		Use:               "show <profile>",
		Short:             "Show a profile's MIBs and the devices using it",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: env.completeNames(profileNames),
		RunE: func(_ *cobra.Command, args []string) error {
			return env.runWithRefresh(func(ctx context.Context, out io.Writer) error {
				api, err := env.Client()
				if err != nil {
					return err
				}
				id, err := api.ResolveProfile(ctx, args[0])
				if err != nil {
					return err
				}

				profile, err := api.GetProfile(ctx, id)
				if err != nil {
					return err
				}
				// A profile means little without its contents and its reach, so
				// gather both rather than making the user run three commands.
				mibs, mibErr := api.ProfileMIBs(ctx, id)
				devices, devErr := api.ProfileDevices(ctx, id)

				if env.Opts.JSON {
					return writeJSON(out, map[string]any{
						"profile": profile,
						"mibs":    mibs,
						"devices": devices,
					})
				}
				return renderProfileDetail(out, profile, mibs, mibErr, devices, devErr)
			})
		},
	}
}

func renderProfileDetail(
	out io.Writer,
	profile *client.Profile,
	mibs []client.AssignedMIB, mibErr error,
	devices []client.Device, devErr error,
) error {
	err := keyValues(out,
		[2]string{"ID", fmt.Sprint(profile.ID)},
		[2]string{"Name", profile.Name},
		[2]string{"Description", dash(profile.Description)},
		[2]string{"Created", profile.CreatedAt.Format("2006-01-02 15:04:05")},
	)
	if err != nil {
		return err
	}

	fmt.Fprintln(out, "\nMIBs:")
	switch {
	case mibErr != nil:
		fmt.Fprintf(out, "  unavailable: %v\n", mibErr)
	case len(mibs) == 0:
		fmt.Fprintln(out, "  (none)")
	default:
		if err := renderProfileMIBTable(out, wholeList(mibs, "MIBs"), "  "); err != nil {
			return err
		}
	}

	fmt.Fprintln(out, "\nDevices:")
	switch {
	case devErr != nil:
		fmt.Fprintf(out, "  unavailable: %v\n", devErr)
	case len(devices) == 0:
		fmt.Fprintln(out, "  (none)")
	default:
		for _, d := range devices {
			fmt.Fprintf(out, "  %d  %s (%s)\n", d.ID, d.Name, d.Address)
		}
	}
	return nil
}

func newProfileCreateCommand(env *Env) *cobra.Command {
	var profile client.Profile

	cmd := &cobra.Command{
		Use:     "create",
		Aliases: []string{"add"},
		Short:   "Create a device profile",
		Long: "Create an empty profile, then fill it with MIBs.\n\n" +
			"Example:\n" +
			"  netmon profile create --name switch --description \"Access switches\"\n" +
			"  netmon profile mib add switch IF-MIB BRIDGE-MIB",
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(context.Background(), env.Opts.Timeout)
			defer cancel()

			api, err := env.Client()
			if err != nil {
				return err
			}
			created, err := api.CreateProfile(ctx, profile)
			if err != nil {
				return err
			}
			// A later resolve in this process must see the new name.
			api.InvalidateProfiles()

			if env.Opts.JSON {
				return writeJSON(env.Out, created)
			}
			fmt.Fprintf(env.Out, "Created profile %s (id %d)\n", created.Name, created.ID)
			return nil
		},
	}

	bindProfileFlags(cmd, &profile)
	mustMarkRequired(cmd, "name")
	return cmd
}

func newProfileUpdateCommand(env *Env) *cobra.Command {
	var profile client.Profile

	cmd := &cobra.Command{
		Use:   "update <profile>",
		Short: "Update a profile's name or description",
		Long: "Change a profile's fields. Only the flags you pass are changed;\n" +
			"everything else keeps its current value.",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: env.completeNames(profileNames),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(context.Background(), env.Opts.Timeout)
			defer cancel()

			api, err := env.Client()
			if err != nil {
				return err
			}
			id, err := api.ResolveProfile(ctx, args[0])
			if err != nil {
				return err
			}

			// The API replaces the whole record, so start from the stored
			// profile and overlay only the flags the user actually set.
			current, err := api.GetProfile(ctx, id)
			if err != nil {
				return err
			}
			applyProfileFlagOverrides(cmd, current, profile)

			updated, err := api.UpdateProfile(ctx, id, *current)
			if err != nil {
				return err
			}
			// An update may have renamed the profile.
			api.InvalidateProfiles()

			if env.Opts.JSON {
				return writeJSON(env.Out, updated)
			}
			fmt.Fprintf(env.Out, "Updated profile %s (id %d)\n", updated.Name, updated.ID)
			return nil
		},
	}

	bindProfileFlags(cmd, &profile)
	return cmd
}

// bindProfileFlags declares the flags that describe a profile.
func bindProfileFlags(cmd *cobra.Command, profile *client.Profile) {
	f := cmd.Flags()
	f.StringVar(&profile.Name, "name", "", "profile name (unique)")
	f.StringVar(&profile.Description, "description", "", "what this profile is for")
}

// applyProfileFlagOverrides copies only explicitly-set flags onto current.
func applyProfileFlagOverrides(cmd *cobra.Command, current *client.Profile, flags client.Profile) {
	if cmd.Flags().Changed("name") {
		current.Name = flags.Name
	}
	if cmd.Flags().Changed("description") {
		current.Description = flags.Description
	}
}

func newProfileDeleteCommand(env *Env) *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:               "delete <profile>",
		Aliases:           []string{"rm", "remove"},
		Short:             "Remove a profile",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: env.completeNames(profileNames),
		RunE: func(_ *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(context.Background(), env.Opts.Timeout)
			defer cancel()

			api, err := env.Client()
			if err != nil {
				return err
			}
			id, err := api.ResolveProfile(ctx, args[0])
			if err != nil {
				return err
			}
			profile, err := api.GetProfile(ctx, id)
			if err != nil {
				return err
			}

			// Deleting a profile drops it from every device that uses it, so
			// require an explicit confirmation unless the caller opted out.
			if !force {
				return fmt.Errorf(
					"refusing to delete profile %s (id %d) without --force", profile.Name, profile.ID)
			}
			if err := api.DeleteProfile(ctx, id); err != nil {
				return err
			}
			// The name is gone; a later resolve must not answer from memory.
			api.InvalidateProfiles()

			if env.Opts.JSON {
				return writeJSON(env.Out, changeReport{
					Action: "deleted", Kind: "profile", Ref: profile.Name, ID: profile.ID,
				})
			}
			fmt.Fprintf(env.Out, "Deleted profile %s (id %d)\n", profile.Name, profile.ID)
			return nil
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "confirm removal")
	return cmd
}

func newProfileMIBCommand(env *Env) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "mib",
		Aliases: []string{"mibs"},
		Short:   "Manage the MIBs a profile contains",
	}

	cmd.AddCommand(
		newProfileMIBListCommand(env),
		&cobra.Command{
			Use:               "add <profile> <mib>...",
			Aliases:           []string{"assign"},
			Short:             "Add MIBs to a profile",
			Args:              cobra.MinimumNArgs(2),
			ValidArgsFunction: env.completeNamesRepeating(profileNames, mibNames),
			RunE: func(_ *cobra.Command, args []string) error {
				return env.runBulk(profileMIBOp("Added", func(
					ctx context.Context, api *client.Client, profileID, mibID int64,
				) error {
					return api.AddMIBToProfile(ctx, profileID, mibID)
				}), args[0], args[1:])
			},
		},
		&cobra.Command{
			Use:               "remove <profile> <mib>...",
			Aliases:           []string{"rm"},
			Short:             "Remove MIBs from a profile",
			Args:              cobra.MinimumNArgs(2),
			ValidArgsFunction: env.completeNamesRepeating(profileNames, mibNames),
			RunE: func(_ *cobra.Command, args []string) error {
				return env.runBulk(profileMIBOp("Removed", func(
					ctx context.Context, api *client.Client, profileID, mibID int64,
				) error {
					return api.RemoveMIBFromProfile(ctx, profileID, mibID)
				}), args[0], args[1:])
			},
		},
	)
	cmd.AddCommand(newMIBAssignmentCommands(env, profileAssignmentScope())...)
	return cmd
}

func newProfileMIBListCommand(env *Env) *cobra.Command {
	filter := newAssignedMIBFilter()

	cmd := &cobra.Command{
		Use:     "list <profile>",
		Aliases: []string{"ls"},
		Short:   "List a profile's MIBs",
		Long: "List the MIBs a profile carries to every device using it.\n\n" +
			"--search matches a module's name, description or tags, ignoring\n" +
			"case.\n\n" +
			"Example:\n" +
			"  netmon profile mib list switch --search interface",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: env.completeNames(profileNames),
		RunE: func(_ *cobra.Command, args []string) error {
			if err := filter.validate(); err != nil {
				return err
			}

			return env.runWithRefresh(func(ctx context.Context, out io.Writer) error {
				api, err := env.Client()
				if err != nil {
					return err
				}
				id, err := api.ResolveProfile(ctx, args[0])
				if err != nil {
					return err
				}
				mibs, err := api.ProfileMIBs(ctx, id)
				if err != nil {
					return err
				}
				page := filter.apply(mibs)

				if env.Opts.JSON {
					return writeJSON(out, page)
				}
				return renderProfileMIBTable(out, page, "")
			})
		},
	}

	filter.bind(cmd)
	return cmd
}

// renderProfileMIBTable lists the MIBs in a profile and how the profile polls
// each one. indent prefixes the first column so the same table can sit inside a
// detail view.
func renderProfileMIBTable(out io.Writer, page listPage[client.AssignedMIB], indent string) error {
	if page.writeEmpty(out, indent, "No MIBs in this profile.") {
		return nil
	}

	t := newTable(out, indent+"ID", "NAME", "OBJECTS", "ENABLED", "FILTERS")
	for _, m := range page.Items {
		t.Row(indent+fmt.Sprint(m.ID), m.Name, m.ObjectCount,
			yesNo(m.Assignment.Enabled), filterSummary(m.Assignment))
	}
	if err := t.Flush(); err != nil {
		return err
	}

	page.note(out, indent)
	return nil
}

// profileMIBOp describes adding or removing a profile's MIBs.
func profileMIBOp(
	verb string,
	apply func(context.Context, *client.Client, int64, int64) error,
) bulkOp {
	return bulkOp{
		kind:          "mib",
		verb:          verb,
		resolveOwner:  resolveProfileRef,
		resolveTarget: resolveMIBRef,
		apply:         apply,
	}
}

func newProfileDevicesCommand(env *Env) *cobra.Command {
	filter := newDeviceListFilter()

	cmd := &cobra.Command{
		Use:     "devices <profile>",
		Aliases: []string{"device"},
		Short:   "List the devices using a profile",
		Long: "List every device that inherits a profile.\n\n" +
			"--search matches a device's name or address, ignoring case.",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: env.completeNames(profileNames),
		RunE: func(_ *cobra.Command, args []string) error {
			if err := filter.validate(); err != nil {
				return err
			}

			return env.runWithRefresh(func(ctx context.Context, out io.Writer) error {
				api, err := env.Client()
				if err != nil {
					return err
				}
				id, err := api.ResolveProfile(ctx, args[0])
				if err != nil {
					return err
				}
				devices, err := api.ProfileDevices(ctx, id)
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
