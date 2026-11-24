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

// DefaultHelperImage is the pinned image for the containers that stream volume contents.
const DefaultHelperImage = "alpine:3.19"

// DefaultDockerBinary is the client this package drives.
const DefaultDockerBinary = "docker"

// CLIOptions configures a CLI.
type CLIOptions struct {
	// Binary is the docker client to run. Empty means DefaultDockerBinary.
	Binary string
	// Project is the compose project, always pinned by the caller.
	Project string
	// ComposeFile is the absolute path of the compose file defining the stack.
	ComposeFile string
	// HelperImage streams volume contents. Empty means DefaultHelperImage.
	HelperImage string
}

// CLI drives the docker client with argument vectors only, never a shell.
type CLI struct {
	binary      string
	project     string
	composeFile string
	helperImage string

	// exec runs one argument vector; tests replace it to assert the vector.
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

// rows splits tab-separated output, keeping an empty trailing field.
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

// VolumeProject reports a volume's compose project label; only an unknown volume reads as absent.
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

// VolumeBytes reports a volume's size from a read-only, networkless helper container.
func (c *CLI) VolumeBytes(ctx context.Context, volume string) (int64, error) {
	if !validName(volume) {
		return 0, &InvalidNameError{Kind: "volume", Name: volume}
	}
	out, err := c.run(ctx, "run", "--rm", "--network", "none",
		"-v", volume+":/src:ro", c.helperImage, "du", "-sb", "/src")
	if err != nil {
		return 0, err
	}
	// "du -sb" prints "<bytes>\t<path>"; empty or unparseable output is a DockerOutputError.
	field, _, _ := strings.Cut(strings.TrimSpace(out), "\t")
	size, convErr := strconv.ParseInt(strings.TrimSpace(field), 10, 64)
	if convErr != nil {
		return 0, &DockerOutputError{Command: "run du", Reason: "expected a byte count", Output: out}
	}
	return size, nil
}

// ExportVolume writes a volume's contents to w as a tar, keeping numeric ownership.
func (c *CLI) ExportVolume(ctx context.Context, volume string, w io.Writer) error {
	if !validName(volume) {
		return &InvalidNameError{Kind: "volume", Name: volume}
	}
	return c.stream(ctx, nil, w,
		"run", "--rm", "--network", "none", "-v", volume+":/src:ro", c.helperImage,
		"tar", "-C", "/src", "--numeric-owner", "-cf", "-", ".")
}

// ImportVolume empties the volume, then unpacks the tar stream in r into it.
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

// ImageID resolves an image reference to its id; a missing image is an empty id.
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
	// Empty or whitespace-only output from a successful exit is not an answer:
	// the "unknown" fallback is kept rather than recording "".
	if out, err := c.run(ctx, "version", "--format", "{{.Server.Version}}"); err == nil {
		if s := strings.TrimSpace(out); s != "" {
			v.Docker = s
		}
	}
	if out, err := c.run(ctx, "compose", "version", "--short"); err == nil {
		if s := strings.TrimSpace(out); s != "" {
			v.Compose = s
		}
	}
	return v, nil
}

var _ Docker = (*CLI)(nil)
