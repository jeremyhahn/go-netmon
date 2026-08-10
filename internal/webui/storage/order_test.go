package storage

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// Every list this package returns is read by a human or fed to a UI that builds
// tabs from it. Two of them — a device's MIBs, directly and through its
// profiles — were assembled through a map, so they came back in a different
// order on every call: four consecutive requests gave four different orders,
// and the dashboard's MIB tabs and the tab that opened by default moved with
// them. The rest were stable but ordered by record ID, which is import order.
//
// These tests state one rule for all of them: by name, and the same on every
// call.

// orderFixture fills a store with records whose names are deliberately not in
// the order they are created, so a test cannot pass by accident on a store that
// happens to return insertion order.
type orderFixture struct {
	store     *Storage
	deviceID  int64
	profileID int64
}

// mibNames and deviceNames are written in creation order; their sorted order is
// different, which is the point.
func mibNames() []string {
	return []string{"UDP-MIB", "CISCO-BGP4-MIB", "IF-MIB", "SNMPv2-MIB", "ENTITY-MIB", "TCP-MIB"}
}

func newOrderFixture(t *testing.T) *orderFixture {
	t.Helper()

	store, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)

	ctx := context.Background()

	device := &Device{Name: "zulu-router", Address: "198.51.100.101", SNMPVersion: "v2c"}
	if err := store.CreateDevice(ctx, device); err != nil {
		t.Fatalf("create device: %v", err)
	}
	for _, name := range []string{"alpha-switch", "mike-router"} {
		if err := store.CreateDevice(ctx, &Device{Name: name, Address: "198.51.100.102", SNMPVersion: "v2c"}); err != nil {
			t.Fatalf("create device %s: %v", name, err)
		}
	}

	profile := &DeviceProfile{Name: "Core Routers"}
	if err := store.CreateDeviceProfile(ctx, profile); err != nil {
		t.Fatalf("create profile: %v", err)
	}
	for _, name := range []string{"Access Switches", "Border Routers"} {
		if err := store.CreateDeviceProfile(ctx, &DeviceProfile{Name: name}); err != nil {
			t.Fatalf("create profile %s: %v", name, err)
		}
	}
	if err := store.AssignProfileToDevice(ctx, device.ID, profile.ID); err != nil {
		t.Fatalf("assign profile: %v", err)
	}

	// Half the MIBs go straight onto the device and half onto its profile, so
	// the combined list is genuinely merged from two sources.
	for i, name := range mibNames() {
		m := &MIB{Name: name, FilePath: "/app/data/mibs/uploaded/" + name + ".txt"}
		if err := store.CreateMIB(ctx, m); err != nil {
			t.Fatalf("create MIB %s: %v", name, err)
		}
		if i%2 == 0 {
			if err := store.AssignMIBToDevice(ctx, device.ID, m.ID); err != nil {
				t.Fatalf("assign %s to device: %v", name, err)
			}
			continue
		}
		if err := store.AssignMIBToProfile(ctx, profile.ID, m.ID); err != nil {
			t.Fatalf("assign %s to profile: %v", name, err)
		}
	}

	return &orderFixture{store: store, deviceID: device.ID, profileID: profile.ID}
}

// assertStableAndSorted calls list twelve times and requires every call to
// return the same names, in ascending order.
//
// Repeating the call is what makes the test meaningful: Go randomizes map
// iteration per range statement, so a single call is as likely as not to come
// back sorted by chance.
func assertStableAndSorted(t *testing.T, what string, list func() []string) {
	t.Helper()

	first := list()
	if len(first) < 2 {
		t.Fatalf("%s returned %d records; the fixture is meant to give it several", what, len(first))
	}
	if !slices.IsSorted(first) {
		t.Errorf("%s returned %v, which is not in name order", what, first)
	}

	for call := 2; call <= 12; call++ {
		got := list()
		if !slices.Equal(got, first) {
			t.Fatalf("%s returned %v on call %d, having returned %v on call 1", what, got, call, first)
		}
	}
}

