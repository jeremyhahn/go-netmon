package backup

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

// What actually reaches the Docker binary.
//
// This adapter is the only place in the backup tool that runs anything, and its
// whole safety claim is that it builds argument vectors rather than command
// lines: nothing in a volume or service name can be reinterpreted as syntax,
// because there is no shell to reinterpret it. The exec field exists so that
// claim can be checked without a daemon, and these tests check it -- both the
// exact vectors, and the destructive ones being scoped the way their comments
// promise.
//
// The vectors are asserted verbatim rather than loosely matched. A restore that
// runs the right command against the wrong volume, or empties a directory that
// is not the one it mounted, is exactly the failure this package must not have,
// and a loose assertion would not see it.

// fakeExec records the vectors it is given and answers with canned output.
type fakeExec struct {
	// calls is every vector, in order.
	calls [][]string
	// stdout is keyed by a substring of the vector; the first match answers.
	stdout map[string]string
	// fail is keyed the same way and makes the matching call fail.
	fail map[string]string
	// stdin captures what was piped to the last call that read anything.
	stdin string
}

func (f *fakeExec) fn() execFunc {
	return func(_ context.Context, stdin io.Reader, stdout, stderr io.Writer, _ string, args []string) error {
		f.calls = append(f.calls, args)
		joined := strings.Join(args, " ")

		if stdin != nil {
			piped, _ := io.ReadAll(stdin)
			f.stdin = string(piped)
		}
		for match, message := range f.fail {
			if strings.Contains(joined, match) {
				_, _ = io.WriteString(stderr, message)
				return errors.New("exit status 1")
			}
		}
		for match, out := range f.stdout {
			if strings.Contains(joined, match) {
				_, _ = io.WriteString(stdout, out)
				return nil
			}
		}
		return nil
	}
}

// last returns the most recent vector.
func (f *fakeExec) last() []string {
	if len(f.calls) == 0 {
		return nil
	}
	return f.calls[len(f.calls)-1]
}

// cliWith builds a CLI wired to a fake process runner.
func cliWith(t *testing.T, fake *fakeExec) *CLI {
	t.Helper()

	c, err := NewCLI(CLIOptions{
		Project: "go-netmon", ComposeFile: "/srv/docker-compose.yml",
		Binary: "docker", HelperImage: "busybox:1.36",
	})
	if err != nil {
		t.Fatalf("NewCLI: %v", err)
	}
	c.exec = fake.fn()
	return c
}

