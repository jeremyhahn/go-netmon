package storage

import (
	"slices"
	"strconv"
	"strings"
)

// The actions a planned change can carry.
const (
	ConfigActionCreate = "create"
	ConfigActionUpdate = "update"
	ConfigActionDelete = "delete"
)

// ConfigPlan is what importing a document would do to this instance.
//
// It is computed from the configuration actually stored, never from what a
// previous run reported, so a plan shown to an operator describes the machine in
// front of them.
type ConfigPlan struct {
	Profiles []ConfigChange `json:"profiles"`
	Devices  []ConfigChange `json:"devices"`
	Counts   ConfigCounts   `json:"counts"`
}

// ConfigChange is one record an import would create, update or delete.
//
// An update carries the fields that differ. A create or a delete carries Detail
// instead: listing every field of a record that is wholly arriving or wholly
// going does not tell an operator anything the record's own summary does not.
type ConfigChange struct {
	Action string `json:"action"`
	Name   string `json:"name"`

	// ID is the stored record's ID, present on an update or a delete.
	ID int64 `json:"id,omitempty"`

	Detail string              `json:"detail,omitempty"`
	Fields []ConfigFieldChange `json:"fields,omitempty"`
}

// ConfigFieldChange is one field of one record moving from one value to another.
type ConfigFieldChange struct {
	Field string `json:"field"`
	From  string `json:"from"`
	To    string `json:"to"`
}

// ConfigCounts summarizes a plan in the terms an operator confirms it in.
type ConfigCounts struct {
	DevicesCreated  int `json:"devices_created"`
	DevicesUpdated  int `json:"devices_updated"`
	DevicesDeleted  int `json:"devices_deleted"`
	ProfilesCreated int `json:"profiles_created"`
	ProfilesUpdated int `json:"profiles_updated"`
	ProfilesDeleted int `json:"profiles_deleted"`
}

// Changed reports whether the plan would alter anything. Importing a document
// that was just exported from the same instance leaves it false.
func (p *ConfigPlan) Changed() bool {
	return len(p.Devices) > 0 || len(p.Profiles) > 0
}

// planConfig compares the configuration that is stored with the one that is
// wanted. Both documents are already normalized, so every difference it reports
// is a difference of configuration rather than of spelling.
func planConfig(current, desired *Config, ids configIDs) *ConfigPlan {
	plan := &ConfigPlan{
		Profiles: profileDiff(ids).run(current.Profiles, desired.Profiles),
		Devices:  deviceDiff(ids).run(current.Devices, desired.Devices),
	}
	plan.Counts = countChanges(plan)
	return plan
}

// configIDs resolves a stored record's name to its ID, so a plan can name the
// record an operator sees in the UI.
type configIDs struct {
	devices  map[string]int64
	profiles map[string]int64
}

// countChanges tallies a plan by record kind and action.
func countChanges(plan *ConfigPlan) ConfigCounts {
	var counts ConfigCounts
	tally := func(changes []ConfigChange, created, updated, deleted *int) {
		for _, c := range changes {
			switch c.Action {
			case ConfigActionCreate:
				*created++
			case ConfigActionUpdate:
				*updated++
			case ConfigActionDelete:
				*deleted++
			}
		}
	}
	tally(plan.Devices, &counts.DevicesCreated, &counts.DevicesUpdated, &counts.DevicesDeleted)
	tally(plan.Profiles, &counts.ProfilesCreated, &counts.ProfilesUpdated, &counts.ProfilesDeleted)
	return counts
}

// recordDiff compares two name-keyed collections of the same record kind.
type recordDiff[T any] struct {
	name   func(T) string
	detail func(T) string
	fields func(before, after T) []ConfigFieldChange
	id     map[string]int64
}

