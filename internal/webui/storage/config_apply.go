package storage

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/cockroachdb/pebble"
)

// configKeyPrefixes are every key space a configuration import owns.
//
// The MIB catalog (mib: records and their idx:mib: index) is deliberately not
// here. A module is a library the configuration draws on, not a piece of it: an
// imported module that nothing references costs nothing and polls nothing, while
// deleting one would throw away parsed metadata and the operator's tags on the
// strength of a document that never claimed to describe the catalog.
var configKeyPrefixes = []string{
	PrefixDevice,
	PrefixProfile,
	PrefixDevMIB,
	PrefixProfMIB,
	PrefixDevProf,
	PrefixNameIndex + "device:",
	PrefixNameIndex + "profile:",
}

// PlanImportConfig reports what importing doc would change, without changing
// anything.
//
// It is the read-only half of ImportConfig and runs the same validation, so a
// document that survives a dry run is a document the apply will accept for the
// same reasons.
//
// pending names modules the caller is about to import — the source files an
// export bundle carries — so that a dry run against a fresh instance describes
// the import that will actually happen rather than failing on modules that are
// a few seconds away from existing. Nothing else treats them as present: the
// apply re-checks the catalog for real, after those modules have been imported.
func (s *Storage) PlanImportConfig(ctx context.Context, doc *Config, pending []string) (*ConfigPlan, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	prepared, err := s.prepareImport(doc, pending)
	if err != nil {
		return nil, err
	}
	return prepared.plan, nil
}

// ImportConfig replaces this instance's configuration with doc.
//
// Replace, not merge: a device, profile or assignment the document does not
// contain is deleted. That is the semantic the operator chose, and it is why
// every caller of this method has to have obtained an explicit confirmation
// first.
//
// The whole change lands in one store batch, so it is all-or-nothing: a failure
// at any point leaves the previous configuration exactly as it was, and no
// reader can observe a half-applied fleet. Validation runs to completion before
// the batch is opened, so a document that is going to be rejected is rejected
// having read nothing but its own fields.
func (s *Storage) ImportConfig(ctx context.Context, doc *Config) (*ConfigPlan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	prepared, err := s.prepareImport(doc, nil)
	if err != nil {
		return nil, err
	}

	// Rewriting an unchanged configuration would churn the store and move every
	// record's write time for no gain. Importing an instance's own export is
	// meant to be a no-op, so it is one.
	if !prepared.plan.Changed() {
		return prepared.plan, nil
	}
	if err := s.writeConfig(prepared); err != nil {
		return nil, err
	}
	return prepared.plan, nil
}

// preparedImport is a validated document together with the state it was judged
// against and the plan that follows from both.
type preparedImport struct {
	state   *configState
	desired *Config
	plan    *ConfigPlan
}

// prepareImport validates a document and works out what applying it would do.
// The caller holds s.mu.
//
// The order is the point. The document's own fields are checked first, because
// that needs no store access and catches the mistakes an operator makes editing
// a file. The modules it references are checked next, against the catalog this
// instance actually holds. Only then is a plan computed. Nothing is written by
// any of it.
func (s *Storage) prepareImport(doc *Config, pending []string) (*preparedImport, error) {
	if err := ValidateConfig(doc); err != nil {
		return nil, err
	}

	state, err := s.readConfigState()
	if err != nil {
		return nil, err
	}

	desired := doc.normalized()
	if err := missingModules(desired, state.mibIDByName, pending); err != nil {
		return nil, err
	}

	current := &Config{
		SchemaVersion: ConfigSchemaVersion,
		Profiles:      state.configProfiles(),
		Devices:       state.configDevices(),
	}
	ids := configIDs{devices: deviceIDsByName(state), profiles: state.profileNameByID}

	return &preparedImport{
		state:   state,
		desired: desired,
		plan:    planConfig(current, desired, ids),
	}, nil
}

