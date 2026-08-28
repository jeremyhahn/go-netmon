package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"strconv"
	"strings"
)

// DefaultHelperImage is the image used for the throwaway containers that stream
// volume contents in and out.
//
// It needs nothing but a POSIX tar and find. It is pinned so that a backup and
// the restore that follows it cannot disagree about tar behavior.
const DefaultHelperImage = "alpine:3.19"

// DefaultDockerBinary is the client this package drives.
const DefaultDockerBinary = "docker"

// CLIOptions configures a CLI.
type CLIOptions struct {
	// Binary is the docker client to run. Empty means DefaultDockerBinary.
	Binary string
	// Project is the compose project to operate on. It is pinned by the
	// caller, never inherited from the working directory: without -p, compose
	// derives the project from the directory name, and a command aimed at a
	// test stack reaches the deployed one instead.
	Project string
	// ComposeFile is the absolute path of the compose file defining the stack.
	ComposeFile string
	// HelperImage streams volume contents. Empty means DefaultHelperImage.
	HelperImage string
}

// CLI drives the Docker command-line client.
//
// Every invocation is built as an explicit argument vector and handed to
// os/exec, which executes the binary directly. No string is ever assembled into
// a command line, and no shell is involved at any point - not on the host, and
// not inside the helper containers, whose commands are argument vectors too.
// There is therefore nothing for a volume name, a path or a project name to be
// interpreted as.
//
// The command-line client is used rather than the Docker SDK because half of
// what this tool needs - the compose project, its service list, its volume
// definitions and its bind mounts - is a compose plugin concept that the SDK
// does not model at all. Driving one tool for both keeps the ownership checks
// and the operations they guard reading from the same source of truth, and it
// keeps the dependency surface of a disaster-recovery binary to the client the
// deployment already requires.
type CLI struct {
	binary      string
	project     string
	composeFile string
	helperImage string

	// exec runs one argument vector. It is a field so that a test can assert
	// on the exact vector that would reach the process - which is the whole
	// safety claim here - without a daemon. It is never a command line, and
	// the production value below is the only thing that executes anything.
	exec execFunc
}

// execFunc runs a program with an explicit argument vector.
type execFunc func(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, name string, args []string) error