// run produces the changes that turn current into desired. Records present in
// current and absent from desired are deleted: the document is the whole
// configuration, not an addition to it.
func (d recordDiff[T]) run(current, desired []T) []ConfigChange {
	before := make(map[string]T, len(current))
	for _, record := range current {
		before[d.name(record)] = record
	}

	var changes []ConfigChange
	kept := make(map[string]struct{}, len(desired))
	for _, record := range desired {
		name := d.name(record)
		kept[name] = struct{}{}

		existing, found := before[name]
		if !found {
			changes = append(changes, ConfigChange{
				Action: ConfigActionCreate, Name: name, Detail: d.detail(record),
			})
			continue
		}
		if fields := d.fields(existing, record); len(fields) > 0 {
			changes = append(changes, ConfigChange{
				Action: ConfigActionUpdate, Name: name, ID: d.id[name], Fields: fields,
			})
		}
	}

	for _, record := range current {
		name := d.name(record)
		if _, stays := kept[name]; stays {
			continue
		}
		changes = append(changes, ConfigChange{
			Action: ConfigActionDelete, Name: name, ID: d.id[name], Detail: d.detail(record),
		})
	}

	slices.SortFunc(changes, compareChanges)
	return changes
}

// compareChanges orders a plan by action and then by name, so the destructive
// entries are read together rather than scattered through the additions.
func compareChanges(a, b ConfigChange) int {
	if order := actionOrder(a.Action) - actionOrder(b.Action); order != 0 {
		return order
	}
	return strings.Compare(a.Name, b.Name)
}

// actionOrder ranks the actions for display: what is being removed first,
// because that is the part an operator has to agree to.
func actionOrder(action string) int {
	switch action {
	case ConfigActionDelete:
		return 0
	case ConfigActionUpdate:
		return 1
	default:
		return 2
	}
}

// deviceDiff compares device records.
func deviceDiff(ids configIDs) recordDiff[ConfigDevice] {
	return recordDiff[ConfigDevice]{
		name:   func(d ConfigDevice) string { return d.Name },
		detail: deviceDetail,
		fields: deviceFieldChanges,
		id:     ids.devices,
	}
}

// profileDiff compares profile records.
func profileDiff(ids configIDs) recordDiff[ConfigProfile] {
	return recordDiff[ConfigProfile]{
		name:   func(p ConfigProfile) string { return p.Name },
		detail: profileDetail,
		fields: profileFieldChanges,
		id:     ids.profiles,
	}
}

// deviceDetail summarizes a device in one line.
func deviceDetail(d ConfigDevice) string {
	parts := []string{
		d.Address + ":" + strconv.Itoa(d.Port),
		d.SNMPVersion,
		"poll " + pollIntervalText(d.PollInterval),
	}
	if len(d.Profiles) > 0 {
		parts = append(parts, "profiles "+strings.Join(d.Profiles, ","))
	}
	parts = append(parts, strconv.Itoa(len(d.MIBs))+" MIB(s)")
	return strings.Join(parts, ", ")
}

// profileDetail summarizes a profile in one line.
func profileDetail(p ConfigProfile) string {
	detail := strconv.Itoa(len(p.MIBs)) + " MIB(s)"
	if p.Description != "" {
		detail += ", " + p.Description
	}
	return detail
}

// deviceFieldChanges reports every field of a device that differs.
//
// The community string is reported like any other field. Redacting it here
// would only make the diff lie about what is changing: the document being
// imported carries the credential in clear, and so does the configuration the
// poller already fetches, so a diff that hid it would protect nothing while
// hiding a change of credential from the person approving it.
func deviceFieldChanges(before, after ConfigDevice) []ConfigFieldChange {
	changes := scalarChanges([]scalarField{
		{"address", before.Address, after.Address},
		{"port", strconv.Itoa(before.Port), strconv.Itoa(after.Port)},
		{"snmp_version", before.SNMPVersion, after.SNMPVersion},
		{"community", before.Community, after.Community},
		{"poll_interval", pollIntervalText(before.PollInterval), pollIntervalText(after.PollInterval)},
		{"enabled", enabledText(before.Enabled), enabledText(after.Enabled)},
		{"profiles", listText(before.Profiles), listText(after.Profiles)},
	})
	return append(changes, assignmentChanges(before.MIBs, after.MIBs)...)
}

// profileFieldChanges reports every field of a profile that differs.
func profileFieldChanges(before, after ConfigProfile) []ConfigFieldChange {
	changes := scalarChanges([]scalarField{
		{"description", before.Description, after.Description},
	})
	return append(changes, assignmentChanges(before.MIBs, after.MIBs)...)
}