func namesOfMIBs(mibs []MIB) []string {
	names := make([]string, len(mibs))
	for i, m := range mibs {
		names[i] = m.Name
	}
	return names
}

func TestDeviceMIBListsAreStableAndSorted(t *testing.T) {
	f := newOrderFixture(t)
	ctx := context.Background()

	assertStableAndSorted(t, "GetDeviceMIBs", func() []string {
		mibs, err := f.store.GetDeviceMIBs(ctx, f.deviceID)
		if err != nil {
			t.Fatalf("GetDeviceMIBs: %v", err)
		}
		return namesOfMIBs(mibs)
	})

	assertStableAndSorted(t, "GetDeviceMIBsSeparated direct", func() []string {
		direct, _, err := f.store.GetDeviceMIBsSeparated(ctx, f.deviceID)
		if err != nil {
			t.Fatalf("GetDeviceMIBsSeparated: %v", err)
		}
		return namesOfMIBs(direct)
	})

	assertStableAndSorted(t, "GetDeviceMIBsSeparated all", func() []string {
		_, all, err := f.store.GetDeviceMIBsSeparated(ctx, f.deviceID)
		if err != nil {
			t.Fatalf("GetDeviceMIBsSeparated: %v", err)
		}
		return namesOfMIBs(all)
	})
}

// TestDeviceMIBsCoversBothSources guards the fixture as much as the code: the
// combined list has to contain the MIBs the profile contributes as well as the
// device's own, or the ordering assertions above would be testing half of it.
func TestDeviceMIBsCoversBothSources(t *testing.T) {
	f := newOrderFixture(t)

	direct, all, err := f.store.GetDeviceMIBsSeparated(context.Background(), f.deviceID)
	if err != nil {
		t.Fatalf("GetDeviceMIBsSeparated: %v", err)
	}

	if len(direct) == len(all) {
		t.Fatalf("the device's own %d MIBs are its whole list; the profile contributed nothing", len(direct))
	}
	want := mibNames()
	slices.Sort(want)
	if got := namesOfMIBs(all); !slices.Equal(got, want) {
		t.Errorf("the combined list is %v, want %v", got, want)
	}
}

func TestProfileAndCatalogListsAreStableAndSorted(t *testing.T) {
	f := newOrderFixture(t)
	ctx := context.Background()

	assertStableAndSorted(t, "GetProfileMIBs", func() []string {
		mibs, err := f.store.GetProfileMIBs(ctx, f.profileID)
		if err != nil {
			t.Fatalf("GetProfileMIBs: %v", err)
		}
		return namesOfMIBs(mibs)
	})

	assertStableAndSorted(t, "ListMIBs", func() []string {
		mibs, err := f.store.ListMIBs(ctx)
		if err != nil {
			t.Fatalf("ListMIBs: %v", err)
		}
		return namesOfMIBs(mibs)
	})
}

func TestDeviceAndProfileListsAreStableAndSorted(t *testing.T) {
	f := newOrderFixture(t)
	ctx := context.Background()

	assertStableAndSorted(t, "ListDevices", func() []string {
		devices, err := f.store.ListDevices(ctx)
		if err != nil {
			t.Fatalf("ListDevices: %v", err)
		}
		names := make([]string, len(devices))
		for i, d := range devices {
			names[i] = d.Name
		}
		return names
	})

	assertStableAndSorted(t, "ListDeviceProfiles", func() []string {
		profiles, err := f.store.ListDeviceProfiles(ctx)
		if err != nil {
			t.Fatalf("ListDeviceProfiles: %v", err)
		}
		names := make([]string, len(profiles))
		for i, p := range profiles {
			names[i] = p.Name
		}
		return names
	})
}

