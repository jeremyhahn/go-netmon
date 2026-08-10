package selector

import (
	"regexp"

	"github.com/jeremyhahn/go-netmon/internal/config"
	"github.com/jeremyhahn/go-netmon/internal/mib"
)

// Kind classifies an object by how the poller would fetch it. It is the only
// classification the selection rules consult, so a client showing "this is a
// column" is showing the same fact the rules acted on.
type Kind string

// The kinds an object can have.
const (
	KindScalar Kind = "scalar"
	KindTable  Kind = "table"
	KindEntry  Kind = "entry"
	KindColumn Kind = "column"
)

// Reason explains a polling decision in a form a program can branch on. A user
// interface that greys out an object needs to say why, and "not readable" and
// "your filter excluded it" call for very different words.
type Reason string

// The reasons a decision can carry.
const (
	// ReasonPolled marks an object the poller will fetch.
	ReasonPolled Reason = "polled"

	// ReasonModuleDisabled marks an object whose module is not being polled at
	// all, either because it is unassigned or because its assignment is off.
	ReasonModuleDisabled Reason = "module-disabled"

	// ReasonNotAccessible marks an object the agent can never return, because
	// its MAX-ACCESS is neither read-only nor read-write. Not-accessible index
	// columns reach a client as labels on other series, never on their own.
	ReasonNotAccessible Reason = "not-accessible"

	// ReasonNotCurrent marks a readable object whose STATUS is deprecated or
	// obsolete, which the poller declines to spend a varbind on.
	ReasonNotCurrent Reason = "not-current"

	// ReasonCoveredByTable marks a row node or a column whose table is already
	// being walked. One walk returns every column of every row, so fetching
	// these separately turns one request into dozens for the same data.
	ReasonCoveredByTable Reason = "covered-by-table-walk"

	// ReasonExcluded marks an object an exclude pattern removed.
	ReasonExcluded Reason = "excluded-by-filter"

	// ReasonNotIncluded marks an object no include pattern matched, in a
	// module that has include patterns.
	ReasonNotIncluded Reason = "not-included-by-filter"

	// ReasonMaxOIDs marks an object dropped because its module had already
	// contributed max_oids objects by the time the object was reached.
	ReasonMaxOIDs Reason = "capped-by-max-oids"
)

// Decision is one object's polling verdict.
type Decision struct {
	// Object is the object the verdict is about. It is the caller's own
	// pointer, not a copy.
	Object *mib.OIDMetadata

	// Kind is how the poller would fetch the object.
	Kind Kind

	// Polled reports whether the poller will issue a request that returns it.
	Polled bool

	// Reason is ReasonPolled when Polled is true, and otherwise names what
	// stopped it.
	Reason Reason
}

// Plan decides, for every object given, whether a device polling the named
// modules would fetch it, and why.
//
// What counts as worth fetching depends on how the poller gets it:
//
//   - Scalars are fetched with GET, so they must be readable. A not-accessible
//     scalar can never be returned and asking costs a varbind every poll.
//   - Tables are fetched with a walk of the table node. That node is itself
//     not-accessible in essentially every MIB — MAX-ACCESS describes the node,
//     not its subtree — so readability is the wrong test for it. One walk of a
//     table returns every column and every row.
//   - Columns are therefore redundant when their table is already selected;
//     walking each of them separately turns one request into dozens.
//   - Entry (row) nodes describe structure and add nothing over the table.
//
// A module may also carry a filter narrowing it further, which is how a device
// polls part of a large MIB rather than all of it. A filter that excludes a
// column still leaves its table walked, so filters that need to reduce SNMP
// traffic should exclude the table.
//
// Decisions come back in the order the objects were given, which matters: a
// module's max_oids cap is spent in that order, so the same objects must be
// presented in the same order to get the same answer.
func Plan(objects []*mib.OIDMetadata, modules []string, filters map[string]config.ModuleFilter) []Decision {
	selected := make(map[string]bool, len(modules))
	for _, module := range modules {
		selected[module] = true
	}

	// Compile each module's filter once rather than per object.
	compiled := make(map[string]*moduleFilter, len(filters))
	for module, filter := range filters {
		compiled[module] = compileModuleFilter(filter)
	}

	walked := walkedTables(objects, selected, compiled)

	counts := make(map[string]int, len(selected))
	decisions := make([]Decision, 0, len(objects))
	for _, object := range objects {
		decisions = append(decisions, decide(object, selected, compiled, walked, counts))
	}
	return decisions
}

// Polled returns the objects a device polling the named modules will fetch.
//
// It is Plan with the rejected objects dropped, which is deliberate: the poller
// and anything explaining the poller must not be able to disagree, and the only
// way to guarantee that is for there to be one rule and one implementation of
// it.
func Polled(objects []*mib.OIDMetadata, modules []string, filters map[string]config.ModuleFilter) []*mib.OIDMetadata {
	decisions := Plan(objects, modules, filters)

	polled := make([]*mib.OIDMetadata, 0, len(decisions))
	for _, decision := range decisions {
		if decision.Polled {
			polled = append(polled, decision.Object)
		}
	}
	return polled
}

