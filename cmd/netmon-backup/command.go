package main

import (
	"os"
	"os/user"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-netmon/internal/backup"
)

// globals holds the settings every command shares, bound to persistent flags.
//
// Nothing here is inferred from the working directory except the repository
// default, which is printed back before anything happens. The compose project in
// particular is pinned: without it compose derives one from the directory name,
// and a command aimed at a test stack reaches the deployed one instead.
type globals struct {
	repo        string
	composeFile string
	project     string
	mibTarget   string
	helperImage string
	dockerBin   string
	webUIURL    string
	noLogical   bool
}

// service builds the service layer from the shared settings.
func (g *globals) service() (*backup.Service, error) {
	composeFile := g.composeFile
	if composeFile == "" {
		composeFile = filepath.Join(g.repo, "docker-compose.yml")
	}
	docker, err := backup.NewCLI(backup.CLIOptions{
		Binary:      g.dockerBin,
		Project:     g.project,
		ComposeFile: mustAbs(composeFile),
		HelperImage: g.helperImage,
	})
	if err != nil {
		return nil, err
	}
	return backup.New(backup.Config{
		RepoRoot:       g.repo,
		ComposeFile:    composeFile,
		Project:        g.project,
		MIBMountTarget: g.mibTarget,
		Docker:         docker,
		Out:            os.Stderr,
		CreatedBy:      createdBy(),
		HomeDir:        homeDir(),
		WebUIURL:       g.logicalSource(),
	})
}

// logicalSource reports the web UI the logical export reads, or empty to skip
// it. --no-logical wins over --webui-url, so a run that asked for no export
// cannot be given one by a leftover environment variable.
func (g *globals) logicalSource() string {
	if g.noLogical {
		return ""
	}
	return strings.TrimSuffix(g.webUIURL, "/")
}

func newRootCommand() *cobra.Command {
	g := &globals{}
	root := &cobra.Command{
		Use:   "netmon-backup",
		Short: "Back up and restore a deployed go-netmon stack",
		Long: strings.TrimSpace(`
Back up and restore a deployed go-netmon stack: its Docker volumes, the
repository configuration bind-mounted into it, and the MIB source tree.

Backups are COLD. Every service in the compose project is stopped, the data is
copied at rest, and exactly the services that were running are started again -
including if this command is interrupted. The web UI and poller stores are
PebbleDB and this deployment's Prometheus runs without --web.enable-admin-api,
so there is no supported way to snapshot either while running.

Every file this tool creates, removes or overwrites goes through a filesystem
sandbox anchored on an explicitly chosen directory. A path that would leave that
directory is refused by the kernel, not sanitized and used anyway.`),
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	flags := root.PersistentFlags()
	flags.StringVar(&g.repo, "repo", envOr("NETMON_REPO_ROOT", cwd()), "Repository root holding config.yaml and docker-compose.yml")
	flags.StringVar(&g.composeFile, "compose-file", os.Getenv("NETMON_COMPOSE_FILE"), "Compose file defining the stack (default <repo>/docker-compose.yml)")
	flags.StringVar(&g.project, "project", envOr("NETMON_PROJECT", "go-netmon"), "Compose project to operate on")
	flags.StringVar(&g.mibTarget, "mib-target", envOr("NETMON_MIB_MOUNT_TARGET", backup.DefaultMIBMountTarget), "Container path the MIB source tree is mounted at")
	flags.StringVar(&g.helperImage, "helper-image", envOr("NETMON_HELPER_IMAGE", backup.DefaultHelperImage), "Image used to stream volume contents")
	flags.StringVar(&g.dockerBin, "docker", envOr("DOCKER", backup.DefaultDockerBinary), "Docker client to run")
	flags.StringVar(&g.webUIURL, "webui-url", envOr("NETMON_WEBUI_URL", backup.DefaultWebUIURL), "Web UI to read the logical export from")
	flags.BoolVar(&g.noLogical, "no-logical", false, "Skip the logical REST export")

	root.AddCommand(newBackupCommand(g), newVerifyCommand(g), newRestoreCommand(g))
	return root
}

func newBackupCommand(g *globals) *cobra.Command {
	var (
		output, name, only, skip, mibTree string
		keepStopped                       bool
	)
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "Back up the deployed stack",
		Long: strings.TrimSpace(`
Create a backup directory holding one compressed member per Docker volume, the
MIB source tree, the bind-mounted repository configuration, and a manifest and
checksum list describing all of it.

Components: config, mibs, webui, poller, metrics, grafana

The stack is stopped for the length of the copy and started again afterwards,
on every exit path including interruption.`),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			svc, err := g.service()
			if err != nil {
				return err
			}
			onlyList, skipList, err := parseSelection(only, skip)
			if err != nil {
				return err
			}
			_, err = svc.Backup(cmd.Context(), backup.Options{
				OutputDir:   output,
				Name:        name,
				Only:        onlyList,
				Skip:        skipList,
				KeepStopped: keepStopped,
				MIBTree:     envOr("NETMON_MIB_TREE", mibTree),
			})
			return err
		},
	}
	f := cmd.Flags()
	f.StringVarP(&output, "output", "o", "", "Parent directory for backups (default <repo>/backups)")
	f.StringVarP(&name, "name", "n", "", "Backup directory name (default netmon-<UTC timestamp>)")
	f.StringVar(&only, "only", "", "Comma-separated components to back up")
	f.StringVar(&skip, "skip", "", "Comma-separated components to omit")
	f.StringVar(&mibTree, "mib-tree", "", "Host MIB source directory (default: read from the compose bind mount)")
	f.BoolVar(&keepStopped, "keep-stopped", false, "Leave the stack stopped afterwards")
	return cmd
}

