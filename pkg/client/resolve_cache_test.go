package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// catalog is a fake listing endpoint that counts how often each collection is
// read, so a test can assert on the round trips the cache saved. Its contents
// are swappable, which is how a test creates a resource behind the client's
// back — exactly what an earlier command in the same process does.
type catalog struct {
	mu       sync.Mutex
	devices  []Device
	profiles []Profile
	mibs     []MIB
	lists    map[string]int
	fail     bool
}

func newCatalog() *catalog {
	return &catalog{lists: map[string]int{}}
}

func (c *catalog) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.lists[r.URL.Path]++
	if c.fail {
		http.Error(w, "database unavailable", http.StatusInternalServerError)
		return
	}

	var body any
	switch r.URL.Path {
	case "/api/devices":
		body = map[string]any{"devices": c.devices}
	case "/api/profiles":
		body = map[string]any{"profiles": c.profiles}
	case "/api/mibs":
		body = map[string]any{"mibs": c.mibs}
	default:
		http.NotFound(w, r)
		return
	}
	json.NewEncoder(w).Encode(body)
}

// count reports how many times a collection was listed.
func (c *catalog) count(path string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lists[path]
}

// addMIB imports a module after the fact.
func (c *catalog) addMIB(m MIB) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.mibs = append(c.mibs, m)
}

// removeMIBs empties the module catalog, standing in for a delete.
func (c *catalog) removeMIBs() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.mibs = nil
}

// setFail makes every listing fail.
func (c *catalog) setFail(fail bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fail = fail
}

// newCatalogClient starts a fake API over a catalog and returns a client
// pointed at it.
func newCatalogClient(t *testing.T, cat *catalog) *Client {
	t.Helper()

	srv := httptest.NewServer(cat)
	t.Cleanup(srv.Close)

	c, err := New(srv.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// TestResolveListsOncePerKind is the reason the cache exists: a command naming
// several modules must not fetch the whole catalog once per argument.
func TestResolveListsOncePerKind(t *testing.T) {
	cat := newCatalog()
	cat.mibs = []MIB{
		{ID: 1, Name: "IF-MIB"},
		{ID: 2, Name: "BRIDGE-MIB"},
		{ID: 3, Name: "ENTITY-MIB"},
		{ID: 4, Name: "HOST-RESOURCES-MIB"},
	}
	c := newCatalogClient(t, cat)

	// The last two repeat an already-resolved name, one of them in a different
	// case: both must be answered from memory rather than by listing again.
	refs := []struct {
		ref string
		id  int64
	}{
		{"IF-MIB", 1},
		{"BRIDGE-MIB", 2},
		{"ENTITY-MIB", 3},
		{"HOST-RESOURCES-MIB", 4},
		{"IF-MIB", 1},
		{"if-mib", 1},
	}

	for _, want := range refs {
		got, err := c.ResolveMIB(context.Background(), want.ref)
		if err != nil {
			t.Fatalf("ResolveMIB(%q): %v", want.ref, err)
		}
		if got != want.id {
			t.Errorf("ResolveMIB(%q) = %d, want %d", want.ref, got, want.id)
		}
	}

	if got := cat.count("/api/mibs"); got != 1 {
		t.Errorf("listed the MIB catalog %d times, want 1", got)
	}
}

// TestResolveRelistsOnMiss covers the staleness rule: a name the cache has
// never seen is re-checked against the server before it is believed absent, so
// a resource an earlier command created in this process is still found.
func TestResolveRelistsOnMiss(t *testing.T) {
	cat := newCatalog()
	cat.mibs = []MIB{{ID: 1, Name: "IF-MIB"}}
	c := newCatalogClient(t, cat)

	if _, err := c.ResolveMIB(context.Background(), "IF-MIB"); err != nil {
		t.Fatalf("warming the cache: %v", err)
	}

	// Imported after the cache was filled, and without invalidating it.
	cat.addMIB(MIB{ID: 2, Name: "BRIDGE-MIB"})

	got, err := c.ResolveMIB(context.Background(), "BRIDGE-MIB")
	if err != nil {
		t.Fatalf("ResolveMIB after a late import: %v", err)
	}
	if got != 2 {
		t.Errorf("got id %d, want 2", got)
	}
	if listed := cat.count("/api/mibs"); listed != 2 {
		t.Errorf("listed %d times, want 2 (one to fill the cache, one on the miss)", listed)
	}

	// The refreshed listing is now cached, so the new name costs nothing more.
	if _, err := c.ResolveMIB(context.Background(), "BRIDGE-MIB"); err != nil {
		t.Fatalf("second resolve: %v", err)
	}
	if listed := cat.count("/api/mibs"); listed != 2 {
		t.Errorf("listed %d times after a cached hit, want 2", listed)
	}
}

// TestResolveNotFoundAfterRelist checks the failing half of the same rule: a
// name that is genuinely absent still reports NotFound, having paid for exactly
// one confirming listing.
func TestResolveNotFoundAfterRelist(t *testing.T) {
	cat := newCatalog()
	cat.mibs = []MIB{{ID: 1, Name: "IF-MIB"}}
	c := newCatalogClient(t, cat)

	if _, err := c.ResolveMIB(context.Background(), "IF-MIB"); err != nil {
		t.Fatalf("warming the cache: %v", err)
	}

	_, err := c.ResolveMIB(context.Background(), "NO-SUCH-MIB")
	var notFound *NotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("error is %T (%v), want *NotFoundError", err, err)
	}
	if notFound.Name != "NO-SUCH-MIB" {
		t.Errorf("Name = %q, want NO-SUCH-MIB", notFound.Name)
	}
	if listed := cat.count("/api/mibs"); listed != 2 {
		t.Errorf("listed %d times, want 2", listed)
	}
}

// TestInvalidateForcesRelist covers the case a miss cannot: a name still in the
// cache but no longer on the server. Only invalidation can catch that, which is
// why the CLI invalidates after a create or a delete.
func TestInvalidateForcesRelist(t *testing.T) {
	cat := newCatalog()
	cat.mibs = []MIB{{ID: 1, Name: "IF-MIB"}}
	c := newCatalogClient(t, cat)

	if _, err := c.ResolveMIB(context.Background(), "IF-MIB"); err != nil {
		t.Fatalf("warming the cache: %v", err)
	}
	cat.removeMIBs()

	// Still cached, so the deletion is invisible.
	if _, err := c.ResolveMIB(context.Background(), "IF-MIB"); err != nil {
		t.Fatalf("cached resolve: %v", err)
	}

	c.InvalidateMIBs()

	_, err := c.ResolveMIB(context.Background(), "IF-MIB")
	var notFound *NotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("error is %T (%v), want *NotFoundError after invalidation", err, err)
	}
	if listed := cat.count("/api/mibs"); listed != 2 {
		t.Errorf("listed %d times, want 2", listed)
	}
}

