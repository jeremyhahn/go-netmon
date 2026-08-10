package cli

import (
	"runtime"

	"github.com/spf13/cobra"
)

// buildInfo is the metadata stamped into the binary at link time, plus the
// toolchain and platform it was built for. Together they are what a bug report
// needs to identify a build exactly.
type buildInfo struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildDate string `json:"build_date"`
	Go        string `json:"go"`
	Platform  string `json:"platform"`
}

// newBuildInfo completes the linker-supplied values with the runtime's own.
func newBuildInfo(version, commit, buildDate string) buildInfo {
	return buildInfo{
		Version:   version,
		Commit:    commit,
		BuildDate: buildDate,
		Go:        runtime.Version(),
		Platform:  runtime.GOOS + "/" + runtime.GOARCH,
	}
}

// String renders the one-line form used by "netmon --version".
func (b buildInfo) String() string {
	return b.Version + " (commit " + b.Commit + ", built " + b.BuildDate + ")"
}

// newVersionCommand builds "netmon version".
//
// The same facts are reachable through --version, but a subcommand is what
// scripts and bug reports expect, and only a subcommand can honor --json.
func newVersionCommand(env *Env, info buildInfo) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version, commit and build date",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if env.Opts.JSON {
				return writeJSON(env.Out, info)
			}
			return keyValues(env.Out,
				[2]string{"Version", info.Version},
				[2]string{"Commit", info.Commit},
				[2]string{"Built", info.BuildDate},
				[2]string{"Go", info.Go},
				[2]string{"Platform", info.Platform},
			)
		},
	}
}
