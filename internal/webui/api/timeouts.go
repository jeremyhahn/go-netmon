package api

import "time"

// The web UI's time budget: each value fits inside the one outside it, as TestTimeoutBudgetOrdering asserts.
//
//	SNMPProbeTimeout × SNMPProbeAttempts ≤ SNMPProbeBudget ≤ HandlerBudget < ServerWriteTimeout
//	PollerRequestTimeout                  < PollerSyncBudget ≤ HandlerBudget < ServerWriteTimeout
//	PollerRequestTimeout                  < FleetStatusBudget ≤ HandlerBudget < ServerWriteTimeout
const (
	// SNMPProbeTimeout is the deadline for a single SNMP request attempt. A
	// device that has not answered in this long on a LAN is not going to.
	SNMPProbeTimeout = 2 * time.Second

	// SNMPProbeRetries is how many times a probe retries after its first
	// attempt.
	SNMPProbeRetries = 2

	// SNMPProbeAttempts is the total number of SNMP attempts a probe makes.
	SNMPProbeAttempts = SNMPProbeRetries + 1

	// SNMPProbeBudget caps an entire probe, including connect and encoding, so
	// a caller-supplied timeout can never push a handler past its budget. It
	// covers SNMPProbeTimeout × SNMPProbeAttempts with room to spare.
	SNMPProbeBudget = 8 * time.Second

	// PollerRequestTimeout caps one HTTP call from the web UI to the poller's
	// control API.
	PollerRequestTimeout = 3 * time.Second

	// PollerSyncBudget caps a whole best-effort push of a device to the
	// poller, retries included. The push is not reported to the caller, so it
	// must never be what makes a request time out.
	PollerSyncBudget = 5 * time.Second

	// FleetStatusBudget caps a whole fleet request, above PollerRequestTimeout.
	FleetStatusBudget = 8 * time.Second

	// HandlerBudget caps the outbound work one request may do. It is the
	// widest any single handler is allowed to be.
	HandlerBudget = 10 * time.Second

	// ServerWriteTimeout exceeds HandlerBudget so a handler has time to write its response.
	ServerWriteTimeout = 15 * time.Second

	// ConfigImportBudget caps a configuration import, outside the chain; the import handler extends its own write deadline.
	ConfigImportBudget = 10 * time.Minute

	// ServerReadTimeout bounds reading a request, including its body.
	ServerReadTimeout = 15 * time.Second
)