// TestInvalidateIsPerKind checks that invalidating one kind does not throw away
// the others, which would undo the saving for every unrelated argument.
func TestInvalidateIsPerKind(t *testing.T) {
	cat := newCatalog()
	cat.devices = []Device{{ID: 1, Name: "core-01"}}
	cat.profiles = []Profile{{ID: 2, Name: "switch"}}
	cat.mibs = []MIB{{ID: 3, Name: "IF-MIB"}}
	c := newCatalogClient(t, cat)

	ctx := context.Background()
	for _, resolve := range []func() (int64, error){
		func() (int64, error) { return c.ResolveDevice(ctx, "core-01") },
		func() (int64, error) { return c.ResolveProfile(ctx, "switch") },
		func() (int64, error) { return c.ResolveMIB(ctx, "IF-MIB") },
	} {
		if _, err := resolve(); err != nil {
			t.Fatalf("warming the caches: %v", err)
		}
	}

	c.InvalidateDevices()
	for _, resolve := range []func() (int64, error){
		func() (int64, error) { return c.ResolveDevice(ctx, "core-01") },
		func() (int64, error) { return c.ResolveProfile(ctx, "switch") },
		func() (int64, error) { return c.ResolveMIB(ctx, "IF-MIB") },
	} {
		if _, err := resolve(); err != nil {
			t.Fatalf("resolve after invalidating devices: %v", err)
		}
	}

	if got := cat.count("/api/devices"); got != 2 {
		t.Errorf("devices listed %d times, want 2", got)
	}
	if got := cat.count("/api/profiles"); got != 1 {
		t.Errorf("profiles listed %d times, want 1", got)
	}
	if got := cat.count("/api/mibs"); got != 1 {
		t.Errorf("MIBs listed %d times, want 1", got)
	}
}

// TestInvalidateEveryKind exercises each invalidation entry point, since the
// CLI calls a different one after creating a device, a profile and a MIB.
func TestInvalidateEveryKind(t *testing.T) {
	cat := newCatalog()
	cat.devices = []Device{{ID: 1, Name: "core-01"}}
	cat.profiles = []Profile{{ID: 2, Name: "switch"}}
	cat.mibs = []MIB{{ID: 3, Name: "IF-MIB"}}
	c := newCatalogClient(t, cat)

	ctx := context.Background()
	kinds := []struct {
		path       string
		resolve    func() (int64, error)
		invalidate func()
	}{
		{"/api/devices", func() (int64, error) { return c.ResolveDevice(ctx, "core-01") }, c.InvalidateDevices},
		{"/api/profiles", func() (int64, error) { return c.ResolveProfile(ctx, "switch") }, c.InvalidateProfiles},
		{"/api/mibs", func() (int64, error) { return c.ResolveMIB(ctx, "IF-MIB") }, c.InvalidateMIBs},
	}

	for _, kind := range kinds {
		if _, err := kind.resolve(); err != nil {
			t.Fatalf("%s: %v", kind.path, err)
		}
		kind.invalidate()
		if _, err := kind.resolve(); err != nil {
			t.Fatalf("%s after invalidation: %v", kind.path, err)
		}
		if got := cat.count(kind.path); got != 2 {
			t.Errorf("%s listed %d times, want 2", kind.path, got)
		}
	}
}