// Classify reports how the poller would fetch an object.
func Classify(object *mib.OIDMetadata) Kind {
	switch {
	case object.IsTable:
		return KindTable
	case object.IsTableEntry:
		return KindEntry
	case object.IsColumn:
		return KindColumn
	default:
		return KindScalar
	}
}

// decide applies the rules to one object, charging it against its module's cap
// when it is polled.
func decide(
	object *mib.OIDMetadata,
	selected map[string]bool,
	filters map[string]*moduleFilter,
	walked map[string]bool,
	counts map[string]int,
) Decision {
	decision := Decision{Object: object, Kind: Classify(object)}

	if !selected[object.Module] {
		decision.Reason = ReasonModuleDisabled
		return decision
	}

	if reason, ok := fetchable(object, decision.Kind, walked); !ok {
		decision.Reason = reason
		return decision
	}

	if filter := filters[object.Module]; filter != nil {
		if reason, ok := filter.allows(object); !ok {
			decision.Reason = reason
			return decision
		}
		if filter.maxOIDs > 0 && counts[object.Module] >= filter.maxOIDs {
			decision.Reason = ReasonMaxOIDs
			return decision
		}
	}

	// Counting a module with no filter is harmless — nothing reads the count
	// without a cap to compare it against — and avoids a second branch here.
	counts[object.Module]++
	decision.Polled, decision.Reason = true, ReasonPolled
	return decision
}

// fetchable reports whether an object needs its own SNMP request, given the set
// of tables that will already be walked, and names what rules it out when it
// does not.
func fetchable(object *mib.OIDMetadata, kind Kind, walked map[string]bool) (Reason, bool) {
	switch kind {
	case KindTable:
		// Walked, not fetched, so its own access does not apply.
		return ReasonPolled, true

	case KindEntry:
		// The table walk already covers every row.
		return ReasonCoveredByTable, false

	case KindColumn:
		// Only worth a walk of its own when its table is not being walked,
		// which happens when the table node is missing from the store or a
		// filter excluded it.
		if walked[object.TableOID] {
			return ReasonCoveredByTable, false
		}
		return readable(object)

	default:
		// A scalar, fetched with GET, so it has to be readable.
		return readable(object)
	}
}

// readable reports whether the agent can return an object at all, separating
// the two ways mib.OIDMetadata.IsPollable can refuse so a client can say which
// one applies.
func readable(object *mib.OIDMetadata) (Reason, bool) {
	if object.IsPollable() {
		return ReasonPolled, true
	}
	if object.Access != "read-only" && object.Access != "read-write" {
		return ReasonNotAccessible, false
	}
	return ReasonNotCurrent, false
}

// walkedTables returns the OIDs of the tables that will be walked. A column
// belonging to one of these is already covered.
func walkedTables(
	objects []*mib.OIDMetadata,
	selected map[string]bool,
	filters map[string]*moduleFilter,
) map[string]bool {
	walked := make(map[string]bool)
	for _, object := range objects {
		if !object.IsTable || !selected[object.Module] {
			continue
		}
		if filter := filters[object.Module]; filter != nil {
			if _, ok := filter.allows(object); !ok {
				continue
			}
		}
		walked[object.OID] = true
	}
	return walked
}

// moduleFilter is a config.ModuleFilter with its patterns compiled.
type moduleFilter struct {
	includeOIDs  []*regexp.Regexp
	excludeOIDs  []*regexp.Regexp
	includeNames []*regexp.Regexp
	excludeNames []*regexp.Regexp
	maxOIDs      int
}

// compileModuleFilter compiles a filter's patterns, discarding any that do not
// compile. A malformed pattern must not take the whole device's polling with
// it, so it is simply not applied.
func compileModuleFilter(f config.ModuleFilter) *moduleFilter {
	return &moduleFilter{
		includeOIDs:  compilePatterns(f.IncludeOIDs),
		excludeOIDs:  compilePatterns(f.ExcludeOIDs),
		includeNames: compilePatterns(f.IncludeNames),
		excludeNames: compilePatterns(f.ExcludeNames),
		maxOIDs:      f.MaxOIDs,
	}
}

// allows reports whether an object survives the filter, naming the clause that
// removed it when it does not. Excludes are checked first so that an object
// matching both an include and an exclude is dropped.
func (f *moduleFilter) allows(object *mib.OIDMetadata) (Reason, bool) {
	if matchesAny(object.OID, f.excludeOIDs) || matchesAny(object.Name, f.excludeNames) {
		return ReasonExcluded, false
	}
	if len(f.includeOIDs) > 0 && !matchesAny(object.OID, f.includeOIDs) {
		return ReasonNotIncluded, false
	}
	if len(f.includeNames) > 0 && !matchesAny(object.Name, f.includeNames) {
		return ReasonNotIncluded, false
	}
	return ReasonPolled, true
}
