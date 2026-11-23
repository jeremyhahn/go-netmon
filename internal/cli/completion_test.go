package cli

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-netmon/pkg/client"
)

// completionAPI serves the three listings the shell completions read.
func completionAPI(t *testing.T) *http.ServeMux {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/devices", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, map[string]any{"devices": []client.Device{
			{ID: 1, Name: "core-01"},
			{ID: 2, Name: "core-02"},
			{ID: 3, Name: "edge-sw-01"},
		}})
	})
	mux.HandleFunc("GET /api/profiles", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, map[string]any{"profiles": []client.Profile{
			{ID: 4, Name: "core"},
			{ID: 5, Name: "switch"},
		}})
	})
	mux.HandleFunc("GET /api/mibs", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, map[string]any{"mibs": []client.MIB{
			{ID: 6, Name: "IF-MIB"},
			{ID: 7, Name: "BRIDGE-MIB"},
		}})
	})
	return mux
}

// failingAPI refuses every listing, standing in for a server that is up but
// cannot answer.
func failingAPI(t *testing.T) http.Handler {
	t.Helper()

	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "database unavailable", http.StatusInternalServerError)
	})
}

// completionFor returns the completion function of a subcommand, failing the
// test if the command does not offer one.
func completionFor(t *testing.T, root *cobra.Command, path ...string) completionFunc {
	t.Helper()

	cmd, _, err := root.Find(path)
	if err != nil {
		t.Fatalf("find %v: %v", path, err)
	}
	if cmd.ValidArgsFunction == nil {
		t.Fatalf("%s has no argument completion", strings.Join(path, " "))
	}
	return cmd.ValidArgsFunction
}

func TestCompletionGeneratesScripts(t *testing.T) {
	for _, shell := range completionShells {
		t.Run(shell, func(t *testing.T) {
			env, out := newFakeEnv(t, http.NotFoundHandler())

			root := &cobra.Command{Use: "netmon"}
			root.AddCommand(newCompletionCommand(env), newDeviceCommand(env))

			if err := runCommand(t, root, "completion", shell); err != nil {
				t.Fatalf("completion %s: %v", shell, err)
			}
			if !strings.Contains(out.String(), "netmon") {
				t.Errorf("%s script does not mention the command:\n%s", shell, out.String())
			}
		})
	}
}

func TestCompletionRejectsUnknownShell(t *testing.T) {
	env, out := newFakeEnv(t, http.NotFoundHandler())

	root := &cobra.Command{Use: "netmon"}
	root.AddCommand(newCompletionCommand(env))

	err := runCommand(t, root, "completion", "powershell")

	var unsupported *UnsupportedShellError
	if !errors.As(err, &unsupported) {
		t.Fatalf("error is %T (%v), want *UnsupportedShellError", err, err)
	}
	if unsupported.Shell != "powershell" {
		t.Errorf("Shell = %q, want powershell", unsupported.Shell)
	}
	// The message has to say which shells do work, or the user is left
	// guessing at the spelling.
	for _, shell := range completionShells {
		if !strings.Contains(unsupported.Error(), shell) {
			t.Errorf("message %q does not offer %s", unsupported.Error(), shell)
		}
	}
	if out.Len() != 0 {
		t.Errorf("a rejected shell must produce no script, got:\n%s", out.String())
	}
}

