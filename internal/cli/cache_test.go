package cli

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-netmon/pkg/client"
)

// countingAPI is a fake management API that records how often each collection
// was listed and lets records be created and deleted, which is what the
// resolver cache has to stay honest about.
type countingAPI struct {
	mu       sync.Mutex
	lists    map[string]int
	devices  []client.Device
	profiles []client.Profile
	mibs     []client.MIB
	nextID   int64
}

func newCountingAPI() *countingAPI {
	return &countingAPI{lists: map[string]int{}, nextID: 100}
}

// count reports how many times a collection was listed.
func (a *countingAPI) count(path string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lists[path]
}

// record notes a listing.
func (a *countingAPI) record(path string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.lists[path]++
}

// profile returns the stored profile with an ID, if it still exists.
func (a *countingAPI) profile(id int64) (client.Profile, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, p := range a.profiles {
		if p.ID == id {
			return p, true
		}
	}
	return client.Profile{}, false
}

// addProfile stores a new profile and returns it with its assigned ID.
func (a *countingAPI) addProfile(p client.Profile) client.Profile {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.nextID++
	p.ID = a.nextID
	a.profiles = append(a.profiles, p)
	return p
}

// deleteProfile removes a profile.
func (a *countingAPI) deleteProfile(id int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	kept := a.profiles[:0]
	for _, p := range a.profiles {
		if p.ID != id {
			kept = append(kept, p)
		}
	}
	a.profiles = kept
}

func (a *countingAPI) mux(t *testing.T) *http.ServeMux {
	t.Helper()

	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/devices", func(w http.ResponseWriter, _ *http.Request) {
		a.record("devices")
		a.mu.Lock()
		devices := a.devices
		a.mu.Unlock()
		respondJSON(t, w, map[string]any{"devices": devices})
	})
	mux.HandleFunc("GET /api/mibs", func(w http.ResponseWriter, _ *http.Request) {
		a.record("mibs")
		a.mu.Lock()
		mibs := a.mibs
		a.mu.Unlock()
		respondJSON(t, w, map[string]any{"mibs": mibs})
	})
	mux.HandleFunc("GET /api/profiles", func(w http.ResponseWriter, _ *http.Request) {
		a.record("profiles")
		a.mu.Lock()
		profiles := a.profiles
		a.mu.Unlock()
		respondJSON(t, w, map[string]any{"profiles": profiles})
	})

	mux.HandleFunc("POST /api/profiles", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(t, w, a.addProfile(client.Profile{Name: "core"}))
	})
	mux.HandleFunc("GET /api/profiles/{id}", func(w http.ResponseWriter, r *http.Request) {
		profile, ok := a.profile(pathID(t, r))
		if !ok {
			http.Error(w, "profile not found", http.StatusNotFound)
			return
		}
		respondJSON(t, w, profile)
	})
	mux.HandleFunc("DELETE /api/profiles/{id}", func(w http.ResponseWriter, r *http.Request) {
		a.deleteProfile(pathID(t, r))
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /api/profiles/{id}/devices", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := a.profile(pathID(t, r)); !ok {
			http.Error(w, "profile not found", http.StatusNotFound)
			return
		}
		respondJSON(t, w, map[string]any{"devices": []client.Device{}})
	})
	mux.HandleFunc("POST /api/profiles/{id}/mibs", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /api/devices/{id}/mibs", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	return mux
}

// pathID reads the {id} wildcard of a request path.
func pathID(t *testing.T, r *http.Request) int64 {
	t.Helper()

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		t.Fatalf("unparsable id %q in %s", r.PathValue("id"), r.URL.Path)
	}
	return id
}

// TestResolverListsOncePerCommand is the user-visible point of the cache: a
// command naming four modules must fetch the catalog once, not four times.
func TestResolverListsOncePerCommand(t *testing.T) {
	api := newCountingAPI()
	api.profiles = []client.Profile{{ID: 1, Name: "core"}}
	api.mibs = []client.MIB{
		{ID: 10, Name: "IF-MIB"},
		{ID: 11, Name: "BRIDGE-MIB"},
		{ID: 12, Name: "ENTITY-MIB"},
		{ID: 13, Name: "HOST-RESOURCES-MIB"},
	}

	env, out := newFakeEnv(t, api.mux(t))
	err := runCommand(t, newProfileCommand(env),
		"mib", "add", "core", "IF-MIB", "BRIDGE-MIB", "ENTITY-MIB", "HOST-RESOURCES-MIB")
	if err != nil {
		t.Fatalf("profile mib add: %v", err)
	}

	if got := api.count("mibs"); got != 1 {
		t.Errorf("listed the MIB catalog %d times for four arguments, want 1", got)
	}
	if got := api.count("profiles"); got != 1 {
		t.Errorf("listed profiles %d times, want 1", got)
	}
	if added := strings.Count(out.String(), "Added"); added != 4 {
		t.Errorf("reported %d additions, want 4:\n%s", added, out.String())
	}
}