// missingModules reports the modules the configuration assigns that this
// instance neither holds nor is about to receive.
func missingModules(doc *Config, available map[string]int64, pending []string) error {
	var missing []string
	for _, module := range doc.ReferencedModules() {
		if _, imported := available[module]; imported {
			continue
		}
		if slices.Contains(pending, module) {
			continue
		}
		missing = append(missing, module)
	}
	if len(missing) == 0 {
		return nil
	}
	return &ConfigMIBMissingError{Modules: missing}
}

// deviceIDsByName indexes the stored devices by name.
func deviceIDsByName(state *configState) map[string]int64 {
	ids := make(map[string]int64, len(state.devices))
	for _, d := range state.devices {
		ids[d.Name] = d.ID
	}
	return ids
}

// writeConfig lands the whole configuration in one batch. The caller holds
// s.mu for writing.
func (s *Storage) writeConfig(prepared *preparedImport) error {
	batch := s.db.NewBatch()
	// Close only releases the batch's memory, committed or not.
	defer func() { _ = batch.Close() }()

	if err := s.clearConfigKeys(batch); err != nil {
		return err
	}

	writer, err := s.newConfigWriter(prepared, batch)
	if err != nil {
		return err
	}
	if err := writer.writeProfiles(); err != nil {
		return err
	}
	if err := writer.writeDevices(); err != nil {
		return err
	}
	if err := writer.writeCounters(); err != nil {
		return err
	}

	if err := batch.Commit(pebble.Sync); err != nil {
		return &ConfigStoreError{Op: "commit configuration", Err: err}
	}
	return nil
}