func TestCompletionOffersServerNames(t *testing.T) {
	env, _ := newFakeEnv(t, completionAPI(t))

	tests := []struct {
		name       string
		complete   completionFunc
		args       []string
		toComplete string
		want       []string
	}{
		{
			name:     "device names",
			complete: completionFor(t, newDeviceCommand(env), "show"),
			want:     []string{"core-01", "core-02", "edge-sw-01"},
		},
		{
			name:       "device names filtered by what was typed",
			complete:   completionFor(t, newDeviceCommand(env), "show"),
			toComplete: "core",
			want:       []string{"core-01", "core-02"},
		},
		{
			name:     "profile names",
			complete: completionFor(t, newProfileCommand(env), "show"),
			want:     []string{"core", "switch"},
		},
		{
			name:     "MIB names",
			complete: completionFor(t, newMIBCommand(env), "show"),
			want:     []string{"IF-MIB", "BRIDGE-MIB"},
		},
		{
			// Names resolve case-insensitively, so completion has to match
			// case-insensitively too or "if-<TAB>" offers nothing.
			name:       "MIB names match regardless of case",
			complete:   completionFor(t, newMIBCommand(env), "show"),
			toComplete: "if-",
			want:       []string{"IF-MIB"},
		},
		{
			name:     "owner first, then MIBs",
			complete: completionFor(t, newDeviceCommand(env), "mib", "add"),
			want:     []string{"core-01", "core-02", "edge-sw-01"},
		},
		{
			name:     "MIBs repeat for every later argument",
			complete: completionFor(t, newDeviceCommand(env), "mib", "add"),
			args:     []string{"core-01", "IF-MIB"},
			want:     []string{"IF-MIB", "BRIDGE-MIB"},
		},
		{
			name:     "profile MIB add completes profiles first",
			complete: completionFor(t, newProfileCommand(env), "mib", "add"),
			want:     []string{"core", "switch"},
		},
		{
			// "device show" takes one argument; nothing follows it.
			name:     "nothing beyond the last argument",
			complete: completionFor(t, newDeviceCommand(env), "show"),
			args:     []string{"core-01"},
			want:     nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, directive := tt.complete(nil, tt.args, tt.toComplete)
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Errorf("completions = %v, want %v", got, tt.want)
			}
			if directive != cobra.ShellCompDirectiveNoFileComp {
				t.Errorf("directive = %v, want NoFileComp", directive)
			}
		})
	}
}

// TestCompletionIsSilentOnFailure is the rule that matters most here: a
// completion runs inside the user's shell, so a failure must produce no
// suggestions and no noise at all.
func TestCompletionIsSilentOnFailure(t *testing.T) {
	t.Run("server error", func(t *testing.T) {
		mux := http.NewServeMux()
		mux.HandleFunc("GET /api/devices", func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "database unavailable", http.StatusInternalServerError)
		})

		env, out := newFakeEnv(t, mux)
		complete := completionFor(t, newDeviceCommand(env), "show")

		names, directive := complete(nil, nil, "")
		if names != nil {
			t.Errorf("completions = %v, want none", names)
		}
		if directive != cobra.ShellCompDirectiveError {
			t.Errorf("directive = %v, want Error", directive)
		}
		if out.Len() != 0 {
			t.Errorf("completion wrote to the shell:\n%s", out.String())
		}
	})

	t.Run("every listing fails silently", func(t *testing.T) {
		// One subtree per lister: each reads a different collection, and a
		// lister that leaks an error leaks it into the shell.
		env, out := newFakeEnv(t, failingAPI(t))

		completions := map[string]completionFunc{
			"device":  completionFor(t, newDeviceCommand(env), "show"),
			"profile": completionFor(t, newProfileCommand(env), "show"),
			"mib":     completionFor(t, newMIBCommand(env), "show"),
		}
		for kind, complete := range completions {
			names, directive := complete(nil, nil, "")
			if names != nil || directive != cobra.ShellCompDirectiveError {
				t.Errorf("%s completion = (%v, %v), want (nil, Error)", kind, names, directive)
			}
		}
		if out.Len() != 0 {
			t.Errorf("completion wrote to the shell:\n%s", out.String())
		}
	})

	t.Run("unusable server URL", func(t *testing.T) {
		env, out := newFakeEnv(t, http.NotFoundHandler())
		env.Opts.Server = ""

		complete := completionFor(t, newDeviceCommand(env), "show")

		names, directive := complete(nil, nil, "")
		if names != nil || directive != cobra.ShellCompDirectiveError {
			t.Errorf("got (%v, %v), want (nil, Error)", names, directive)
		}
		if out.Len() != 0 {
			t.Errorf("completion wrote to the shell:\n%s", out.String())
		}
	})
}
