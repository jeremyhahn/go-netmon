package cli

import (
	"context"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-netmon/pkg/client"
)

// UnsupportedShellError reports a shell the completion generator has no script
// for.
type UnsupportedShellError struct {
	Shell string
}

func (e *UnsupportedShellError) Error() string {
	return "netmon: no completion script for " + e.Shell +
		"; supported shells are " + strings.Join(completionShells, ", ")
}

// completionShells are the shells netmon generates a script for.
var completionShells = []string{"bash", "zsh", "fish"}

// newCompletionCommand builds "netmon completion".
func newCompletionCommand(env *Env) *cobra.Command {
	return &cobra.Command{
		Use:   "completion [" + strings.Join(completionShells, "|") + "]",
		Short: "Generate a shell completion script",
		Long: "Print the completion script for a shell.\n\n" +
			"Device, profile and MIB arguments complete from the server, so\n" +
			"  netmon device show core<TAB>\n" +
			"offers the devices that actually exist. That uses the same --server or\n" +
			"NETMON_SERVER the commands do.\n\n" +
			"  bash:  source <(netmon completion bash)\n" +
			"  zsh:   netmon completion zsh > \"${fpath[1]}/_netmon\"\n" +
			"  fish:  netmon completion fish > ~/.config/fish/completions/netmon.fish",
		Args:      cobra.ExactArgs(1),
		ValidArgs: completionShells,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Generate for the root: a script for the "completion" subcommand
			// alone would complete nothing else.
			root := cmd.Root()
			switch args[0] {
			case "bash":
				return root.GenBashCompletionV2(env.Out, true)
			case "zsh":
				return root.GenZshCompletion(env.Out)
			case "fish":
				return root.GenFishCompletion(env.Out, true)
			default:
				return &UnsupportedShellError{Shell: args[0]}
			}
		},
	}
}

// completionTimeout bounds the API call a completion makes. A <TAB> has to feel
// instant, and an unreachable server must never hang a terminal, so this is far
// shorter than --timeout.
const completionTimeout = 2 * time.Second

// nameLister returns the names of one resource kind for a shell to complete.
type nameLister func(context.Context, *client.Client) ([]string, error)

// deviceNames lists configured device names.
func deviceNames(ctx context.Context, api *client.Client) ([]string, error) {
	devices, err := api.ListDevices(ctx)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(devices))
	for i, d := range devices {
		names[i] = d.Name
	}
	return names, nil
}

// profileNames lists profile names.
func profileNames(ctx context.Context, api *client.Client) ([]string, error) {
	profiles, err := api.ListProfiles(ctx)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(profiles))
	for i, p := range profiles {
		names[i] = p.Name
	}
	return names, nil
}

// mibNames lists imported MIB module names.
func mibNames(ctx context.Context, api *client.Client) ([]string, error) {
	mibs, err := api.ListMIBs(ctx, false)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(mibs))
	for i, m := range mibs {
		names[i] = m.Name
	}
	return names, nil
}

// completionFunc is cobra's ValidArgsFunction signature.
type completionFunc func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective)

// completeNames builds a completion that offers, for each argument position,
// the names from the matching lister, and nothing beyond the last one.
func (e *Env) completeNames(listers ...nameLister) completionFunc {
	return e.completion(listers, false)
}

// completeNamesRepeating is completeNames with the final lister repeated, which
// is what the variadic "<owner> <mib>..." commands need.
func (e *Env) completeNamesRepeating(listers ...nameLister) completionFunc {
	return e.completion(listers, true)
}

// completion offers the names valid at the argument position being typed.
//
// Every failure is silent — an unreachable server, an unusable --server value,
// a timeout — because a completion writes to the shell's command line, where a
// diagnostic is noise the user cannot even read properly. An operator who wants
// the reason can run the command itself.
func (e *Env) completion(listers []nameLister, repeat bool) completionFunc {
	return func(_ *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		list, ok := listerFor(listers, len(args), repeat)
		if !ok {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}

		api, err := e.Client()
		if err != nil {
			return nil, cobra.ShellCompDirectiveError
		}

		ctx, cancel := context.WithTimeout(context.Background(), completionTimeout)
		defer cancel()

		names, err := list(ctx, api)
		if err != nil {
			return nil, cobra.ShellCompDirectiveError
		}
		return matchPrefix(names, toComplete), cobra.ShellCompDirectiveNoFileComp
	}
}

// listerFor picks the lister for the argument position being completed, or
// reports that the position takes no name.
func listerFor(listers []nameLister, position int, repeat bool) (nameLister, bool) {
	if position < len(listers) {
		return listers[position], true
	}
	if repeat && len(listers) > 0 {
		return listers[len(listers)-1], true
	}
	return nil, false
}

// matchPrefix keeps the names the user's partial word could still become.
// Matching is case-insensitive because resolving a name is, so completing
// "if-" must still offer "IF-MIB".
func matchPrefix(names []string, prefix string) []string {
	if prefix == "" {
		return names
	}
	matched := make([]string, 0, len(names))
	for _, name := range names {
		if strings.HasPrefix(strings.ToLower(name), strings.ToLower(prefix)) {
			matched = append(matched, name)
		}
	}
	return matched
}