// clearConfigKeys stages the removal of every record the import replaces.
//
// The reads happen against the store rather than the batch, which has not been
// committed, so this sees exactly the state the plan was computed from.
func (s *Storage) clearConfigKeys(batch *pebble.Batch) error {
	for _, prefix := range configKeyPrefixes {
		err := s.scanPrefix(prefix, func(key string, _ []byte) error {
			if delErr := batch.Delete([]byte(key), nil); delErr != nil {
				return &ConfigStoreError{Op: "stage delete of " + key, Err: delErr}
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// configWriter stages the desired configuration into a batch, keeping the IDs
// and creation times of the records that survive.
//
// Identity is preserved on purpose: a device that is in both the store and the
// document is the same device, and giving it a new ID on every import would
// break every reference held elsewhere and make an unchanged import look like a
// fleet-wide replacement.
type configWriter struct {
	batch    *pebble.Batch
	desired  *Config
	mibIDs   map[string]int64
	devices  map[string]Device
	profiles map[string]DeviceProfile

	deviceIDs  map[string]int64
	profileIDs map[string]int64

	nextDeviceID  int64
	nextProfileID int64
	now           time.Time
}

// newConfigWriter reads the counters the import allocates from and indexes the
// records it may reuse.
func (s *Storage) newConfigWriter(prepared *preparedImport, batch *pebble.Batch) (*configWriter, error) {
	deviceCounter, err := s.counterValue(CounterDevice)
	if err != nil {
		return nil, err
	}
	profileCounter, err := s.counterValue(CounterProfile)
	if err != nil {
		return nil, err
	}

	state := prepared.state
	devices := make(map[string]Device, len(state.devices))
	for _, d := range state.devices {
		devices[d.Name] = d
		deviceCounter = max(deviceCounter, d.ID)
	}
	profiles := make(map[string]DeviceProfile, len(state.profiles))
	for _, p := range state.profiles {
		profiles[p.Name] = p
		profileCounter = max(profileCounter, p.ID)
	}

	return &configWriter{
		batch:         batch,
		desired:       prepared.desired,
		mibIDs:        state.mibIDByName,
		devices:       devices,
		profiles:      profiles,
		deviceIDs:     make(map[string]int64, len(prepared.desired.Devices)),
		profileIDs:    make(map[string]int64, len(prepared.desired.Profiles)),
		nextDeviceID:  deviceCounter,
		nextProfileID: profileCounter,
		now:           time.Now(),
	}, nil
}

// writeProfiles stages every profile and its MIB assignments.
func (w *configWriter) writeProfiles() error {
	for _, p := range w.desired.Profiles {
		record := DeviceProfile{Name: p.Name, Description: p.Description, CreatedAt: w.now}
		if existing, kept := w.profiles[p.Name]; kept {
			record.ID, record.CreatedAt = existing.ID, existing.CreatedAt
		} else {
			w.nextProfileID++
			record.ID = w.nextProfileID
		}
		w.profileIDs[p.Name] = record.ID

		if err := w.put(PrefixProfile+formatID(record.ID), record); err != nil {
			return err
		}
		if err := w.putIndex(PrefixNameIndex+"profile:"+record.Name, record.ID); err != nil {
			return err
		}
		if err := w.writeAssignments(PrefixProfMIB, record.ID, p.MIBs); err != nil {
			return err
		}
	}
	return nil
}

// writeDevices stages every device, its MIB assignments and its profile links.
func (w *configWriter) writeDevices() error {
	for _, d := range w.desired.Devices {
		record := Device{
			Name:         d.Name,
			Address:      d.Address,
			SNMPVersion:  d.SNMPVersion,
			Community:    d.Community,
			Port:         d.Port,
			CreatedAt:    w.now,
			PollInterval: copyInterval(d.PollInterval),
			Enabled:      copyFlag(d.Enabled),
		}
		if existing, kept := w.devices[d.Name]; kept {
			record.ID, record.CreatedAt = existing.ID, existing.CreatedAt
		} else {
			w.nextDeviceID++
			record.ID = w.nextDeviceID
		}
		w.deviceIDs[d.Name] = record.ID

		if err := w.put(PrefixDevice+formatID(record.ID), record); err != nil {
			return err
		}
		if err := w.putIndex(PrefixNameIndex+"device:"+record.Name, record.ID); err != nil {
			return err
		}
		if err := w.writeAssignments(PrefixDevMIB, record.ID, d.MIBs); err != nil {
			return err
		}
		if err := w.writeDeviceProfiles(record.ID, d.Profiles); err != nil {
			return err
		}
	}
	return nil
}

// writeAssignments stages one owner's MIB assignments.
func (w *configWriter) writeAssignments(prefix string, ownerID int64, assignments []ConfigAssignment) error {
	for _, a := range assignments {
		// prepareImport has already refused a document naming a module this
		// instance does not hold, so every lookup here resolves.
		mibID := w.mibIDs[a.Module]

		encoded, err := a.encode()
		if err != nil {
			return &ConfigStoreError{Op: "encode assignment for " + a.Module, Err: err}
		}
		key := prefix + formatID(ownerID) + ":" + formatID(mibID)
		if err := w.batch.Set([]byte(key), encoded, nil); err != nil {
			return &ConfigStoreError{Op: "stage assignment " + key, Err: err}
		}
	}
	return nil
}

// writeDeviceProfiles stages a device's profile links.
func (w *configWriter) writeDeviceProfiles(deviceID int64, names []string) error {
	for _, name := range names {
		// Validation has already refused a device naming a profile the document
		// does not define, so every lookup here resolves.
		key := PrefixDevProf + formatID(deviceID) + ":" + formatID(w.profileIDs[name])
		if err := w.batch.Set([]byte(key), []byte{1}, nil); err != nil {
			return &ConfigStoreError{Op: "stage profile link " + key, Err: err}
		}
	}
	return nil
}

// writeCounters advances the auto-increment counters past every ID in use, so a
// record created after the import can never be handed the ID of one it deleted.
func (w *configWriter) writeCounters() error {
	if err := w.putCounter(CounterDevice, w.nextDeviceID); err != nil {
		return err
	}
	return w.putCounter(CounterProfile, w.nextProfileID)
}

// put stages a JSON record.
func (w *configWriter) put(key string, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return &ConfigStoreError{Op: "encode record " + key, Err: err}
	}
	if err := w.batch.Set([]byte(key), encoded, nil); err != nil {
		return &ConfigStoreError{Op: "stage record " + key, Err: err}
	}
	return nil
}

// putIndex stages a name index entry.
func (w *configWriter) putIndex(key string, id int64) error {
	if err := w.batch.Set([]byte(key), []byte(formatID(id)), nil); err != nil {
		return &ConfigStoreError{Op: "stage index " + key, Err: err}
	}
	return nil
}

// putCounter stages an auto-increment counter.
func (w *configWriter) putCounter(key string, value int64) error {
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, uint64(value)) // #nosec G115 -- counters only ever increment from zero
	if err := w.batch.Set([]byte(key), buf, nil); err != nil {
		return &ConfigStoreError{Op: "stage counter " + key, Err: err}
	}
	return nil
}

// counterValue reads an auto-increment counter without advancing it. The caller
// holds s.mu.
func (s *Storage) counterValue(key string) (int64, error) {
	data, closer, err := s.db.Get([]byte(key))
	if err == pebble.ErrNotFound {
		return 0, nil
	}
	if err != nil {
		return 0, &ConfigStoreError{Op: "read counter " + key, Err: err}
	}
	// closer only unpins the value, which has already been copied out.
	defer func() { _ = closer.Close() }()

	if len(data) != 8 {
		return 0, nil
	}
	// The counter was written as a uint64 of an int64 that only increments from
	// zero, so it cannot be above int64 range.
	return int64(binary.BigEndian.Uint64(data)), nil // #nosec G115 -- round-trip of a value written as an int64
}

// normalized returns the document in the one shape the store and the exporter
// both produce: ordered by name throughout, with a device's default port filled
// in and every empty pattern list collapsed.
//
// Comparing normalized documents is what makes a re-import of an instance's own
// export a no-op instead of a diff full of reorderings.
func (c *Config) normalized() *Config {
	normalized := &Config{
		SchemaVersion: c.SchemaVersion,
		ExportedAt:    c.ExportedAt,
		MIBs:          slices.Clone(c.MIBs),
		Profiles:      make([]ConfigProfile, 0, len(c.Profiles)),
		Devices:       make([]ConfigDevice, 0, len(c.Devices)),
	}

	for _, p := range c.Profiles {
		p.MIBs = normalizeAssignments(p.MIBs)
		normalized.Profiles = append(normalized.Profiles, p)
	}
	for _, d := range c.Devices {
		if d.Port == 0 {
			d.Port = DefaultDevicePort
		}
		d.Profiles = sortedCopy(d.Profiles)
		d.MIBs = normalizeAssignments(d.MIBs)
		d.PollInterval = copyInterval(d.PollInterval)
		d.Enabled = copyFlag(d.Enabled)
		normalized.Devices = append(normalized.Devices, d)
	}

	slices.SortFunc(normalized.MIBs, func(a, b ConfigMIB) int { return strings.Compare(a.Name, b.Name) })
	slices.SortFunc(normalized.Profiles, func(a, b ConfigProfile) int { return strings.Compare(a.Name, b.Name) })
	slices.SortFunc(normalized.Devices, func(a, b ConfigDevice) int { return strings.Compare(a.Name, b.Name) })
	return normalized
}

// normalizeAssignments orders assignments by module and collapses their empty
// pattern lists.
func normalizeAssignments(assignments []ConfigAssignment) []ConfigAssignment {
	if len(assignments) == 0 {
		return nil
	}

	normalized := make([]ConfigAssignment, 0, len(assignments))
	for _, a := range assignments {
		normalized = append(normalized, ConfigAssignment{
			Module: a.Module, MIBAssignment: a.normalize(),
		})
	}
	slices.SortFunc(normalized, func(a, b ConfigAssignment) int {
		return strings.Compare(a.Module, b.Module)
	})
	return normalized
}

// sortedCopy returns a sorted copy of a name list, or nil when it is empty.
func sortedCopy(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	sorted := slices.Clone(values)
	slices.Sort(sorted)
	return sorted
}
