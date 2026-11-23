package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/jeremyhahn/go-netmon/internal/config"
	"github.com/jeremyhahn/go-netmon/internal/mib"
	"github.com/jeremyhahn/go-netmon/internal/mib/selector"
	"github.com/jeremyhahn/go-netmon/internal/webui/storage"
)

// MIBObjectPlan answers, for one device and one MIB, the question a checkbox
// list has to answer before it can be drawn: which of this module's objects is
// this device actually polling, and why not the rest?
//
// The answer depends on the assignment's filters and on the poller's own rules
// about tables, columns and access, so it cannot be worked out from the
// assignment alone. It is computed here, by the same code the poller runs, and
// not reimplemented in a browser or a CLI.
type MIBObjectPlan struct {
	DeviceID int64  `json:"device_id"`
	Device   string `json:"device"`
	MIBID    int64  `json:"mib_id"`
	MIB      string `json:"mib"`

	// Assignment is the assignment the poller acts on: the device's own when
	// it has one, otherwise the union of the profiles contributing the MIB.
	Assignment storage.MIBAssignment `json:"assignment"`

	// Source names where Assignment came from — "device", "profile" or
	// "default" — because an operator editing a greyed-out object needs to
	// know whether the rule lives on this device or on a shared profile.
	Source string `json:"source"`

	// Total is how many objects the module defines; Polled how many of them
	// this device fetches.
	Total  int `json:"total"`
	Polled int `json:"polled"`

	// Estimate prices the whole module, and is the same estimate
	// /mibs/{mibId}/estimate returns. Each object's Series is derived from its
	// Instances, so the per-object costs and the module total agree.
	Estimate *MIBSeriesEstimate `json:"estimate"`

	// Objects are the module's objects, in the order the poller sees them. The
	// order matters: a max_oids cap is spent in it, so re-sorting this list
	// client-side changes which objects appear capped. A narrowed request
	// carries a slice of that order rather than a different order.
	Objects []MIBObjectDecision `json:"objects"`

	// Page describes which slice of the plan Objects carries, and is absent
	// from an unnarrowed request, which carries all of them. Total and Polled
	// above always count the whole plan, so a paged response still says what
	// the device does with the module as a whole.
	Page *Page `json:"page,omitempty"`
}

// MIBObjectDecision is one object with the verdict on it.
//
// The MIB's own fields are flattened in, so a client that already renders
// MIBObject elsewhere keeps working. Polled and Reason are the fields this
// endpoint exists for; the embedded Pollable reports what the MIB says about
// the object, not what this device does with it.
type MIBObjectDecision struct {
	MIBObject

	// Kind is how the poller would fetch the object: scalar, column, table or
	// entry. It is the classification the selection rules themselves used.
	Kind string `json:"kind"`

	// Polled reports whether this device fetches the object right now.
	Polled bool `json:"polled"`

	// Reason is "polled", or the machine-usable name of what stopped it: one
	// of not-accessible, not-current, covered-by-table-walk,
	// excluded-by-filter, not-included-by-filter, capped-by-max-oids or
	// module-disabled.
	Reason string `json:"reason"`

	// Series is what the object costs in Prometheus series, 0 when it is not
	// polled.
	Series int `json:"series"`
}

// MIBNotAssignedError reports a MIB the device does not poll at all, neither
// directly nor through a profile. There is nothing to explain about a module
// that is not attached, and answering with an all-disabled list would suggest
// the assignment exists and is merely turned off.
type MIBNotAssignedError struct {
	Device string
	MIB    string
}

func (e *MIBNotAssignedError) Error() string {
	return "mib " + e.MIB + " is not assigned to device " + e.Device
}

// PlanMIBObjects reports what a device polls from one module's objects, given
// the assignment that governs it.
//
// The decision comes from selector.Plan, which is the function the poller's own
// selection is built on: selector.Polled is that plan with the rejected objects
// dropped. There is one rule and one implementation of it, so an object this
// reports as polled is an object the poller fetches by construction rather than
// by two pieces of code happening to agree.
//
// instances is the rows per table the cost estimate assumed, used to price
// table-valued objects.
func PlanMIBObjects(
	objects []*mib.OIDMetadata,
	module string,
	assignment storage.MIBAssignment,
	instances int,
) []MIBObjectDecision {
	decisions := selector.Plan(objects,
		assignedModules(module, assignment),
		assignmentFilters(module, assignment))

	columns := pollableColumnsByTable(objects)

	planned := make([]MIBObjectDecision, 0, len(decisions))
	for _, decision := range decisions {
		planned = append(planned, MIBObjectDecision{
			MIBObject: newMIBObject(decision.Object),
			Kind:      string(decision.Kind),
			Polled:    decision.Polled,
			Reason:    string(decision.Reason),
			Series:    objectSeries(decision, columns, instances),
		})
	}
	return planned
}

// assignedModules returns the module list to plan against. A disabled
// assignment contributes no module at all, which is exactly how the poller
// learns a MIB has been paused: resolveDeviceModules leaves it out.
func assignedModules(module string, assignment storage.MIBAssignment) []string {
	if !assignment.Enabled {
		return nil
	}
	return []string{module}
}

// assignmentFilters converts the filtering half of an assignment to what the
// poller is given, omitting the entry entirely for an unfiltered assignment so
// that the plan sees precisely what a poller worker would.
func assignmentFilters(module string, assignment storage.MIBAssignment) map[string]config.ModuleFilter {
	if !assignment.Enabled || !assignment.HasFilters() {
		return nil
	}
	return map[string]config.ModuleFilter{
		module: {
			IncludeOIDs:  assignment.IncludeOIDs,
			ExcludeOIDs:  assignment.ExcludeOIDs,
			IncludeNames: assignment.IncludeNames,
			ExcludeNames: assignment.ExcludeNames,
			MaxOIDs:      assignment.MaxOIDs,
		},
	}
}