func sameVector(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func expectVector(t *testing.T, got, want []string) {
	t.Helper()

	if !sameVector(got, want) {
		t.Errorf("ran\n  %q\nwant\n  %q", got, want)
	}
}

// TestNewCLIRefusesWhatItCannotPin covers construction. A project name that is
// not a name, or no compose file at all, would leave every later vector
// pointing somewhere unintended.
func TestNewCLIRefusesWhatItCannotPin(t *testing.T) {
	t.Parallel()

	if _, err := NewCLI(CLIOptions{Project: "not a name", ComposeFile: "x.yml"}); err == nil {
		t.Error("a project name with a space was accepted")
	}
	if _, err := NewCLI(CLIOptions{Project: "go-netmon", ComposeFile: "  "}); err == nil {
		t.Error("an empty compose file was accepted")
	}

	// The defaults are filled in rather than left empty, or the vector would
	// name no binary at all.
	c, err := NewCLI(CLIOptions{Project: "go-netmon", ComposeFile: "x.yml"})
	if err != nil {
		t.Fatalf("NewCLI: %v", err)
	}
	if c.binary != DefaultDockerBinary || c.helperImage != DefaultHelperImage {
		t.Errorf("defaults not applied: binary=%q helper=%q", c.binary, c.helperImage)
	}
}

// TestComposeVectorsArePinnedToTheProject is the invariant behind every compose
// call: it must name this project and this file, so it can never act on another
// stack that happens to be on the host.
func TestComposeVectorsArePinnedToTheProject(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	prefix := []string{"compose", "-p", "go-netmon", "-f", "/srv/docker-compose.yml"}

	tests := []struct {
		name string
		call func(*CLI) error
		want []string
	}{
		{
			"ComposeVolumes",
			func(c *CLI) error { _, err := c.ComposeVolumes(ctx); return err },
			append(prefix, "config", "--volumes"),
		},
		{
			"RunningServices",
			func(c *CLI) error { _, err := c.RunningServices(ctx); return err },
			append(prefix, "ps", "--services", "--filter", "status=running"),
		},
		{
			"StopServices",
			func(c *CLI) error { return c.StopServices(ctx, []string{"prometheus", "grafana"}) },
			append(prefix, "stop", "prometheus", "grafana"),
		},
		{
			"StartServices",
			func(c *CLI) error { return c.StartServices(ctx, []string{"prometheus"}) },
			append(prefix, "start", "prometheus"),
		},
		{
			"CreateVolumes",
			func(c *CLI) error { return c.CreateVolumes(ctx) },
			append(prefix, "up", "--no-start"),
		},
		{
			"UpDetached",
			func(c *CLI) error { return c.UpDetached(ctx) },
			append(prefix, "up", "-d"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeExec{}
			if err := tt.call(cliWith(t, fake)); err != nil {
				t.Fatalf("%s: %v", tt.name, err)
			}
			expectVector(t, fake.last(), tt.want)
		})
	}
}

// TestServiceActionRefusesANameItCannotVouchFor is the bad-input half. A
// service name is interpolated into a vector, so anything that is not a name is
// refused before it gets there -- and an empty list is a no-op rather than a
// compose call that would act on every service.
func TestServiceActionRefusesANameItCannotVouchFor(t *testing.T) {
	t.Parallel()

	fake := &fakeExec{}
	c := cliWith(t, fake)
	ctx := context.Background()

	if err := c.StopServices(ctx, nil); err != nil {
		t.Errorf("stopping nothing returned %v, want no error and no call", err)
	}
	if len(fake.calls) != 0 {
		t.Errorf("stopping nothing ran %q; an empty list must not become a fleet-wide stop", fake.calls)
	}

	if err := c.StopServices(ctx, []string{"prometheus", "--rm -v /:/host"}); err == nil {
		t.Error("a service name carrying flags was accepted")
	}
	if len(fake.calls) != 0 {
		t.Errorf("a refused name still ran %q", fake.calls)
	}
}

// TestVolumeVectorsScopeTheirReach covers the destructive paths. Each mounts
// exactly one volume, runs with no network, and -- for the read-only ones --
// says so, which is what keeps a helper container's reach to the volume.
func TestVolumeVectorsScopeTheirReach(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	t.Run("export is read-only and networkless", func(t *testing.T) {
		fake := &fakeExec{stdout: map[string]string{"tar": "tar-bytes"}}
		var out bytes.Buffer
		if err := cliWith(t, fake).ExportVolume(ctx, "go-netmon_webui-data", &out); err != nil {
			t.Fatalf("ExportVolume: %v", err)
		}
		expectVector(t, fake.last(), []string{
			"run", "--rm", "--network", "none",
			"-v", "go-netmon_webui-data:/src:ro", "busybox:1.36",
			"tar", "-C", "/src", "--numeric-owner", "-cf", "-", ".",
		})
		if out.String() != "tar-bytes" {
			t.Errorf("export wrote %q, want the stream the container produced", out.String())
		}
	})

	t.Run("import empties before it extracts", func(t *testing.T) {
		fake := &fakeExec{}
		err := cliWith(t, fake).ImportVolume(ctx, "go-netmon_webui-data", strings.NewReader("payload"))
		if err != nil {
			t.Fatalf("ImportVolume: %v", err)
		}
		if len(fake.calls) != 2 {
			t.Fatalf("ran %d commands, want two: empty then extract", len(fake.calls))
		}
		// The delete is scoped to the mount point, with a mindepth that spares
		// the directory itself -- a restore replaces contents, not the volume.
		expectVector(t, fake.calls[0], []string{
			"run", "--rm", "--network", "none",
			"-v", "go-netmon_webui-data:/dst", "busybox:1.36",
			"find", "/dst", "-mindepth", "1", "-delete",
		})
		expectVector(t, fake.calls[1], []string{
			"run", "--rm", "-i", "--network", "none",
			"-v", "go-netmon_webui-data:/dst", "busybox:1.36",
			"tar", "-C", "/dst", "--numeric-owner", "-xf", "-",
		})
		if fake.stdin != "payload" {
			t.Errorf("extract was fed %q, want the archive stream", fake.stdin)
		}
	})

	t.Run("a failed empty does not go on to extract", func(t *testing.T) {
		fake := &fakeExec{fail: map[string]string{"find": "permission denied"}}
		err := cliWith(t, fake).ImportVolume(ctx, "go-netmon_webui-data", strings.NewReader("payload"))
		if err == nil {
			t.Fatal("a failed delete was not reported")
		}
		if len(fake.calls) != 1 {
			t.Errorf("ran %d commands after a failed delete, want to have stopped at one: "+
				"extracting into a directory that could not be emptied merges rather than replaces", len(fake.calls))
		}
	})

	t.Run("a name that is not a name never reaches a vector", func(t *testing.T) {
		fake := &fakeExec{}
		c := cliWith(t, fake)
		bad := "../../etc:/host"

		if err := c.ExportVolume(ctx, bad, io.Discard); err == nil {
			t.Error("ExportVolume accepted a name carrying a path")
		}
		if err := c.ImportVolume(ctx, bad, strings.NewReader("x")); err == nil {
			t.Error("ImportVolume accepted a name carrying a path")
		}
		if _, err := c.VolumeBytes(ctx, bad); err == nil {
			t.Error("VolumeBytes accepted a name carrying a path")
		}
		if _, _, err := c.VolumeProject(ctx, bad); err == nil {
			t.Error("VolumeProject accepted a name carrying a path")
		}
		if len(fake.calls) != 0 {
			t.Errorf("a refused name still ran %q", fake.calls)
		}
	})
}

// TestVolumeProjectSeparatesAbsentFromUnanswered is the distinction a restore
// depends on: a volume that is not there may be created, while a question the
// daemon failed to answer must stop the restore rather than be read as "not
// there" and overwritten.
func TestVolumeProjectSeparatesAbsentFromUnanswered(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	absent := &fakeExec{fail: map[string]string{"volume inspect": "Error: No such volume: whatever"}}
	project, found, err := cliWith(t, absent).VolumeProject(ctx, "whatever")
	if err != nil || found || project != "" {
		t.Errorf("an absent volume reported (%q, %v, %v), want (\"\", false, nil)", project, found, err)
	}

	broken := &fakeExec{fail: map[string]string{"volume inspect": "Cannot connect to the Docker daemon"}}
	if _, _, brokenErr := cliWith(t, broken).VolumeProject(ctx, "whatever"); brokenErr == nil {
		t.Error("a daemon that could not answer was read as the volume being absent, " +
			"which is what would let a restore overwrite it")
	}

	known := &fakeExec{stdout: map[string]string{"volume inspect": "go-netmon\n"}}
	project, found, err = cliWith(t, known).VolumeProject(ctx, "go-netmon_webui-data")
	if err != nil || !found || project != "go-netmon" {
		t.Errorf("got (%q, %v, %v), want (\"go-netmon\", true, nil)", project, found, err)
	}

	// An unlabeled volume exists but belongs to no project, which the template
	// renders as "<no value>" rather than as an empty string.
	unlabeled := &fakeExec{stdout: map[string]string{"volume inspect": "<no value>\n"}}
	project, found, err = cliWith(t, unlabeled).VolumeProject(ctx, "stray")
	if err != nil || !found || project != "" {
		t.Errorf("an unlabeled volume reported (%q, %v, %v), want (\"\", true, nil)", project, found, err)
	}
}

// TestVolumeBytesReadsTheHelpersAnswer covers the size probe and its bad-output
// case: a manifest that records a wrong size is worse than one that records none.
func TestVolumeBytesReadsTheHelpersAnswer(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	fake := &fakeExec{stdout: map[string]string{"du": "25473024\t/src\n"}}
	size, err := cliWith(t, fake).VolumeBytes(ctx, "go-netmon_webui-data")
	if err != nil {
		t.Fatalf("VolumeBytes: %v", err)
	}
	if size != 25473024 {
		t.Errorf("size = %d, want 25473024", size)
	}
	expectVector(t, fake.last(), []string{
		"run", "--rm", "--network", "none",
		"-v", "go-netmon_webui-data:/src:ro", "busybox:1.36",
		"du", "-sb", "/src",
	})

	garbled := &fakeExec{stdout: map[string]string{"du": "not a number\t/src\n"}}
	if _, err := cliWith(t, garbled).VolumeBytes(ctx, "go-netmon_webui-data"); err == nil {
		t.Error("output that is not a byte count was accepted as a size")
	}
}

// TestForeignContainersSpotsWhatThisComposeFileDidNotCreate covers the check
// that stops a restore from stopping somebody else's containers.
func TestForeignContainersSpotsWhatThisComposeFileDidNotCreate(t *testing.T) {
	t.Parallel()

	fake := &fakeExec{stdout: map[string]string{
		"ps -a": "go-netmon-webui\t/srv/docker-compose.yml\n" +
			"someone-elses\t/home/other/compose.yml\n",
	}}
	foreign, err := cliWith(t, fake).ForeignContainers(context.Background())
	if err != nil {
		t.Fatalf("ForeignContainers: %v", err)
	}
	if len(foreign) != 1 || foreign[0].Name != "someone-elses" {
		t.Fatalf("found %+v, want only the container from another compose file", foreign)
	}

	// Output that does not carry both fields is a failure rather than a guess:
	// deciding what to stop from a half-read line is how the wrong thing gets
	// stopped.
	malformed := &fakeExec{stdout: map[string]string{"ps -a": "no-tab-here\n"}}
	if _, err := cliWith(t, malformed).ForeignContainers(context.Background()); err == nil {
		t.Error("a row missing its label was accepted")
	}
}

// TestComposeServicesReadsBindsAndSortsThem covers the parse the MIB-tree
// backup depends on: it is how the host path behind /app/mibs is discovered.
func TestComposeServicesReadsBindsAndSortsThem(t *testing.T) {
	t.Parallel()

	fake := &fakeExec{stdout: map[string]string{"config --format json": `{
      "services": {
        "webui": {
          "image": "go-netmon-webui:latest",
          "volumes": [
            {"type": "bind", "source": "/srv/mibs", "target": "/app/mibs"},
            {"type": "volume", "source": "webui-data", "target": "/data"}
          ]
        },
        "alpha": {"image": "alpine", "volumes": []}
      }
    }`}}

	services, err := cliWith(t, fake).ComposeServices(context.Background())
	if err != nil {
		t.Fatalf("ComposeServices: %v", err)
	}
	if len(services) != 2 {
		t.Fatalf("found %d services, want 2", len(services))
	}
	// Sorted, so a manifest and a plan do not reorder between runs.
	if services[0].Name != "alpha" || services[1].Name != "webui" {
		t.Errorf("services came back as %q, %q; want them sorted", services[0].Name, services[1].Name)
	}
	// Only the bind is a bind: a named volume is backed up as a volume, and
	// treating it as a host path would put the wrong thing in the archive.
	webui := services[1]
	if len(webui.Binds) != 1 || webui.Binds[0].Source != "/srv/mibs" || webui.Binds[0].Target != "/app/mibs" {
		t.Errorf("binds = %+v, want only the /app/mibs bind", webui.Binds)
	}

	broken := &fakeExec{stdout: map[string]string{"config --format json": "not json"}}
	if _, err := cliWith(t, broken).ComposeServices(context.Background()); err == nil {
		t.Error("output that is not JSON was accepted as a compose config")
	}
}

// TestImageIDAndVersionsDegradeQuietly covers the two probes whose failure is
// not a reason to abandon a backup: an image the host does not have, and a
// runtime that will not report its version, are facts about the host.
func TestImageIDAndVersionsDegradeQuietly(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	fake := &fakeExec{stdout: map[string]string{
		"image inspect":    "sha256:abc123\n",
		"version --format": "27.1.1\n",
		"compose version":  "v2.29.0\n",
	}}
	c := cliWith(t, fake)

	id, err := c.ImageID(ctx, "go-netmon-webui:latest")
	if err != nil || id != "sha256:abc123" {
		t.Errorf("ImageID = (%q, %v), want the resolved id", id, err)
	}
	if empty, emptyErr := c.ImageID(ctx, "   "); emptyErr != nil || empty != "" {
		t.Errorf("an empty reference returned (%q, %v), want (\"\", nil) without a call", empty, emptyErr)
	}

	versions, err := c.Versions(ctx)
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}
	if versions.Docker != "27.1.1" || versions.Compose != "v2.29.0" {
		t.Errorf("versions = %+v, want both reported", versions)
	}

	// A host with no such image, and a runtime that will not answer.
	missing := &fakeExec{fail: map[string]string{"image inspect": "No such image", "version": "not found"}}
	c = cliWith(t, missing)
	if ghost, ghostErr := c.ImageID(ctx, "ghost:latest"); ghostErr != nil || ghost != "" {
		t.Errorf("a missing image returned (%q, %v), want (\"\", nil): the manifest records what was there", ghost, ghostErr)
	}
	versions, err = c.Versions(ctx)
	if err != nil {
		t.Fatalf("Versions with an unhelpful runtime: %v", err)
	}
	if versions.Docker != "unknown" || versions.Compose != "unknown" {
		t.Errorf("versions = %+v, want both 'unknown' rather than empty", versions)
	}
}

