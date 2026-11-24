package storage

import (
	"regexp"
	"strconv"
	"strings"
)

// DefaultDevicePort is the SNMP port a device is given when none is recorded.
const DefaultDevicePort = 161

// MaxDevicePort is the highest port number a device may be polled on.
const MaxDevicePort = 65535

// supportedSNMPVersions are the version strings a device record may carry. Both
// spellings of each version are accepted because both are already in use: the
// web UI writes "v2c" and the poller's own configuration writes "2c".
var supportedSNMPVersions = []string{"1", "2c", "3", "v1", "v2c", "v3"}

// ConfigStoreError reports that the record store could not be read or written
// while a configuration was being exported or imported.
type ConfigStoreError struct {
	Op  string
	Err error
}

func (e *ConfigStoreError) Error() string {
	return "config: " + e.Op + ": " + e.Err.Error()
}

func (e *ConfigStoreError) Unwrap() error { return e.Err }

// ConfigSchemaError reports a configuration document this build cannot read.
//
// It is raised before any field is examined, so a document from a future build
// is refused whole rather than read for the parts that happen to still parse.
type ConfigSchemaError struct {
	Found     int
	Supported int
}

func (e *ConfigSchemaError) Error() string {
	return "config: document schema_version " + strconv.Itoa(e.Found) +
		" cannot be read by this build, which supports version " +
		strconv.Itoa(e.Supported) +
		"; export the configuration again from the instance that produced it, " +
		"or upgrade this instance"
}

// ConfigProblem is one reason a configuration document was rejected. It names
// the record and the field so a caller can point at the input that has to
// change.
type ConfigProblem struct {
	// Scope is "document", "device", "profile" or "mib".
	Scope   string `json:"scope"`
	Subject string `json:"subject,omitempty"`
	Field   string `json:"field,omitempty"`
	Value   string `json:"value,omitempty"`
	Reason  string `json:"reason"`
}

func (e *ConfigProblem) Error() string {
	subject := e.Scope
	if e.Subject != "" {
		subject += " " + strconv.Quote(e.Subject)
	}
	if e.Field == "" {
		return subject + ": " + e.Reason
	}
	return subject + ": " + e.Field + " " + strconv.Quote(e.Value) + ": " + e.Reason
}

// ConfigValidationError reports every problem found in one document.
//
// Every problem is collected rather than only the first, because an operator
// fixing an exported file by hand should see the whole list once instead of
// discovering it one failed import at a time.
type ConfigValidationError struct {
	Problems []ConfigProblem
}

func (e *ConfigValidationError) Error() string {
	parts := make([]string, 0, len(e.Problems))
	for i := range e.Problems {
		parts = append(parts, e.Problems[i].Error())
	}
	return "config: " + strconv.Itoa(len(e.Problems)) +
		" problem(s) in the configuration document: " + strings.Join(parts, "; ")
}

// ConfigMIBMissingError reports modules the document references that this
// instance cannot supply: neither carried by the bundle nor already imported.
//
// It is deliberately fatal to the whole import. Dropping the assignment instead
// would leave a device polling less than the configuration asked for, with
// nothing on the screen to say so.
type ConfigMIBMissingError struct {
	Modules []string
}

func (e *ConfigMIBMissingError) Error() string {
	return "config: " + strconv.Itoa(len(e.Modules)) +
		" MIB module(s) referenced by this configuration are not imported on this " +
		"instance and are not carried by the file: " + strings.Join(e.Modules, ", ") +
		"; import those modules first, or use an export bundle that carries them"
}

// AssignmentFieldError names the first thing in a MIB assignment the poller
// could not act on.
type AssignmentFieldError struct {
	Field  string
	Value  string
	Reason string
}

func (e *AssignmentFieldError) Error() string {
	return "assignment: invalid " + e.Field + " " + strconv.Quote(e.Value) + ": " + e.Reason
}

