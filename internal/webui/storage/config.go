package storage

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/cockroachdb/pebble"
)

// ConfigSchemaVersion is the version of the configuration document this build
// reads and writes.
//
// It exists so that a document written by a future build is refused outright
// rather than half-understood: a field this build does not know about is a
// configuration the operator asked for and would not get, and silently polling
// less than was asked for is the failure mode this whole feature is meant to
// prevent.
const ConfigSchemaVersion = 1

// Config is the portable form of everything an operator configures: the devices
// polled, the profiles that bundle MIBs, and every per-assignment rule that says
// how each MIB is polled.
//
// It is keyed by name throughout. Record IDs are local to one instance — the
// same module is id 12 here and id 140 there — so a document that carried them
// could not be moved between instances, which is the point of having one.
//
// Everything in it is configuration. Metric history is not here, and neither is
// the parsed MIB catalog: MIBs is a list of the modules the configuration
// references, and the source files that go with it travel beside the document
// rather than inside it.
type Config struct {
	SchemaVersion int       `json:"schema_version"`
	ExportedAt    time.Time `json:"exported_at"`

	// MIBs names the modules this configuration needs. It is a prerequisite
	// list, not a configuration to be replaced: an import adds or refreshes
	// these modules and never deletes one the document omits.
	MIBs []ConfigMIB `json:"mibs"`

	Profiles []ConfigProfile `json:"profiles"`
	Devices  []ConfigDevice  `json:"devices"`
}

// ConfigMIB is one MIB module the configuration depends on.
//
// File and SHA256 are set when the document travels inside a bundle that
// carries the module's source; they are absent from a bare document, which then
// requires the module to be imported on the target already.
type ConfigMIB struct {
	Name   string   `json:"name"`
	File   string   `json:"file,omitempty"`
	SHA256 string   `json:"sha256,omitempty"`
	Tags   []string `json:"tags,omitempty"`
}

// ConfigProfile is a named bundle of MIB assignments shared by many devices.
type ConfigProfile struct {
	Name        string             `json:"name"`
	Description string             `json:"description,omitempty"`
	MIBs        []ConfigAssignment `json:"mibs,omitempty"`
}

// ConfigDevice is one monitored device and everything attached to it.
type ConfigDevice struct {
	Name        string `json:"name"`
	Address     string `json:"address"`
	Port        int    `json:"port"`
	SNMPVersion string `json:"snmp_version"`
	Community   string `json:"community,omitempty"`

	// PollInterval is absent on a device that inherits the poller's global
	// default, which is a different configuration from one that pins the same
	// number the default happens to be today.
	PollInterval *int `json:"poll_interval,omitempty"`

	// Enabled is absent on a device that is polled, as on Device: only a
	// device an operator switched off says so.
	Enabled *bool `json:"enabled,omitempty"`

	// Profiles names the profiles this device inherits, and MIBs are the
	// modules assigned to the device itself.
	Profiles []string           `json:"profiles,omitempty"`
	MIBs     []ConfigAssignment `json:"mibs,omitempty"`
}

// ConfigAssignment is one MIB assignment: the module, and the rule that says how
// it is polled.
//
// The assignment's own fields are flattened into the object so the document
// reads as one record per module rather than a module wrapped around a nested
// rule.
type ConfigAssignment struct {
	Module string `json:"module"`
	MIBAssignment
}

// ConfigExportOptions narrows what an export carries.
type ConfigExportOptions struct {
	// ReferencedMIBsOnly limits MIBs to the modules some device or profile
	// actually assigns, instead of the whole imported catalog.
	ReferencedMIBsOnly bool
}

// ExportConfig returns the instance's whole logical configuration.
//
// The document is fully ordered — devices, profiles, modules and assignments
// all by name — so two exports of the same configuration are byte-identical and
// a diff between two instances shows only real differences.
func (s *Storage) ExportConfig(ctx context.Context, opts ConfigExportOptions) (*Config, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	state, err := s.readConfigState()
	if err != nil {
		return nil, err
	}

	doc := &Config{
		SchemaVersion: ConfigSchemaVersion,
		ExportedAt:    time.Now().UTC(),
		MIBs:          state.configMIBs(),
		Profiles:      state.configProfiles(),
		Devices:       state.configDevices(),
	}

	if opts.ReferencedMIBsOnly {
		doc.MIBs = retainModules(doc.MIBs, doc.ReferencedModules())
	}
	return doc, nil
}

// ReferencedModules returns the module names some device or profile assigns, in
// name order. It is what a caller needs to decide which source files a bundle
// has to carry.
func (c *Config) ReferencedModules() []string {
	seen := make(map[string]struct{})
	for _, p := range c.Profiles {
		collectModules(seen, p.MIBs)
	}
	for _, d := range c.Devices {
		collectModules(seen, d.MIBs)
	}

	modules := make([]string, 0, len(seen))
	for name := range seen {
		modules = append(modules, name)
	}
	slices.Sort(modules)
	return modules
}