// TestVolumeNamesAndDockerErrorCarryTheirContext covers the listing and the
// error a failed call produces: without the stderr the runtime wrote, an
// operator gets an exit status and nothing to act on.
func TestVolumeNamesAndDockerErrorCarryTheirContext(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	fake := &fakeExec{stdout: map[string]string{"volume ls": "go-netmon_webui-data\n\ngo-netmon_prometheus-data\n"}}
	names, err := cliWith(t, fake).VolumeNames(ctx)
	if err != nil {
		t.Fatalf("VolumeNames: %v", err)
	}
	if len(names) != 2 {
		t.Fatalf("found %d volumes, want 2 with the blank line dropped: %q", len(names), names)
	}
	expectVector(t, fake.last(), []string{"volume", "ls", "--format", "{{.Name}}"})

	broken := &fakeExec{fail: map[string]string{"volume ls": "Cannot connect to the Docker daemon at unix:///var/run/docker.sock"}}
	_, err = cliWith(t, broken).VolumeNames(ctx)
	if err == nil {
		t.Fatal("a failed listing was reported as success")
	}
	var dockerErr *DockerError
	if !errors.As(err, &dockerErr) {
		t.Fatalf("error was %T, want a *DockerError carrying the command and stderr", err)
	}
	if !strings.Contains(dockerErr.Error(), "Cannot connect") {
		t.Errorf("error reads %q, which does not carry what the runtime said", dockerErr.Error())
	}
}

