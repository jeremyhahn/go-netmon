package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
)

// deviceListServer returns a client whose /api/devices endpoint serves the
// given devices.
func deviceListServer(t *testing.T, devices ...Device) *Client {
	t.Helper()
	return newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"devices": devices})
	})
}

func TestResolveDevice_ByName(t *testing.T) {
	c := deviceListServer(t,
		Device{ID: 1, Name: "core-01"},
		Device{ID: 2, Name: "core-02"},
	)

	tests := []struct {
		name string
		ref  string
		want int64
	}{
		{"exact name", "core-01", 1},
		{"second device", "core-02", 2},
		// Shells and humans are inconsistent about case; device names are not
		// case-sensitive identifiers to an operator.
		{"case insensitive", "Core-01", 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := c.ResolveDevice(context.Background(), tt.ref)
			if err != nil {
				t.Fatalf("ResolveDevice(%q): %v", tt.ref, err)
			}
			if got != tt.want {
				t.Errorf("got id %d, want %d", got, tt.want)
			}
		})
	}
}

func TestResolveDevice_ByID(t *testing.T) {
	// A numeric reference must not require a round trip to list devices.
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Error("server must not be called for a numeric reference")
	})

	got, err := c.ResolveDevice(context.Background(), "42")
	if err != nil {
		t.Fatalf("ResolveDevice: %v", err)
	}
	if got != 42 {
		t.Errorf("got id %d, want 42", got)
	}
}

func TestResolveDevice_NotFound(t *testing.T) {
	c := deviceListServer(t, Device{ID: 1, Name: "core-01"})

	_, err := c.ResolveDevice(context.Background(), "no-such-device")
	if err == nil {
		t.Fatal("expected an error")
	}
	var notFound *NotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("error is %T, want *NotFoundError", err)
	}
	if notFound.Name != "no-such-device" {
		t.Errorf("Name = %q, want no-such-device", notFound.Name)
	}
}

func TestResolveDevice_Ambiguous(t *testing.T) {
	// Two devices differing only by case: the CLI must refuse rather than
	// silently pick one.
	c := deviceListServer(t,
		Device{ID: 1, Name: "core-01"},
		Device{ID: 2, Name: "CORE-01"},
	)

	_, err := c.ResolveDevice(context.Background(), "core-01")
	if err == nil {
		t.Fatal("expected an error")
	}
	var ambiguous *AmbiguousNameError
	if !errors.As(err, &ambiguous) {
		t.Fatalf("error is %T, want *AmbiguousNameError", err)
	}
	if ambiguous.Matches != 2 {
		t.Errorf("Matches = %d, want 2", ambiguous.Matches)
	}
}

func TestResolveDevice_ServerError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})

	if _, err := c.ResolveDevice(context.Background(), "core-01"); err == nil {
		t.Fatal("expected an error when the device list cannot be fetched")
	}
}

func TestResolveProfile(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"profiles": []Profile{{ID: 5, Name: "asr1000-core"}},
		})
	})

	got, err := c.ResolveProfile(context.Background(), "asr1000-core")
	if err != nil {
		t.Fatalf("ResolveProfile: %v", err)
	}
	if got != 5 {
		t.Errorf("got id %d, want 5", got)
	}

	if _, err := c.ResolveProfile(context.Background(), "missing"); err == nil {
		t.Error("expected an error for an unknown profile")
	}
}

func TestResolveMIB(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"mibs": []MIB{{ID: 9, Name: "IF-MIB"}},
		})
	})

	// MIB module names are conventionally upper-case but users type either.
	for _, ref := range []string{"IF-MIB", "if-mib"} {
		got, err := c.ResolveMIB(context.Background(), ref)
		if err != nil {
			t.Fatalf("ResolveMIB(%q): %v", ref, err)
		}
		if got != 9 {
			t.Errorf("ResolveMIB(%q) = %d, want 9", ref, got)
		}
	}

	if _, err := c.ResolveMIB(context.Background(), "NO-SUCH-MIB"); err == nil {
		t.Error("expected an error for an unknown MIB")
	}
}

func TestParseID(t *testing.T) {
	tests := []struct {
		input  string
		want   int64
		wantOK bool
	}{
		{"1", 1, true},
		{"42", 42, true},
		// Zero and negatives are not valid IDs, so they fall through to a
		// name lookup rather than producing a bogus request.
		{"0", 0, false},
		{"-1", 0, false},
		{"core-01", 0, false},
		{"", 0, false},
		{"1.5", 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, ok := parseID(tt.input)
			if ok != tt.wantOK || got != tt.want {
				t.Errorf("parseID(%q) = (%d, %v), want (%d, %v)",
					tt.input, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}