// ValidateMIBAssignment reports the first filter the poller could not act on.
//
// Patterns are compiled here rather than at poll time so that a malformed one is
// refused at the edge and never reaches a worker. It is the single definition of
// a usable assignment: the HTTP handlers and the configuration importer both
// call it, so neither can accept a rule the other would reject.
func ValidateMIBAssignment(a MIBAssignment) error {
	for _, group := range assignmentPatternGroups(a) {
		for _, pattern := range group.patterns {
			if _, err := regexp.Compile(pattern); err != nil {
				return &AssignmentFieldError{
					Field:  group.field,
					Value:  pattern,
					Reason: "not a valid regular expression: " + err.Error(),
				}
			}
		}
	}

	if a.MaxOIDs < 0 {
		return &AssignmentFieldError{
			Field:  "max_oids",
			Value:  strconv.Itoa(a.MaxOIDs),
			Reason: "must not be negative",
		}
	}
	return nil
}

// patternGroup pairs a document field with the patterns it holds.
type patternGroup struct {
	field    string
	patterns []string
}

// assignmentPatternGroups lists an assignment's four pattern fields under the
// names the document and the API use for them.
func assignmentPatternGroups(a MIBAssignment) []patternGroup {
	return []patternGroup{
		{"include_oids", a.IncludeOIDs},
		{"exclude_oids", a.ExcludeOIDs},
		{"include_names", a.IncludeNames},
		{"exclude_names", a.ExcludeNames},
	}
}

// ValidateConfig reports everything wrong with a configuration document that can
// be judged without reading the store.
//
// The schema version is checked first and on its own: a document this build
// cannot read must not have its fields reported as individually invalid, which
// would send an operator editing a file that is simply from a newer build.
func ValidateConfig(doc *Config) error {
	if doc == nil {
		return &ConfigValidationError{Problems: []ConfigProblem{{
			Scope: "document", Reason: "no configuration document was supplied",
		}}}
	}
	if doc.SchemaVersion != ConfigSchemaVersion {
		return &ConfigSchemaError{Found: doc.SchemaVersion, Supported: ConfigSchemaVersion}
	}

	v := &configValidator{}
	v.checkMIBs(doc.MIBs)
	profiles := v.checkProfiles(doc.Profiles)
	v.checkDevices(doc.Devices, profiles)
	return v.err()
}

// configValidator accumulates the problems found in one document.
type configValidator struct {
	problems []ConfigProblem
}

// add records one problem.
func (v *configValidator) add(scope, subject, field, value, reason string) {
	v.problems = append(v.problems, ConfigProblem{
		Scope: scope, Subject: subject, Field: field, Value: value, Reason: reason,
	})
}

// err returns the accumulated problems, or nil when there are none.
func (v *configValidator) err() error {
	if len(v.problems) == 0 {
		return nil
	}
	return &ConfigValidationError{Problems: v.problems}
}

// checkNames reports empty and repeated names within one collection, and returns
// the set of names it saw.
func (v *configValidator) checkNames(scope string, names []string) map[string]struct{} {
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		switch {
		case strings.TrimSpace(name) == "":
			v.add(scope, "", "name", name, "must not be empty")
		case contains(seen, name):
			v.add(scope, name, "name", name, "is used by more than one record")
		default:
			seen[name] = struct{}{}
		}
	}
	return seen
}

// checkMIBs validates the document's module prerequisite list.
func (v *configValidator) checkMIBs(mibs []ConfigMIB) {
	names := make([]string, 0, len(mibs))
	for _, m := range mibs {
		names = append(names, m.Name)
	}
	v.checkNames("mib", names)
}

// checkProfiles validates every profile and returns the set of profile names a
// device may reference.
func (v *configValidator) checkProfiles(profiles []ConfigProfile) map[string]struct{} {
	names := make([]string, 0, len(profiles))
	for _, p := range profiles {
		names = append(names, p.Name)
	}
	seen := v.checkNames("profile", names)

	for _, p := range profiles {
		v.checkAssignments("profile", p.Name, p.MIBs)
	}
	return seen
}

