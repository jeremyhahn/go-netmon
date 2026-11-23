package metrics

import (
	"strings"

	"github.com/jeremyhahn/go-netmon/internal/mib"
)

// metricNamePrefix is prepended to every metric name this package emits.
const metricNamePrefix = "snmp_"

// objectNameIndex resolves an SNMP instance OID to the Prometheus metric name
// of the MIB object it instantiates.
//
// An instance OID is an object OID followed by instance sub-identifiers: ".0"
// for a scalar, the encoded row index for a table column. Resolving one is
// therefore a longest-prefix match of the instance OID against the OIDs of
// every instantiable object -- every scalar and every column -- known to the
// MIB store. The instance sub-identifiers belong in labels, never in the
// metric name: putting them in the name gives Prometheus one metric name per
// table row, which is unqueryable by hand and defeats aggregation.
//
// The match is done by probing successively shorter prefixes of the instance
// OID in a map, longest first, rather than by descending a trie of
// sub-identifiers. Both are longest-prefix structures, but the probe stops at
// the first hit -- one or two probes for the overwhelmingly common cases of a
// scalar and a single-sub-identifier row index -- whereas a trie descent
// always pays one step per sub-identifier of the object OID, around a dozen
// for a typical enterprise OID. Each probe is a map lookup on a substring of
// the caller's OID, so a lookup allocates nothing, and the metric name is
// precomputed at build time so a hit copies no bytes either.
//
// An objectNameIndex is immutable once built; a rebuild produces a new value
// that is published by atomic pointer swap (see TableIndexCache.nameIndex),
// so lookups on the poll path never take a lock.
type objectNameIndex struct {
	// metricNames maps an object OID, normalized without its leading dot,
	// to the complete metric name for that object.
	metricNames map[string]string

	// minSubIDs and maxSubIDs are the fewest and most sub-identifiers of
	// any indexed object OID. They bound the probe: prefixes longer than
	// maxSubIDs cannot be an object OID and are skipped without a lookup,
	// and prefixes shorter than minSubIDs end the search.
	minSubIDs int
	maxSubIDs int
}

// newObjectNameIndex builds an index over the instantiable objects in oids.
//
// Only scalars and columns are indexed. Tables, conceptual rows and bare OID
// branch nodes are never instantiated by an agent, so admitting them would
// let an instance of an unknown column resolve to its enclosing row and
// collapse every unknown column of that table onto a single metric name --
// distinct series silently overwriting one another. Leaving them out means
// such an OID stays unresolved and keeps its raw-OID name, which is visible
// and lossless.
//
// The first definition of an OID wins, so an OID exported by several modules
// resolves to a stable name across rebuilds.
func newObjectNameIndex(oids []*mib.OIDMetadata) *objectNameIndex {
	index := &objectNameIndex{metricNames: make(map[string]string, len(oids))}

	for _, oid := range instantiableObjects(oids) {
		key := strings.TrimPrefix(oid.OID, ".")
		if _, exists := index.metricNames[key]; exists {
			continue
		}
		index.metricNames[key] = metricNamePrefix + SanitizeMetricName(oid.Name)

		subIDs := countSubIDs(key)
		if index.minSubIDs == 0 || subIDs < index.minSubIDs {
			index.minSubIDs = subIDs
		}
		if subIDs > index.maxSubIDs {
			index.maxSubIDs = subIDs
		}
	}

	return index
}

// metricNameFor returns the metric name of the MIB object that instanceOID
// instantiates, and whether the object was found. A nil or empty index
// resolves nothing. It allocates nothing.
func (ix *objectNameIndex) metricNameFor(instanceOID string) (string, bool) {
	if ix == nil || len(ix.metricNames) == 0 {
		return "", false
	}

	candidate := strings.TrimPrefix(instanceOID, ".")

	// Walk the prefixes of the instance OID from longest to shortest,
	// probing only those that could be an object OID. Prefixes longer than
	// the deepest indexed object are trimmed without a lookup, so a long
	// row index -- an InetAddress index carries eighteen sub-identifiers --
	// costs pointer arithmetic rather than map lookups, and the search ends
	// once the prefix is shorter than the shallowest indexed object.
	for subIDs := countSubIDs(candidate); subIDs >= ix.minSubIDs; subIDs-- {
		if subIDs <= ix.maxSubIDs {
			if name, found := ix.metricNames[candidate]; found {
				return name, true
			}
		}

		cut := strings.LastIndexByte(candidate, '.')
		if cut < 0 {
			break
		}
		candidate = candidate[:cut]
	}

	return "", false
}

// resolveMetricName returns the metric name for an instance OID and whether
// it was resolved from MIB metadata.
//
// An OID that no loaded MIB describes keeps the raw-OID name it has always
// had. Dropping the sample instead would silently lose data, and there is no
// object name to give it. Callers report the unresolved count so the gap is
// measurable rather than invisible.
func resolveMetricName(index *objectNameIndex, instanceOID string) (string, bool) {
	if name, found := index.metricNameFor(instanceOID); found {
		return name, true
	}
	return SanitizeMetricName(metricNamePrefix + instanceOID), false
}

// countSubIDs returns the number of dot-separated sub-identifiers in a
// normalized OID.
func countSubIDs(oid string) int {
	if oid == "" {
		return 0
	}
	return strings.Count(oid, ".") + 1
}

// instantiableObjects returns the objects an agent can actually be asked for,
// in the order they were swept.
//
// It is shared by the metric name index and the metric catalog so that neither
// can come to describe a different set of objects than the other: they answer
// two halves of one question -- what a sample is called, and what that name
// means -- and a sample named from one while described by the other is worse
// than either being wrong alone.
//
// Tables, conceptual rows and bare OID branch nodes are never instantiated by
// an agent, so admitting them would let an instance of an unknown column
// resolve to its enclosing row and collapse distinct series onto one name.
func instantiableObjects(oids []*mib.OIDMetadata) []*mib.OIDMetadata {
	instantiable := make([]*mib.OIDMetadata, 0, len(oids))
	for _, oid := range oids {
		if oid == nil || oid.OID == "" || oid.Name == "" {
			continue
		}
		if !oid.IsScalar && !oid.IsColumn {
			continue
		}
		instantiable = append(instantiable, oid)
	}
	return instantiable
}
