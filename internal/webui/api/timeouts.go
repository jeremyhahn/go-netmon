package api

import "time"

// The web UI's time budget, from the outside in.
//
// Every value below is part of one chain, and the chain only works if each link
// fits inside the one outside it:
//
//	SNMPProbeTimeout × SNMPProbeAttempts ≤ SNMPProbeBudget ≤ HandlerBudget < ServerWriteTimeout
//	PollerRequestTimeout                  < PollerSyncBudget ≤ HandlerBudget < ServerWriteTimeout
//	FleetStatusDeviceBudget < PollerRequestTimeout
//	FleetStatusDeviceBudget ≤ FleetStatusBudget  ≤ HandlerBudget < ServerWriteTimeout
//
// When it does not, the failure is not a slow response — it is no response at
// all. net/http closes a connection whose write deadline expires before the
// handler wrote anything, and the caller sees a bare EOF with nothing to act
// on. That is what an unreachable device used to produce here: a probe of
// 10s × 3 attempts = 30s inside a 15s write timeout.
//
// TestTimeoutBudgetOrdering asserts the ordering above, so an edit that
// inverts a pair fails the build rather than the field.
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

	// FleetStatusDeviceBudget caps the wait for one device's status inside a
	// bulk fleet request.
	//
	// It is deliberately shorter than PollerRequestTimeout so that it, and not
	// the poller client's own timeout, is what expires first. The client retries
	// a failed call three times with backoff; letting that ladder run once per
	// device would multiply a hung poller by the size of the fleet. Cutting the
	// call at this deadline turns "the poller is not answering" into one
	// unknown row per device, promptly, instead of a request that never returns.
	FleetStatusDeviceBudget = 2 * time.Second

	// FleetStatusBudget caps an entire bulk fleet request. Devices still
	// waiting when it expires are reported as unknown, so the response arrives
	// on time with every configured device in it either way.
	FleetStatusBudget = 8 * time.Second

	// HandlerBudget caps the outbound work one request may do. It is the
	// widest any single handler is allowed to be.
	HandlerBudget = 10 * time.Second

	// ServerWriteTimeout is the connection's write deadline. It exceeds
	// HandlerBudget so that a handler which spends its entire budget still has
	// time to serialize and write its response instead of having the
	// connection closed underneath it.
	ServerWriteTimeout = 15 * time.Second

	// ConfigImportBudget caps a configuration import, and is deliberately
	// outside the chain above.
	//
	// An import parses every MIB module the bundle carries, which on a restore
	// of a full catalog is a few hundred modules and minutes of work, not the
	// seconds every other endpoint is held to. Widening ServerWriteTimeout for
	// it would widen it for everything, so the import handler extends its own
	// connection deadline to this instead and the rest of the server keeps the
	// tight budget it needs.
	ConfigImportBudget = 10 * time.Minute

	// ServerReadTimeout bounds reading a request, including its body.
	ServerReadTimeout = 15 * time.Second
)
