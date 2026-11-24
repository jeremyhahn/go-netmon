package metrics

import (
	"strings"

	"github.com/jeremyhahn/go-netmon/internal/mib"
)

// metricNamePrefix is prepended to every metric name this package emits.
const metricNamePrefix = "snmp_"

// objectNameIndex resolves an instance OID to its object's metric name by longest-prefix map probes; immutable and lock-free.
type objectNameIndex struct {
	// metricNames maps an object OID, normalized without its leading dot,
	// to the complete metric name for that object.
	metricNames map[string]string

	// minSubIDs and maxSubIDs bound the prefix probe.
	minSubIDs int
	maxSubIDs int
}

// newObjectNameIndex indexes scalars and columns only; the first definition of an OID wins.
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

	// Probe prefixes longest first, only those within the indexed length bounds.
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

// resolveMetricName returns the metric name for an instance OID; unknown OIDs keep their raw-OID name.
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

// instantiableObjects returns the scalars and columns, in sweep order, shared by the name index and the catalog.
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