func newVerifyCommand(g *globals) *cobra.Command {
	var from string
	cmd := &cobra.Command{
		Use:   "verify",
		Short: "Verify a backup's integrity without changing anything",
		Long: strings.TrimSpace(`
Check a backup directory end to end: its manifest, the checksum list covering
every file in it, and a full decompression of every member. Reads only.`),
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			svc, err := g.service()
			if err != nil {
				return err
			}
			archive, err := svc.Verify(from)
			if err != nil {
				return err
			}
			return archive.Close()
		},
	}
	cmd.Flags().StringVarP(&from, "from", "f", "", "Backup directory to verify (required)")
	_ = cmd.MarkFlagRequired("from")
	return cmd
}

func newRestoreCommand(g *globals) *cobra.Command {
	var (
		from, only, skip, mibTree                            string
		apply, noSafety, forceProject, allowTree, verifyOnly bool
	)
	cmd := &cobra.Command{
		Use:   "restore",
		Short: "Restore the stack from a backup",
		Long: strings.TrimSpace(`
Restore Docker volumes, the MIB source tree and the bind-mounted repository
configuration from a backup directory.

THIS IS DESTRUCTIVE, and it changes nothing without --apply. The archive is
verified in full first, the current state is preserved as a safety backup, and
the plan naming everything that would be overwritten is printed before any of it
happens.

Restoring 'mibs' writes into the host directory the stack bind-mounts - your
working copy, not a Docker volume - and replaces its contents outright. When
that directory already holds files, --allow-tree-overwrite is required on top of
--apply, and without it the restore fails rather than quietly skipping it.`),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			svc, err := g.service()
			if err != nil {
				return err
			}
			onlyList, skipList, err := parseSelection(only, skip)
			if err != nil {
				return err
			}
			_, err = svc.Restore(cmd.Context(), backup.RestoreOptions{
				From:               from,
				Apply:              apply,
				VerifyOnly:         verifyOnly,
				Only:               onlyList,
				Skip:               skipList,
				NoSafetyBackup:     noSafety,
				ForceProject:       forceProject,
				AllowTreeOverwrite: allowTree,
				MIBTree:            envOr("NETMON_MIB_TREE", mibTree),
			})
			return err
		},
	}
	f := cmd.Flags()
	f.StringVarP(&from, "from", "f", "", "Backup directory to restore from (required)")
	f.BoolVar(&apply, "apply", false, "Actually perform the restore (required to change data)")
	f.BoolVar(&verifyOnly, "verify-only", false, "Verify the archive and stop")
	f.StringVar(&only, "only", "", "Comma-separated components to restore")
	f.StringVar(&skip, "skip", "", "Comma-separated components to omit")
	f.BoolVar(&noSafety, "no-safety-backup", false, "Do not back up current state before overwriting it")
	f.BoolVar(&forceProject, "force-project", false, "Allow restoring a backup taken from another compose project")
	f.BoolVar(&allowTree, "allow-tree-overwrite", false, "Permit replacing a non-empty MIB source tree on the host")
	f.StringVar(&mibTree, "mib-tree", "", "Restore the MIB source tree into this directory instead")
	_ = cmd.MarkFlagRequired("from")
	return cmd
}

// parseSelection turns the --only/--skip flags into components, refusing an
// unknown name rather than quietly covering more or less than was asked for.
func parseSelection(only, skip string) ([]backup.Component, []backup.Component, error) {
	onlyList, err := backup.ParseComponents(only)
	if err != nil {
		return nil, nil, err
	}
	skipList, err := backup.ParseComponents(skip)
	if err != nil {
		return nil, nil, err
	}
	return onlyList, skipList, nil
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func cwd() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	return dir
}

func mustAbs(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return abs
}

func homeDir() string {
	dir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return dir
}

// createdBy records who took a backup, for the manifest's provenance.
func createdBy() string {
	name := "unknown"
	if u, err := user.Current(); err == nil && u.Username != "" {
		name = u.Username
	}
	host, err := os.Hostname()
	if err != nil {
		host = "unknown"
	}
	return name + "@" + host
}
