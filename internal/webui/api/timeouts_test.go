package api

import (
	"testing"
	"time"
)

// TestTimeoutBudgetOrdering pins the nesting the web UI's time budget depends
// on. Each link must fit inside the one outside it; when it does not, a slow
// device does not produce a slow response, it produces no response at all,
// because the connection's write deadline expires before the handler writes.
//
// This test exists so that an edit which inverts a pair — the failure that
// originally shipped, a 30s probe inside a 15s write timeout — fails here
// rather than in the field.
func TestTimeoutBudgetOrdering(t *testing.T) {
	snmpWorstCase := SNMPProbeTimeout * time.Duration(SNMPProbeAttempts)

	tests := []struct {
		name         string
		inner, outer time.Duration
		innerName    string
		outerName    string
	}{
		{
			name:      "SNMP retries fit inside the probe budget",
			innerName: "SNMPProbeTimeout x SNMPProbeAttempts", inner: snmpWorstCase,
			outerName: "SNMPProbeBudget", outer: SNMPProbeBudget,
		},
		{
			name:      "the probe budget fits inside the handler budget",
			innerName: "SNMPProbeBudget", inner: SNMPProbeBudget,
			outerName: "HandlerBudget", outer: HandlerBudget,
		},
		{
			name:      "one poller request fits inside the poller sync budget",
			innerName: "PollerRequestTimeout", inner: PollerRequestTimeout,
			outerName: "PollerSyncBudget", outer: PollerSyncBudget,
		},
		{
			name:      "the poller sync budget fits inside the handler budget",
			innerName: "PollerSyncBudget", inner: PollerSyncBudget,
			outerName: "HandlerBudget", outer: HandlerBudget,
		},
		{
			name:      "the poller request timeout fits inside the fleet budget",
			innerName: "PollerRequestTimeout", inner: PollerRequestTimeout,
			outerName: "FleetStatusBudget", outer: FleetStatusBudget,
		},
		{
			name:      "the fleet budget fits inside the handler budget",
			innerName: "FleetStatusBudget", inner: FleetStatusBudget,
			outerName: "HandlerBudget", outer: HandlerBudget,
		},
		{
			name:      "the handler budget fits inside the write timeout",
			innerName: "HandlerBudget", inner: HandlerBudget,
			outerName: "ServerWriteTimeout", outer: ServerWriteTimeout,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.inner > tt.outer {
				t.Fatalf("%s (%v) exceeds %s (%v): a handler cannot answer inside its deadline",
					tt.innerName, tt.inner, tt.outerName, tt.outer)
			}
		})
	}

	// The outermost pair must have real headroom, not merely be equal: a
	// handler that spends its whole budget still has to serialize and write.
	if slack := ServerWriteTimeout - HandlerBudget; slack < time.Second {
		t.Errorf("only %v between HandlerBudget and ServerWriteTimeout; too little to write a response", slack)
	}

	// The fleet view makes one call to the poller however large the fleet is,
	// so the client's own timeout must expire inside the fleet budget: the
	// request then answers with every device marked unknown rather than being
	// cut off mid-flight with nothing written.
	if PollerRequestTimeout >= FleetStatusBudget {
		t.Errorf("PollerRequestTimeout (%v) does not expire before FleetStatusBudget (%v); the fleet request would be cut off before it could answer",
			PollerRequestTimeout, FleetStatusBudget)
	}

	if SNMPProbeAttempts != SNMPProbeRetries+1 {
		t.Errorf("SNMPProbeAttempts = %d, want retries+1 = %d", SNMPProbeAttempts, SNMPProbeRetries+1)
	}
}

// TestServerTimeoutsComeFromTheBudget keeps the listener wired to the constants
// the ordering test checks, so the assertions above describe the running server.
func TestServerTimeoutsComeFromTheBudget(t *testing.T) {
	srv := newHTTPServer("127.0.0.1:0", nil)

	if srv.WriteTimeout != ServerWriteTimeout {
		t.Errorf("WriteTimeout = %v, want %v", srv.WriteTimeout, ServerWriteTimeout)
	}
	if srv.ReadTimeout != ServerReadTimeout {
		t.Errorf("ReadTimeout = %v, want %v", srv.ReadTimeout, ServerReadTimeout)
	}
}