// checkDevices validates every device, including that each profile it names is
// defined by the same document.
func (v *configValidator) checkDevices(devices []ConfigDevice, profiles map[string]struct{}) {
	names := make([]string, 0, len(devices))
	for _, d := range devices {
		names = append(names, d.Name)
	}
	v.checkNames("device", names)

	for _, d := range devices {
		v.checkDevice(d, profiles)
	}
}

// checkDevice validates one device's own fields and its references.
func (v *configValidator) checkDevice(d ConfigDevice, profiles map[string]struct{}) {
	if strings.TrimSpace(d.Address) == "" {
		v.add("device", d.Name, "address", d.Address, "must not be empty")
	}
	if d.Port < 0 || d.Port > MaxDevicePort {
		v.add("device", d.Name, "port", strconv.Itoa(d.Port),
			"must be between 1 and "+strconv.Itoa(MaxDevicePort)+
				", or 0 to use the default of "+strconv.Itoa(DefaultDevicePort))
	}
	if !isSupportedSNMPVersion(d.SNMPVersion) {
		v.add("device", d.Name, "snmp_version", d.SNMPVersion,
			"is not a supported SNMP version; use one of "+
				strings.Join(supportedSNMPVersions, ", "))
	}
	if err := ValidatePollInterval(d.Name, d.PollInterval); err != nil {
		v.add("device", d.Name, "poll_interval", intervalText(d.PollInterval), err.Error())
	}

	seen := make(map[string]struct{}, len(d.Profiles))
	for _, name := range d.Profiles {
		switch {
		case !contains(profiles, name):
			v.add("device", d.Name, "profiles", name,
				"names a profile this document does not define")
		case contains(seen, name):
			v.add("device", d.Name, "profiles", name, "is listed more than once")
		default:
			seen[name] = struct{}{}
		}
	}

	v.checkAssignments("device", d.Name, d.MIBs)
}

// checkAssignments validates one owner's MIB assignments.
func (v *configValidator) checkAssignments(scope, subject string, assignments []ConfigAssignment) {
	seen := make(map[string]struct{}, len(assignments))
	for _, a := range assignments {
		switch {
		case strings.TrimSpace(a.Module) == "":
			v.add(scope, subject, "mibs.module", a.Module, "must not be empty")
			continue
		case contains(seen, a.Module):
			v.add(scope, subject, "mibs.module", a.Module, "is assigned more than once")
			continue
		}
		seen[a.Module] = struct{}{}

		var fieldErr *AssignmentFieldError
		if err := ValidateMIBAssignment(a.MIBAssignment); asAssignmentFieldError(err, &fieldErr) {
			v.add(scope, subject, "mibs["+a.Module+"]."+fieldErr.Field,
				fieldErr.Value, fieldErr.Reason)
		}
	}
}

// asAssignmentFieldError reports whether err is an assignment field failure and
// binds it to target.
func asAssignmentFieldError(err error, target **AssignmentFieldError) bool {
	if err == nil {
		return false
	}
	fieldErr, ok := err.(*AssignmentFieldError)
	if !ok {
		return false
	}
	*target = fieldErr
	return true
}

// isSupportedSNMPVersion reports whether a device may be stored with a version.
func isSupportedSNMPVersion(version string) bool {
	lowered := strings.ToLower(strings.TrimSpace(version))
	for _, supported := range supportedSNMPVersions {
		if lowered == supported {
			return true
		}
	}
	return false
}

// intervalText renders an optional poll interval for an error message.
func intervalText(interval *int) string {
	if interval == nil {
		return ""
	}
	return strconv.Itoa(*interval)
}

// contains reports set membership, keeping the call sites readable.
func contains(set map[string]struct{}, key string) bool {
	_, ok := set[key]
	return ok
}
