package client

import (
	"context"
	"strconv"
	"strings"
	"sync"
)

// ResolveDevice turns a user-supplied device reference into an ID. A reference
// is either a numeric ID or a device name; names are matched case-insensitively
// so "core-01" and "Core-01" both work from a shell.
func (c *Client) ResolveDevice(ctx context.Context, ref string) (int64, error) {
	return resolveRef(ctx, &c.deviceNames, "device", ref, c.ListDevices,
		func(d Device) (string, int64) { return d.Name, d.ID })
}

// ResolveProfile turns a profile reference into an ID.
func (c *Client) ResolveProfile(ctx context.Context, ref string) (int64, error) {
	return resolveRef(ctx, &c.profileNames, "profile", ref, c.ListProfiles,
		func(p Profile) (string, int64) { return p.Name, p.ID })
}

// ResolveMIB turns a MIB reference into an ID. MIB module names are
// conventionally upper-case, but matching stays case-insensitive.
func (c *Client) ResolveMIB(ctx context.Context, ref string) (int64, error) {
	listAll := func(ctx context.Context) ([]MIB, error) { return c.ListMIBs(ctx, false) }
	return resolveRef(ctx, &c.mibNames, "MIB", ref, listAll,
		func(m MIB) (string, int64) { return m.Name, m.ID })
}

// resolveRef turns a reference into an ID for one resource kind: a bare number
// passes straight through, and anything else goes to that kind's name cache,
// which lists via list and projects each record with named.
func resolveRef[T any](
	ctx context.Context,
	cache *nameCache,
	kind, ref string,
	list func(context.Context) ([]T, error),
	named func(T) (string, int64),
) (int64, error) {
	if id, ok := parseID(ref); ok {
		return id, nil
	}

	return cache.lookup(ctx, kind, ref, func(ctx context.Context) ([]namedID, error) {
		items, err := list(ctx)
		if err != nil {
			return nil, err
		}
		pairs := make([]namedID, len(items))
		for i, item := range items {
			name, id := named(item)
			pairs[i] = namedID{name: name, id: id}
		}
		return pairs, nil
	})
}

// InvalidateDevices drops the cached device name→ID mapping. A command that
// creates, renames or deletes a device calls this so a later resolve in the
// same process sees the change.
func (c *Client) InvalidateDevices() { c.deviceNames.invalidate() }

// InvalidateProfiles drops the cached profile name→ID mapping.
func (c *Client) InvalidateProfiles() { c.profileNames.invalidate() }

// InvalidateMIBs drops the cached MIB name→ID mapping.
func (c *Client) InvalidateMIBs() { c.mibNames.invalidate() }

// namedID pairs a resource's name with its ID, which is all a name lookup needs
// from a listing.
type namedID struct {
	name string
	id   int64
}

// lister fetches every name→ID pair of one resource kind.
type lister func(context.Context) ([]namedID, error)

// nameCache memoizes one List endpoint's name→ID mapping for the life of a
// Client.
//
// Resolving a name costs a full listing, and one command resolves many names —
// "netmon profile mib add core IF-MIB BRIDGE-MIB ENTITY-MIB" resolves four — so
// without a cache a server holding a few hundred MIB modules serves the whole
// catalog once per argument.
//
// The cache never reports "not found" from memory: a miss re-lists once before
// concluding the name does not exist, so a resource created after the cache was
// filled is still found. That leaves only a stale hit, which is why the commands
// that create, rename or delete something invalidate the kind they changed.
type nameCache struct {
	// mu is held across the listing rather than only across the map write, so
	// concurrent resolves of the same kind coalesce into one request instead of
	// racing to issue several.
	mu     sync.Mutex
	byName map[string][]int64
	loaded bool
}

// lookup returns the ID stored under ref, listing when it has to. kind names the
// resource in any error, and list fetches the mapping.
func (c *nameCache) lookup(ctx context.Context, kind, ref string, list lister) (int64, error) {
	key := strings.ToLower(ref)

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.loaded {
		if ids, ok := c.byName[key]; ok {
			return single(kind, ref, ids)
		}
	}

	// Either nothing is cached yet or the name is absent from what is. Both
	// need a listing: a name the cache has never seen may have been created
	// since it was filled, and refusing it from memory would make a resource an
	// earlier command in this process created invisible to the next one.
	if err := c.reload(ctx, list); err != nil {
		return 0, err
	}
	return single(kind, ref, c.byName[key])
}

// reload replaces the cached mapping. The caller holds mu.
func (c *nameCache) reload(ctx context.Context, list lister) error {
	named, err := list(ctx)
	if err != nil {
		return err
	}

	byName := make(map[string][]int64, len(named))
	for _, n := range named {
		key := strings.ToLower(n.name)
		byName[key] = append(byName[key], n.id)
	}
	c.byName, c.loaded = byName, true
	return nil
}

// invalidate drops the cached mapping so the next lookup lists again.
func (c *nameCache) invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.byName, c.loaded = nil, false
}

// parseID reports whether ref is a bare numeric ID.
func parseID(ref string) (int64, bool) {
	id, err := strconv.ParseInt(ref, 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

// single collapses a match list to one ID, distinguishing "none" from
// "several" so the caller can tell the user which problem they have.
func single(kind, ref string, matches []int64) (int64, error) {
	switch len(matches) {
	case 0:
		return 0, &NotFoundError{Kind: kind, Name: ref}
	case 1:
		return matches[0], nil
	default:
		return 0, &AmbiguousNameError{Kind: kind, Name: ref, Matches: len(matches)}
	}
}