// collectModules adds each assignment's module to seen.
func collectModules(seen map[string]struct{}, assignments []ConfigAssignment) {
	for _, a := range assignments {
		seen[a.Module] = struct{}{}
	}
}

// retainModules keeps only the entries named in keep, which is sorted.
func retainModules(mibs []ConfigMIB, keep []string) []ConfigMIB {
	kept := make([]ConfigMIB, 0, len(keep))
	for _, m := range mibs {
		if _, found := slices.BinarySearch(keep, m.Name); found {
			kept = append(kept, m)
		}
	}
	return kept
}

// configState is everything the configuration document is built from, read in
// one pass over the store while the lock is held once.
type configState struct {
	devices  []Device
	profiles []DeviceProfile
	mibs     []MIB

	// mibNameByID and mibIDByName translate between the document's module names
	// and the join rows' module IDs.
	mibNameByID map[int64]string
	mibIDByName map[string]int64

	profileNameByID map[string]int64

	deviceMIBs     map[int64]map[int64]MIBAssignment
	profileMIBs    map[int64]map[int64]MIBAssignment
	deviceProfiles map[int64][]int64
}

// readConfigState loads every record the configuration document covers. The
// caller holds s.mu.
func (s *Storage) readConfigState() (*configState, error) {
	state := &configState{
		mibNameByID:     make(map[int64]string),
		mibIDByName:     make(map[string]int64),
		profileNameByID: make(map[string]int64),
		deviceMIBs:      make(map[int64]map[int64]MIBAssignment),
		profileMIBs:     make(map[int64]map[int64]MIBAssignment),
		deviceProfiles:  make(map[int64][]int64),
	}

	if err := scanRecords(s, PrefixDevice, &state.devices); err != nil {
		return nil, err
	}
	if err := scanRecords(s, PrefixProfile, &state.profiles); err != nil {
		return nil, err
	}
	if err := scanRecords(s, PrefixMIB, &state.mibs); err != nil {
		return nil, err
	}

	for _, m := range state.mibs {
		state.mibNameByID[m.ID] = m.Name
		state.mibIDByName[m.Name] = m.ID
	}
	for _, p := range state.profiles {
		state.profileNameByID[p.Name] = p.ID
	}

	if err := s.readJoinAssignments(PrefixDevMIB, state.deviceMIBs); err != nil {
		return nil, err
	}
	if err := s.readJoinAssignments(PrefixProfMIB, state.profileMIBs); err != nil {
		return nil, err
	}
	if err := s.readDeviceProfiles(state.deviceProfiles); err != nil {
		return nil, err
	}

	sortByName(state.devices, deviceName)
	sortByName(state.profiles, profileName)
	sortByName(state.mibs, mibName)
	return state, nil
}

// readJoinAssignments loads every dev-mib: or prof-mib: row into owner→MIB→rule.
func (s *Storage) readJoinAssignments(prefix string, into map[int64]map[int64]MIBAssignment) error {
	return s.scanPrefix(prefix, func(key string, value []byte) error {
		ownerID, mibID, ok := parseJoinKey(key, prefix)
		if !ok {
			return nil
		}
		if into[ownerID] == nil {
			into[ownerID] = make(map[int64]MIBAssignment)
		}
		into[ownerID][mibID] = decodeMIBAssignment(value).normalize()
		return nil
	})
}

// readDeviceProfiles loads every dev-prof: row into device→profiles.
func (s *Storage) readDeviceProfiles(into map[int64][]int64) error {
	return s.scanPrefix(PrefixDevProf, func(key string, _ []byte) error {
		deviceID, profileID, ok := parseJoinKey(key, PrefixDevProf)
		if !ok {
			return nil
		}
		into[deviceID] = append(into[deviceID], profileID)
		return nil
	})
}

// configMIBs renders the imported catalog as the document's prerequisite list.
func (st *configState) configMIBs() []ConfigMIB {
	mibs := make([]ConfigMIB, 0, len(st.mibs))
	for _, m := range st.mibs {
		mibs = append(mibs, ConfigMIB{Name: m.Name, Tags: emptyToNil(m.Tags)})
	}
	return mibs
}

// configProfiles renders every profile with its assignments.
func (st *configState) configProfiles() []ConfigProfile {
	profiles := make([]ConfigProfile, 0, len(st.profiles))
	for _, p := range st.profiles {
		profiles = append(profiles, ConfigProfile{
			Name:        p.Name,
			Description: p.Description,
			MIBs:        st.assignments(st.profileMIBs[p.ID]),
		})
	}
	return profiles
}