// runProcess executes a program directly. There is no shell: os/exec passes the
// vector to execve, so nothing in it can be reinterpreted as syntax.
func runProcess(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, name string, args []string) error {
	// #nosec G204 -- args is an argument vector this package builds from
	// validated names; it is never a shell command line.
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

// NewCLI builds a Docker client bound to one compose project and compose file.
func NewCLI(opts CLIOptions) (*CLI, error) {
	if !validName(opts.Project) {
		return nil, &InvalidNameError{Kind: "compose project", Name: opts.Project}
	}
	if strings.TrimSpace(opts.ComposeFile) == "" {
		return nil, &ComposeFileError{Path: "<empty>", Reason: "no compose file was given"}
	}
	c := &CLI{
		binary:      opts.Binary,
		project:     opts.Project,
		composeFile: opts.ComposeFile,
		helperImage: opts.HelperImage,
	}
	if c.binary == "" {
		c.binary = DefaultDockerBinary
	}
	if c.helperImage == "" {
		c.helperImage = DefaultHelperImage
	}
	c.exec = runProcess
	return c, nil
}

// compose prefixes an argument vector with the pinned project and compose file.
func (c *CLI) compose(args ...string) []string {
	return append([]string{"compose", "-p", c.project, "-f", c.composeFile}, args...)
}

// run executes docker with an explicit argument vector and returns its stdout.
func (c *CLI) run(ctx context.Context, args ...string) (string, error) {
	var stdout bytes.Buffer
	if err := c.stream(ctx, nil, &stdout, args...); err != nil {
		return "", err
	}
	return stdout.String(), nil
}

// stream executes docker with an explicit argument vector, wiring stdin and
// stdout to the child process. Standard error is always captured, so a failure
// reports what the runtime actually said rather than an exit status alone.
func (c *CLI) stream(ctx context.Context, stdin io.Reader, stdout io.Writer, args ...string) error {
	var captured bytes.Buffer
	if err := c.exec(ctx, stdin, stdout, &captured, c.binary, args); err != nil {
		return &DockerError{Args: args, Stderr: captured.String(), Err: err}
	}
	return nil
}

// lines splits command output into non-empty trimmed lines.
func lines(out string) []string {
	var result []string
	for _, l := range strings.Split(out, "\n") {
		if trimmed := strings.TrimSpace(l); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

// rows splits tab-separated output, preserving the separators.
//
// lines trims each line, which silently removes a trailing tab — and a trailing
// tab is exactly what a row whose last field is empty ends in. Trimming it turns
// "name\t" into "name" and makes an absent label indistinguishable from output
// this package cannot parse.
func rows(out string) []string {
	var result []string
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimRight(l, "\r")
		if strings.TrimSpace(l) != "" {
			result = append(result, l)
		}
	}
	return result
}

// ForeignContainers lists containers in the project that this compose file did
// not create.
func (c *CLI) ForeignContainers(ctx context.Context) ([]Container, error) {
	out, err := c.run(ctx, "ps", "-a",
		"--filter", "label=com.docker.compose.project="+c.project,
		"--format", "{{.Names}}\t{{.Label \""+ConfigFilesLabel+"\"}}")
	if err != nil {
		return nil, err
	}
	var foreign []Container
	for _, line := range rows(out) {
		name, configFiles, ok := strings.Cut(line, "\t")
		if !ok {
			return nil, &DockerOutputError{Command: "ps", Reason: "expected name and config-files label", Output: line}
		}
		if configFiles == c.composeFile {
			continue
		}
		foreign = append(foreign, Container{Name: name, ConfigFiles: configFiles})
	}
	return foreign, nil
}

// ComposeVolumes lists the logical volume names the compose file defines.
func (c *CLI) ComposeVolumes(ctx context.Context) ([]string, error) {
	out, err := c.run(ctx, c.compose("config", "--volumes")...)
	if err != nil {
		return nil, err
	}
	return lines(out), nil
}

// composeConfig is the subset of "docker compose config --format json" this
// tool reads.
type composeConfig struct {
	Services map[string]struct {
		Image   string `json:"image"`
		Volumes []struct {
			Type   string `json:"type"`
			Source string `json:"source"`
			Target string `json:"target"`
		} `json:"volumes"`
	} `json:"services"`
}

// ComposeServices lists the services the compose file defines.
func (c *CLI) ComposeServices(ctx context.Context) ([]ComposeService, error) {
	out, err := c.run(ctx, c.compose("config", "--format", "json")...)
	if err != nil {
		return nil, err
	}
	var cfg composeConfig
	if err := json.Unmarshal([]byte(out), &cfg); err != nil {
		return nil, &DockerOutputError{Command: "compose config", Reason: "is not valid JSON", Output: out}
	}
	services := make([]ComposeService, 0, len(cfg.Services))
	for name, svc := range cfg.Services {
		s := ComposeService{Name: name, Image: svc.Image}
		for _, v := range svc.Volumes {
			if v.Type == "bind" {
				s.Binds = append(s.Binds, Bind{Source: v.Source, Target: v.Target})
			}
		}
		services = append(services, s)
	}
	sortServices(services)
	return services, nil
}

// RunningServices lists services that currently have a running container.
func (c *CLI) RunningServices(ctx context.Context) ([]string, error) {
	out, err := c.run(ctx, c.compose("ps", "--services", "--filter", "status=running")...)
	if err != nil {
		return nil, err
	}
	return lines(out), nil
}

// StopServices stops the named services.
func (c *CLI) StopServices(ctx context.Context, services []string) error {
	return c.serviceAction(ctx, "stop", services)
}

// StartServices starts the named services.
func (c *CLI) StartServices(ctx context.Context, services []string) error {
	return c.serviceAction(ctx, "start", services)
}

func (c *CLI) serviceAction(ctx context.Context, action string, services []string) error {
	if len(services) == 0 {
		return nil
	}
	if err := checkNames("service", services); err != nil {
		return err
	}
	_, err := c.run(ctx, c.compose(append([]string{action}, services...)...)...)
	return err
}

// CreateVolumes materializes the project's volumes without starting anything.
func (c *CLI) CreateVolumes(ctx context.Context) error {
	_, err := c.run(ctx, c.compose("up", "--no-start")...)
	return err
}

// UpDetached brings the whole project up in the background.
func (c *CLI) UpDetached(ctx context.Context) error {
	_, err := c.run(ctx, c.compose("up", "-d")...)
	return err
}

// VolumeNames lists every volume the runtime knows.
func (c *CLI) VolumeNames(ctx context.Context) ([]string, error) {
	out, err := c.run(ctx, "volume", "ls", "--format", "{{.Name}}")
	if err != nil {
		return nil, err
	}
	return lines(out), nil
}

// VolumeProject reports the compose project label on a volume.
//
// A volume the runtime does not know is reported as absent. That is the only
// failure treated as "not there": any other error from the daemon is returned,
// because a decision about what to overwrite must not be made from a question
// that went unanswered.
func (c *CLI) VolumeProject(ctx context.Context, volume string) (string, bool, error) {
	if !validName(volume) {
		return "", false, &InvalidNameError{Kind: "volume", Name: volume}
	}
	out, err := c.run(ctx, "volume", "inspect", volume,
		"--format", "{{index .Labels \"com.docker.compose.project\"}}")
	if err != nil {
		var dockerErr *DockerError
		if errors.As(err, &dockerErr) && strings.Contains(strings.ToLower(dockerErr.Stderr), "no such volume") {
			return "", false, nil
		}
		return "", false, err
	}
	project := strings.TrimSpace(out)
	if project == "<no value>" {
		project = ""
	}
	return project, true, nil
}

// VolumeBytes reports the on-disk size of a volume's contents.
//
// It runs in a helper container so the answer is right regardless of storage
// driver and needs no root on the host. The volume is mounted read-only and the
// container has no network.
func (c *CLI) VolumeBytes(ctx context.Context, volume string) (int64, error) {
	if !validName(volume) {
		return 0, &InvalidNameError{Kind: "volume", Name: volume}
	}
	out, err := c.run(ctx, "run", "--rm", "--network", "none",
		"-v", volume+":/src:ro", c.helperImage, "du", "-sb", "/src")
	if err != nil {
		return 0, err
	}
	// "du -sb" prints "<bytes>\t<path>", so the byte count is everything before
	// the first tab and ParseInt is handed it directly.
	//
	// This used to read strings.Fields(field + " ")[0]. The appended space was
	// there to guarantee Fields returned at least one element, and it does not:
	// Fields splits around whitespace and discards it, so a whitespace-only
	// string yields an empty slice and [0] panicked. An idiom that reads as a
	// guard and is not one is worse than no guard, because the next reader
	// stops looking -- and it panicked one line ABOVE the DockerOutputError
	// below, whose Reason is "expected a byte count", leaving that error
	// unreachable for precisely the case it was written for.
	//
	// Measured: the helper container exiting 0 with empty stdout is reachable
	// on a loaded or degraded daemon, and it took down a CI run that way.
	// Empty, whitespace-only and unparseable output now all take the same
	// route, because they are the same thing to the caller: docker did not
	// answer the question.
	field, _, _ := strings.Cut(strings.TrimSpace(out), "\t")
	size, convErr := strconv.ParseInt(strings.TrimSpace(field), 10, 64)
	if convErr != nil {
		return 0, &DockerOutputError{Command: "run du", Reason: "expected a byte count", Output: out}
	}
	return size, nil
}

// ExportVolume writes a volume's contents to w as an uncompressed tar.
//
// Numeric ownership is preserved: the Prometheus and Grafana images run as
// non-root uids, and a restore that loses ownership produces a container that
// cannot write its own data directory.
func (c *CLI) ExportVolume(ctx context.Context, volume string, w io.Writer) error {
	if !validName(volume) {
		return &InvalidNameError{Kind: "volume", Name: volume}
	}
	return c.stream(ctx, nil, w,
		"run", "--rm", "--network", "none", "-v", volume+":/src:ro", c.helperImage,
		"tar", "-C", "/src", "--numeric-owner", "-cf", "-", ".")
}

// ImportVolume replaces a volume's contents with the tar stream in r.
//
// Two containers, two argument vectors, no shell. The destination is emptied
// first so the restore is a replacement rather than a merge: a merge leaves
// behind files the backup does not contain, which is precisely the "silently
// half-matches" outcome a restore must not produce. The deletion runs inside a
// container whose only mount is this volume, so its reach is the volume and
// nothing else.
func (c *CLI) ImportVolume(ctx context.Context, volume string, r io.Reader) error {
	if !validName(volume) {
		return &InvalidNameError{Kind: "volume", Name: volume}
	}
	if err := c.stream(ctx, nil, io.Discard,
		"run", "--rm", "--network", "none", "-v", volume+":/dst", c.helperImage,
		"find", "/dst", "-mindepth", "1", "-delete"); err != nil {
		return err
	}
	return c.stream(ctx, r, io.Discard,
		"run", "--rm", "-i", "--network", "none", "-v", volume+":/dst", c.helperImage,
		"tar", "-C", "/dst", "--numeric-owner", "-xf", "-")
}

// ImageID resolves an image reference to its content id.
//
// An image the host does not have is reported as an empty id rather than an
// error: the manifest records what was there, and a missing image is a fact
// about the host, not a reason to abandon a backup.
func (c *CLI) ImageID(ctx context.Context, image string) (string, error) {
	if strings.TrimSpace(image) == "" {
		return "", nil
	}
	out, err := c.run(ctx, "image", "inspect", image, "--format", "{{.Id}}")
	if err != nil {
		return "", nil
	}
	return strings.TrimSpace(out), nil
}

// Versions reports the runtime and compose plugin versions.
func (c *CLI) Versions(ctx context.Context) (Versions, error) {
	v := Versions{Docker: "unknown", Compose: "unknown"}
	if out, err := c.run(ctx, "version", "--format", "{{.Server.Version}}"); err == nil {
		v.Docker = strings.TrimSpace(out)
	}
	if out, err := c.run(ctx, "compose", "version", "--short"); err == nil {
		v.Compose = strings.TrimSpace(out)
	}
	return v, nil
}

var _ Docker = (*CLI)(nil)