// TestRunProcessTakesAVectorNotACommandLine proves the claim the rest of this
// package rests on.
//
// Everything above asserts which vector would be handed to the process. This
// asserts what the process does with it: the arguments reach the program
// verbatim, so a volume or service name carrying shell syntax is data and not
// syntax. os/exec passes the vector to execve; there is no shell to reinterpret
// anything, and this is the test that would fail if that ever changed.
//
// It runs /bin/echo and /bin/cat: no daemon, nothing written, nothing on the
// host touched.
func TestRunProcessTakesAVectorNotACommandLine(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	hostile := "; rm -rf / #"

	err := runProcess(context.Background(), nil, &stdout, &stderr, "/bin/echo", []string{hostile})
	if err != nil {
		t.Fatalf("runProcess: %v", err)
	}
	if got := strings.TrimRight(stdout.String(), "\n"); got != hostile {
		t.Errorf("echo printed %q, want the argument verbatim: anything else means it was interpreted", got)
	}

	// A failing program is reported as an error, and its stderr reaches the
	// caller's writer rather than being swallowed. Reading a file that is not
	// there does both, without naming a shell -- which the containment test in
	// this package forbids, and rightly.
	stdout.Reset()
	stderr.Reset()
	if err := runProcess(context.Background(), nil,
		&stdout, &stderr, "/bin/cat", []string{"/nonexistent-by-design"}); err == nil {
		t.Error("a program exiting non-zero was reported as success")
	}
	if !strings.Contains(stderr.String(), "nonexistent-by-design") {
		t.Errorf("stderr = %q, want what the program wrote", stderr.String())
	}

	// Stdin is wired through, which is how a volume import is fed its tar.
	stdout.Reset()
	if err := runProcess(context.Background(), strings.NewReader("piped"),
		&stdout, &stderr, "/bin/cat", nil); err != nil {
		t.Fatalf("runProcess with stdin: %v", err)
	}
	if stdout.String() != "piped" {
		t.Errorf("cat produced %q, want the stdin it was given", stdout.String())
	}
}