// objectSeries prices one object in Prometheus series from the instance count
// the module estimate arrived at, so this endpoint and /estimate can never put
// different numbers on the same table.
//
// A polled scalar costs one series. A polled column costs one per row. A walked
// table costs one per row for each of its pollable columns, since the walk is
// what collects them; those columns are then reported as covered and priced at
// zero, so the per-object costs sum to the module's total instead of counting
// the same data twice.
func objectSeries(decision selector.Decision, pollableColumns map[string]int, instances int) int {
	if !decision.Polled {
		return 0
	}
	// A polled object costs at least one series, whatever the estimator could
	// infer about row counts.
	if instances < 1 {
		instances = 1
	}

	switch decision.Kind {
	case selector.KindTable:
		return pollableColumns[decision.Object.OID] * instances
	case selector.KindColumn:
		return instances
	case selector.KindScalar:
		return 1
	default:
		return 0
	}
}

// pollableColumnsByTable counts, per table OID, the columns a walk of that
// table will actually bring back as series.
func pollableColumnsByTable(objects []*mib.OIDMetadata) map[string]int {
	counts := make(map[string]int)
	for _, object := range objects {
		if object.IsColumn && object.TableOID != "" && object.IsPollable() {
			counts[object.TableOID]++
		}
	}
	return counts
}

// deviceMIBObjectPlan builds the plan for one device and one MIB.
//
// The MIB has to reach the device, directly or through a profile, and the
// assignment used is the effective one — what the poller acts on — rather than
// the device's own, which may not exist.
func (s *Server) deviceMIBObjectPlan(
	ctx context.Context,
	deviceID, mibID int64,
	query planQuery,
) (*MIBObjectPlan, error) {
	device, err := s.storage.GetDevice(ctx, deviceID)
	if err != nil {
		return nil, &ResourceNotFoundError{Kind: "device", ID: deviceID}
	}

	storedMIB, err := s.storage.GetMIB(ctx, mibID)
	if err != nil {
		return nil, &ResourceNotFoundError{Kind: "MIB", ID: mibID}
	}

	deviceMIBs, err := s.storage.GetDeviceMIBs(ctx, deviceID)
	if err != nil {
		return nil, err
	}
	if !holdsMIB(deviceMIBs, mibID) {
		return nil, &MIBNotAssignedError{Device: device.Name, MIB: storedMIB.Name}
	}

	assignments, err := s.loadDeviceAssignments(ctx, deviceID)
	if err != nil {
		return nil, err
	}
	assignment := assignments.effective(mibID)

	objects, err := s.moduleOIDs(storedMIB.Name)
	if err != nil {
		return nil, err
	}
	estimate := s.estimateObjectSeries(ctx, device.Name, storedMIB.Name, objects)

	decisions := PlanMIBObjects(objects, storedMIB.Name, assignment, estimate.Instances)

	plan := &MIBObjectPlan{
		DeviceID:   device.ID,
		Device:     device.Name,
		MIBID:      storedMIB.ID,
		MIB:        storedMIB.Name,
		Assignment: assignment,
		Source:     assignments.source(mibID),
		Estimate:   estimate,
	}

	// Total and Polled describe the whole module, and are counted before the
	// query narrows anything: "12 polled of 293" has to stay true on page two.
	plan.Total = len(decisions)
	for _, decision := range decisions {
		if decision.Polled {
			plan.Polled++
		}
	}

	selected, page, err := selectPage(decisions, query.listQuery, query.keep())
	if err != nil {
		return nil, err
	}
	plan.Objects = selected
	if query.narrowed {
		plan.Page = page
	}
	return plan, nil
}

// holdsMIB reports whether a MIB is among the ones a device carries.
func holdsMIB(mibs []storage.MIB, mibID int64) bool {
	for _, m := range mibs {
		if m.ID == mibID {
			return true
		}
	}
	return false
}

// handleGetDeviceMIBObjects lists a MIB's objects with, for each, whether this
// device is polling it and why.
//
// The query narrows which objects the response carries — search, polled, offset
// and limit — but never which order they are in: the plan's order is the poller's
// own, and capped-by-max-oids is only meaningful in it.
func (s *Server) handleGetDeviceMIBObjects(w http.ResponseWriter, r *http.Request) {
	deviceID, mibID, ok := assignmentIDs(w, r, "device")
	if !ok {
		return
	}

	// Refuse a malformed query before the device, the MIB and the module's
	// objects are read.
	query, err := parsePlanQuery(r.URL.Query())
	if err != nil {
		respondObjectPlanError(w, err)
		return
	}

	plan, err := s.deviceMIBObjectPlan(r.Context(), deviceID, mibID, query)
	if err != nil {
		respondObjectPlanError(w, err)
		return
	}

	respondJSON(w, http.StatusOK, plan)
}

// respondObjectPlanError maps a plan failure to a status. Naming a device, a
// MIB or a pairing that does not exist is a missing resource, not a server
// fault, and must not be reported as one.
func respondObjectPlanError(w http.ResponseWriter, err error) {
	if isQueryFault(err) {
		respondQueryError(w, err)
		return
	}

	var (
		notFound    *ResourceNotFoundError
		notAssigned *MIBNotAssignedError
	)
	if errors.As(err, &notFound) || errors.As(err, &notAssigned) {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	http.Error(w, err.Error(), http.StatusInternalServerError)
}