// TestResolverCacheSpansCommands checks that the saving survives across the
// commands of one process, which is what a --refresh loop and any embedding of
// the CLI depend on.
func TestResolverCacheSpansCommands(t *testing.T) {
	api := newCountingAPI()
	api.profiles = []client.Profile{{ID: 1, Name: "core"}}

	env, _ := newFakeEnv(t, api.mux(t))
	for i := 0; i < 3; i++ {
		if err := runCommand(t, newProfileCommand(env), "devices", "core"); err != nil {
			t.Fatalf("profile devices: %v", err)
		}
	}

	if got := api.count("profiles"); got != 1 {
		t.Errorf("listed profiles %d times across three commands, want 1", got)
	}
}

// TestResolverSeesResourceCreatedInSameRun covers the create path: a profile
// created by one command must be resolvable by the next, which is why creating
// invalidates the cache.
func TestResolverSeesResourceCreatedInSameRun(t *testing.T) {
	api := newCountingAPI()
	api.profiles = []client.Profile{{ID: 1, Name: "edge"}}
	api.mibs = []client.MIB{{ID: 10, Name: "IF-MIB"}}

	env, _ := newFakeEnv(t, api.mux(t))

	// Warm the cache on the profiles that exist now.
	if err := runCommand(t, newProfileCommand(env), "devices", "edge"); err != nil {
		t.Fatalf("profile devices: %v", err)
	}
	if err := runCommand(t, newProfileCommand(env), "create", "--name", "core"); err != nil {
		t.Fatalf("profile create: %v", err)
	}
	if err := runCommand(t, newProfileCommand(env), "mib", "add", "core", "IF-MIB"); err != nil {
		t.Fatalf("resolving a profile created in this run: %v", err)
	}
}

// TestResolverSeesResourceDeletedInSameRun covers the case a cache miss cannot:
// a name still in the cache but deleted since. Without invalidation the resolve
// would succeed and the server would answer 404 to whatever came next, which
// tells the operator nothing about the name they typed.
func TestResolverSeesResourceDeletedInSameRun(t *testing.T) {
	api := newCountingAPI()
	api.profiles = []client.Profile{{ID: 1, Name: "core"}}

	env, _ := newFakeEnv(t, api.mux(t))

	if err := runCommand(t, newProfileCommand(env), "devices", "core"); err != nil {
		t.Fatalf("profile devices: %v", err)
	}
	if err := runCommand(t, newProfileCommand(env), "delete", "core", "--force"); err != nil {
		t.Fatalf("profile delete: %v", err)
	}

	err := runCommand(t, newProfileCommand(env), "devices", "core")
	var notFound *client.NotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("error is %T (%v), want *client.NotFoundError", err, err)
	}
	if notFound.Name != "core" {
		t.Errorf("Name = %q, want core", notFound.Name)
	}
}

// TestResolverRelistsForAnUnknownName checks the other half of the rule at the
// command level: a name absent from the cache is confirmed against the server
// before the command gives up.
func TestResolverRelistsForAnUnknownName(t *testing.T) {
	api := newCountingAPI()
	api.profiles = []client.Profile{{ID: 1, Name: "core"}}

	env, _ := newFakeEnv(t, api.mux(t))
	if err := runCommand(t, newProfileCommand(env), "devices", "core"); err != nil {
		t.Fatalf("profile devices: %v", err)
	}

	// Created behind the CLI's back, as another operator or the web UI would.
	api.addProfile(client.Profile{Name: "edge"})

	if err := runCommand(t, newProfileCommand(env), "devices", "edge"); err != nil {
		t.Fatalf("a profile created elsewhere must still resolve: %v", err)
	}
	if got := api.count("profiles"); got != 2 {
		t.Errorf("listed profiles %d times, want 2 (one to fill the cache, one on the miss)", got)
	}
}