// TestAFailedListingIsNotAnEmptyOne is the distinction every caller depends on.
//
// A compose file that defines no volumes and a compose call that failed both
// produce no names. Reading the second as the first would let a backup report
// success having archived nothing, which is the worst outcome this tool has.
func TestAFailedListingIsNotAnEmptyOne(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	tests := []struct {
		name string
		call func(*CLI) ([]string, error)
	}{
		{"ComposeVolumes", func(c *CLI) ([]string, error) { return c.ComposeVolumes(ctx) }},
		{"RunningServices", func(c *CLI) ([]string, error) { return c.RunningServices(ctx) }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			broken := &fakeExec{fail: map[string]string{"compose": "no configuration file provided"}}
			names, err := tt.call(cliWith(t, broken))
			if err == nil {
				t.Fatalf("%s reported success when the call failed", tt.name)
			}
			if names != nil {
				t.Errorf("%s returned %q alongside its error; a partial answer invites use", tt.name, names)
			}

			// And the genuinely empty case is empty, not an error.
			quiet := &fakeExec{stdout: map[string]string{"compose": "\n  \n"}}
			names, err = tt.call(cliWith(t, quiet))
			if err != nil {
				t.Errorf("%s treated an empty answer as a failure: %v", tt.name, err)
			}
			if len(names) != 0 {
				t.Errorf("%s found %q in blank output", tt.name, names)
			}
		})
	}
}