// scalarField is one field's rendered value on each side of a comparison.
type scalarField struct {
	name string
	from string
	to   string
}

// scalarChanges keeps the fields whose rendered values differ.
func scalarChanges(fields []scalarField) []ConfigFieldChange {
	changes := make([]ConfigFieldChange, 0, len(fields))
	for _, f := range fields {
		if f.from != f.to {
			changes = append(changes, ConfigFieldChange{Field: f.name, From: f.from, To: f.to})
		}
	}
	return changes
}

// assignmentChanges reports the MIB assignments that arrive, leave or change.
//
// A rule that changes is reported field by field, because "IF-MIB changed" does
// not say whether a device started polling more or stopped polling most of it.
func assignmentChanges(before, after []ConfigAssignment) []ConfigFieldChange {
	beforeByModule := assignmentsByModule(before)
	afterByModule := assignmentsByModule(after)

	var changes []ConfigFieldChange
	for _, module := range mergedModules(beforeByModule, afterByModule) {
		old, hadBefore := beforeByModule[module]
		next, hasAfter := afterByModule[module]

		switch {
		case !hadBefore:
			changes = append(changes, ConfigFieldChange{
				Field: "mibs[" + module + "]", From: "", To: assignmentText(next),
			})
		case !hasAfter:
			changes = append(changes, ConfigFieldChange{
				Field: "mibs[" + module + "]", From: assignmentText(old), To: "",
			})
		default:
			changes = append(changes, ruleChanges(module, old, next)...)
		}
	}
	return changes
}

// ruleChanges reports the parts of one assignment's rule that differ.
func ruleChanges(module string, before, after MIBAssignment) []ConfigFieldChange {
	prefix := "mibs[" + module + "]."
	return scalarChanges([]scalarField{
		{prefix + "enabled", strconv.FormatBool(before.Enabled), strconv.FormatBool(after.Enabled)},
		{prefix + "include_oids", listText(before.IncludeOIDs), listText(after.IncludeOIDs)},
		{prefix + "exclude_oids", listText(before.ExcludeOIDs), listText(after.ExcludeOIDs)},
		{prefix + "include_names", listText(before.IncludeNames), listText(after.IncludeNames)},
		{prefix + "exclude_names", listText(before.ExcludeNames), listText(after.ExcludeNames)},
		{prefix + "max_oids", strconv.Itoa(before.MaxOIDs), strconv.Itoa(after.MaxOIDs)},
	})
}

// assignmentsByModule indexes assignments by the module they configure.
func assignmentsByModule(assignments []ConfigAssignment) map[string]MIBAssignment {
	byModule := make(map[string]MIBAssignment, len(assignments))
	for _, a := range assignments {
		byModule[a.Module] = a.MIBAssignment
	}
	return byModule
}

// mergedModules returns every module named on either side, in name order.
func mergedModules(before, after map[string]MIBAssignment) []string {
	modules := make([]string, 0, len(before)+len(after))
	for module := range before {
		modules = append(modules, module)
	}
	for module := range after {
		if _, both := before[module]; !both {
			modules = append(modules, module)
		}
	}
	slices.Sort(modules)
	return modules
}

// assignmentText renders an assignment as the one word that says whether the
// module is polled, with a note when a rule narrows it.
func assignmentText(a MIBAssignment) string {
	state := "disabled"
	if a.Enabled {
		state = "enabled"
	}
	if a.HasFilters() {
		state += ", filtered"
	}
	return state
}

// listText renders a pattern or name list for a diff cell.
func listText(values []string) string {
	if len(values) == 0 {
		return "[]"
	}
	return "[" + strings.Join(values, " ") + "]"
}

// pollIntervalText renders a device's schedule, naming inheritance rather than
// printing a zero that reads as "never".
func pollIntervalText(interval *int) string {
	if interval == nil {
		return "inherit"
	}
	return strconv.Itoa(*interval) + "s"
}

// enabledText reads an absent flag as what it means, so a device exported
// before the field existed does not show as changed against one that says
// enabled.
func enabledText(flag *bool) string {
	return strconv.FormatBool(EnabledOf(&Device{Enabled: flag}))
}
