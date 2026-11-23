package api

import (
	"fmt"

	"github.com/jeremyhahn/go-netmon/internal/mib"
)

// Whether a module can produce anything at all.
//
// A MIB can be imported, assigned and polled and still yield nothing, and until
// this the only way to find out was an empty dashboard. Two kinds of module do
// it, and they need different advice:
//
//   - A registry module defines only OBJECT IDENTIFIER assignments — names in a
//     vendor's tree, with nothing measurable under them. ATEN-PRODUCTS-MIB says
//     so in its own header: "this file only provides a product-level
//     description". CISCO-PRODUCTS-MIB is 2888 of them. No amount of polling
//     will produce a series, and nothing is wrong.
//
//   - A module whose imports did not resolve parses "successfully" and comes out
//     with no usable OIDs, because the parent identifier its objects hang off
//     came from a module that is not in the store. That is fixable: import the
//     missing module.
//
// The distinction is what makes the warning worth showing rather than a shrug.

// MIBPollability reports whether a module has anything worth polling.
type MIBPollability struct {
	// Objects is how many the module defines, of any kind.
	Objects int
	// Pollable is how many of them the poller would ask a device for.
	Pollable int
	// Unresolved is how many carry no OID, which is what an import that did
	// not resolve leaves behind.
	Unresolved int

	// Warning is empty when the module will produce data. Otherwise it says
	// what will happen and what to do about it, in one sentence an operator can
	// act on.
	Warning string `json:"warning,omitempty"`
}

// UsableForPolling reports whether assigning this module can produce a series.
func (p MIBPollability) UsableForPolling() bool { return p.Pollable > 0 }

// assessPollability examines a module's objects.
func assessPollability(mibName string, objects []*mib.OIDMetadata) MIBPollability {
	result := MIBPollability{Objects: len(objects)}

	for _, object := range objects {
		if object == nil {
			continue
		}
		if object.OID == "" {
			result.Unresolved++
			continue
		}
		if object.IsPollable() {
			result.Pollable++
		}
	}

	result.Warning = pollabilityWarning(mibName, result)
	return result
}

// pollabilityWarning is the sentence shown when a module cannot produce data.
func pollabilityWarning(mibName string, p MIBPollability) string {
	if p.UsableForPolling() {
		return ""
	}

	switch {
	case p.Objects == 0:
		return fmt.Sprintf(
			"%s defines no objects, so it cannot produce any metrics. "+
				"Re-import it and check the file is a MIB module.", mibName)

	case p.Unresolved == p.Objects:
		// Every object lost its OID: the module's parent identifier came from
		// somewhere that is not in the store.
		return fmt.Sprintf(
			"%s parsed but none of its %d objects resolved to an OID, which means a "+
				"module it IMPORTS is missing. Import that module and re-import %s. "+
				"Until then it will produce no metrics.",
			mibName, p.Objects, mibName)

	case p.Unresolved > 0:
		return fmt.Sprintf(
			"%s has no pollable objects: %d of its %d did not resolve to an OID, and "+
				"the rest are not readable values. Nothing will be polled from it.",
			mibName, p.Unresolved, p.Objects)

	default:
		// Everything resolved and nothing is readable. This is a registry
		// module and there is nothing to fix.
		return fmt.Sprintf(
			"%s defines %d objects and none of them are readable values — it is a "+
				"registry of identifiers rather than data. Assigning it is harmless "+
				"but it will produce no metrics and no dashboard panels.",
			mibName, p.Objects)
	}
}