// configDevices renders every device with its profiles and direct assignments.
func (st *configState) configDevices() []ConfigDevice {
	devices := make([]ConfigDevice, 0, len(st.devices))
	for _, d := range st.devices {
		devices = append(devices, ConfigDevice{
			Name:         d.Name,
			Address:      d.Address,
			Port:         d.Port,
			SNMPVersion:  d.SNMPVersion,
			Community:    d.Community,
			PollInterval: copyInterval(d.PollInterval),
			Enabled:      copyFlag(d.Enabled),
			Profiles:     st.profileNames(st.deviceProfiles[d.ID]),
			MIBs:         st.assignments(st.deviceMIBs[d.ID]),
		})
	}
	return devices
}

// assignments renders one owner's join rows as document assignments, ordered by
// module name. A row naming a module that is no longer in the catalog is left
// out: it is a dangling reference the store has, not a configuration anyone can
// act on, and carrying it would make the document fail its own validation.
func (st *configState) assignments(byMIB map[int64]MIBAssignment) []ConfigAssignment {
	if len(byMIB) == 0 {
		return nil
	}

	list := make([]ConfigAssignment, 0, len(byMIB))
	for mibID, a := range byMIB {
		name, known := st.mibNameByID[mibID]
		if !known {
			continue
		}
		list = append(list, ConfigAssignment{Module: name, MIBAssignment: a})
	}
	if len(list) == 0 {
		return nil
	}

	slices.SortFunc(list, func(a, b ConfigAssignment) int {
		return strings.Compare(a.Module, b.Module)
	})
	return list
}

// profileNames renders a device's profile IDs as sorted profile names, skipping
// any that no longer resolve.
func (st *configState) profileNames(ids []int64) []string {
	if len(ids) == 0 {
		return nil
	}

	byID := make(map[int64]string, len(st.profiles))
	for _, p := range st.profiles {
		byID[p.ID] = p.Name
	}

	names := make([]string, 0, len(ids))
	for _, id := range ids {
		if name, known := byID[id]; known {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	slices.Sort(names)
	return names
}

// scanPrefix visits every key under prefix in order. The caller holds s.mu.
func (s *Storage) scanPrefix(prefix string, visit func(key string, value []byte) error) error {
	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte(prefix),
		UpperBound: []byte(prefix + "\xff"),
	})
	if err != nil {
		return &ConfigStoreError{Op: "scan " + prefix, Err: err}
	}
	// Iteration has finished; a close error cannot change the rows already read.
	defer func() { _ = iter.Close() }()

	for iter.First(); iter.Valid(); iter.Next() {
		if err := visit(string(iter.Key()), iter.Value()); err != nil {
			return err
		}
	}
	if err := iter.Error(); err != nil {
		return &ConfigStoreError{Op: "scan " + prefix, Err: err}
	}
	return nil
}

// scanRecords decodes every JSON record stored under prefix into out.
func scanRecords[T any](s *Storage, prefix string, out *[]T) error {
	return s.scanPrefix(prefix, func(_ string, value []byte) error {
		var record T
		if err := json.Unmarshal(value, &record); err != nil {
			return &ConfigStoreError{Op: "decode " + prefix, Err: err}
		}
		*out = append(*out, record)
		return nil
	})
}

// parseJoinKey splits a two-ID join row key such as dev-mib:<owner>:<mib>.
func parseJoinKey(key, prefix string) (ownerID, otherID int64, ok bool) {
	rest, found := strings.CutPrefix(key, prefix)
	if !found {
		return 0, 0, false
	}
	left, right, split := strings.Cut(rest, ":")
	if !split {
		return 0, 0, false
	}

	ownerID, err := parseID(left)
	if err != nil {
		return 0, 0, false
	}
	otherID, err = parseID(right)
	if err != nil {
		return 0, 0, false
	}
	return ownerID, otherID, true
}

// normalize renders an assignment in the one form the document uses, so that a
// rule stored with an empty list and one stored without the field compare and
// serialize identically.
func (a MIBAssignment) normalize() MIBAssignment {
	return MIBAssignment{
		Enabled:      a.Enabled,
		IncludeOIDs:  emptyToNil(a.IncludeOIDs),
		ExcludeOIDs:  emptyToNil(a.ExcludeOIDs),
		IncludeNames: emptyToNil(a.IncludeNames),
		ExcludeNames: emptyToNil(a.ExcludeNames),
		MaxOIDs:      a.MaxOIDs,
	}
}

// emptyToNil collapses an empty slice to nil so that "no patterns" has one
// representation and never shows up as a difference.
func emptyToNil(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	return values
}

// copyInterval copies a device's optional poll interval so the document cannot
// alias a stored record.
func copyInterval(interval *int) *int {
	if interval == nil {
		return nil
	}
	value := *interval
	return &value
}

func copyFlag(flag *bool) *bool {
	if flag == nil {
		return nil
	}
	value := *flag
	return &value
}