// TestResolveReportsListingFailures checks that every resolver surfaces the
// server's failure rather than reporting the name missing, which would send an
// operator looking for a record that is there.
func TestResolveReportsListingFailures(t *testing.T) {
	cat := newCatalog()
	cat.setFail(true)
	c := newCatalogClient(t, cat)

	ctx := context.Background()
	resolvers := map[string]func() (int64, error){
		"device":  func() (int64, error) { return c.ResolveDevice(ctx, "core-01") },
		"profile": func() (int64, error) { return c.ResolveProfile(ctx, "switch") },
		"MIB":     func() (int64, error) { return c.ResolveMIB(ctx, "IF-MIB") },
	}

	for kind, resolve := range resolvers {
		_, err := resolve()
		var apiErr *APIError
		if !errors.As(err, &apiErr) {
			t.Errorf("%s: error is %T (%v), want *APIError", kind, err, err)
		}
		var notFound *NotFoundError
		if errors.As(err, &notFound) {
			t.Errorf("%s: a failed listing must not read as a missing record", kind)
		}
	}
}

// TestResolveCachesAmbiguity checks that a name matching several records stays
// ambiguous from the cache, rather than being re-listed or silently resolved.
func TestResolveCachesAmbiguity(t *testing.T) {
	cat := newCatalog()
	cat.devices = []Device{{ID: 1, Name: "core-01"}, {ID: 2, Name: "CORE-01"}}
	c := newCatalogClient(t, cat)

	for i := 0; i < 2; i++ {
		_, err := c.ResolveDevice(context.Background(), "core-01")
		var ambiguous *AmbiguousNameError
		if !errors.As(err, &ambiguous) {
			t.Fatalf("error is %T (%v), want *AmbiguousNameError", err, err)
		}
		if ambiguous.Matches != 2 {
			t.Errorf("Matches = %d, want 2", ambiguous.Matches)
		}
	}
	if got := cat.count("/api/devices"); got != 1 {
		t.Errorf("listed %d times, want 1", got)
	}
}

// TestResolveDoesNotCacheFailure checks that a listing the server could not
// serve leaves nothing behind, so the next resolve tries again instead of
// reporting a resource missing on the strength of a failed request.
func TestResolveDoesNotCacheFailure(t *testing.T) {
	cat := newCatalog()
	cat.mibs = []MIB{{ID: 1, Name: "IF-MIB"}}
	cat.setFail(true)
	c := newCatalogClient(t, cat)

	_, err := c.ResolveMIB(context.Background(), "IF-MIB")
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error is %T (%v), want *APIError", err, err)
	}

	cat.setFail(false)
	got, err := c.ResolveMIB(context.Background(), "IF-MIB")
	if err != nil {
		t.Fatalf("ResolveMIB after the server recovered: %v", err)
	}
	if got != 1 {
		t.Errorf("got id %d, want 1", got)
	}
}

// TestResolveConcurrent runs the resolvers from many goroutines: a Client is
// shared, so the cache must be race-free and must still coalesce the concurrent
// misses into a single listing.
func TestResolveConcurrent(t *testing.T) {
	cat := newCatalog()
	cat.devices = []Device{{ID: 7, Name: "core-01"}}
	cat.mibs = []MIB{{ID: 9, Name: "IF-MIB"}}
	c := newCatalogClient(t, cat)

	const goroutines = 25
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			if id, err := c.ResolveDevice(context.Background(), "core-01"); err != nil || id != 7 {
				t.Errorf("ResolveDevice = (%d, %v), want (7, nil)", id, err)
			}
			if id, err := c.ResolveMIB(context.Background(), "IF-MIB"); err != nil || id != 9 {
				t.Errorf("ResolveMIB = (%d, %v), want (9, nil)", id, err)
			}
		}()
	}
	wg.Wait()

	if got := cat.count("/api/devices"); got != 1 {
		t.Errorf("devices listed %d times under concurrency, want 1", got)
	}
	if got := cat.count("/api/mibs"); got != 1 {
		t.Errorf("MIBs listed %d times under concurrency, want 1", got)
	}
}

// TestResolveByIDSkipsCache checks that a numeric reference still costs nothing
// and does not fill the cache on the way past.
func TestResolveByIDSkipsCache(t *testing.T) {
	cat := newCatalog()
	c := newCatalogClient(t, cat)

	ctx := context.Background()
	resolvers := map[string]func() (int64, error){
		"device":  func() (int64, error) { return c.ResolveDevice(ctx, "42") },
		"profile": func() (int64, error) { return c.ResolveProfile(ctx, "42") },
		"MIB":     func() (int64, error) { return c.ResolveMIB(ctx, "42") },
	}
	for kind, resolve := range resolvers {
		if id, err := resolve(); err != nil || id != 42 {
			t.Errorf("%s = (%d, %v), want (42, nil)", kind, id, err)
		}
	}

	for _, path := range []string{"/api/devices", "/api/profiles", "/api/mibs"} {
		if got := cat.count(path); got != 0 {
			t.Errorf("%s listed %d times for a numeric reference, want 0", path, got)
		}
	}
}