// TestLinkedListsAreStableAndSorted covers the two lists that answer "what is
// on the other end of this association", which the UI shows on the device and
// profile pages.
func TestLinkedListsAreStableAndSorted(t *testing.T) {
	f := newOrderFixture(t)
	ctx := context.Background()

	// Give the device a second profile and the profile a second device so both
	// lists have something to order.
	extra := &DeviceProfile{Name: "Aggregation"}
	if err := f.store.CreateDeviceProfile(ctx, extra); err != nil {
		t.Fatalf("create profile: %v", err)
	}
	if err := f.store.AssignProfileToDevice(ctx, f.deviceID, extra.ID); err != nil {
		t.Fatalf("assign profile: %v", err)
	}

	other, err := f.store.GetDeviceByName(ctx, "alpha-switch")
	if err != nil {
		t.Fatalf("get device: %v", err)
	}
	if err := f.store.AssignProfileToDevice(ctx, other.ID, f.profileID); err != nil {
		t.Fatalf("assign profile: %v", err)
	}

	assertStableAndSorted(t, "GetDeviceProfiles", func() []string {
		profiles, err := f.store.GetDeviceProfiles(ctx, f.deviceID)
		if err != nil {
			t.Fatalf("GetDeviceProfiles: %v", err)
		}
		names := make([]string, len(profiles))
		for i, p := range profiles {
			names[i] = p.Name
		}
		return names
	})

	assertStableAndSorted(t, "GetDevicesByProfile", func() []string {
		devices, err := f.store.GetDevicesByProfile(ctx, f.profileID)
		if err != nil {
			t.Fatalf("GetDevicesByProfile: %v", err)
		}
		names := make([]string, len(devices))
		for i, d := range devices {
			names[i] = d.Name
		}
		return names
	})
}

// TestListsOfAnEmptyStoreAreEmpty is the bad-input case: asking for the MIBs of
// a device that does not exist must answer with nothing rather than fail or
// return another device's records.
func TestListsOfAnEmptyStoreAreEmpty(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	mibs, err := store.GetDeviceMIBs(ctx, 4242)
	if err != nil {
		t.Fatalf("GetDeviceMIBs for an unknown device: %v", err)
	}
	if len(mibs) != 0 {
		t.Errorf("an unknown device has %d MIBs", len(mibs))
	}

	direct, all, err := store.GetDeviceMIBsSeparated(ctx, 4242)
	if err != nil {
		t.Fatalf("GetDeviceMIBsSeparated for an unknown device: %v", err)
	}
	if len(direct) != 0 || len(all) != 0 {
		t.Errorf("an unknown device has %d direct and %d total MIBs", len(direct), len(all))
	}
}

// TestSortByNameOrdersRecords covers the helper directly, including the
// degenerate inputs its callers can hand it.
func TestSortByNameOrdersRecords(t *testing.T) {
	mibs := []MIB{{Name: "UDP-MIB"}, {Name: "IF-MIB"}, {Name: "TCP-MIB"}}
	sortByName(mibs, mibName)

	if got := strings.Join(namesOfMIBs(mibs), ","); got != "IF-MIB,TCP-MIB,UDP-MIB" {
		t.Errorf("sorted to %s", got)
	}

	// Nothing to sort, and one thing to sort: both must be no-ops rather than
	// panics, because a store with no records reaches this on every list call.
	sortByName([]MIB(nil), mibName)
	single := []Device{{Name: "only"}}
	sortByName(single, deviceName)
	if single[0].Name != "only" {
		t.Errorf("sorting one record changed it to %q", single[0].Name)
	}

	profiles := []DeviceProfile{{Name: "b"}, {Name: "a"}}
	sortByName(profiles, profileName)
	if profiles[0].Name != "a" {
		t.Errorf("profiles sorted to %v", profiles)
	}
}

// TestSortByNameIsTotalOnDuplicates states what happens if the name index is
// ever bypassed and two records share a name: the order must still be settled,
// not left to the sort's internals.
func TestSortByNameIsTotalOnDuplicates(t *testing.T) {
	mibs := make([]MIB, 0, 6)
	for i := range 3 {
		mibs = append(mibs, MIB{ID: int64(i), Name: fmt.Sprintf("MIB-%d", i%2)})
	}
	sortByName(mibs, mibName)

	if !slices.IsSorted(namesOfMIBs(mibs)) {
		t.Errorf("duplicate names sorted to %v", namesOfMIBs(mibs))
	}
}
