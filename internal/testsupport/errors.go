package testsupport

// RepoRootError reports a failure to locate the repository root.
type RepoRootError struct {
	Reason string
}

func (e *RepoRootError) Error() string {
	return "testsupport: locate repository root: " + e.Reason
}

// SNMPAgentListenError reports a failure to bind the in-process SNMP agent's
// loopback socket.
type SNMPAgentListenError struct {
	Err error
}

// USMKeyError reports a test SNMPv3 user whose keys could not be derived.
type USMKeyError struct {
	Err error
}

func (e *USMKeyError) Error() string { return "testsupport: derive SNMPv3 user keys: " + e.Err.Error() }

func (e *USMKeyError) Unwrap() error { return e.Err }

// USMRejectedError reports an SNMPv3 request the test agent refused: it did
// not authenticate, or decrypt, under the agent's user.
type USMRejectedError struct {
	Err error
}

func (e *USMRejectedError) Error() string {
	return "testsupport: SNMPv3 request rejected: " + e.Err.Error()
}

func (e *USMRejectedError) Unwrap() error { return e.Err }

func (e *SNMPAgentListenError) Error() string {
	return "testsupport: listen for snmp agent: " + e.Err.Error()
}

func (e *SNMPAgentListenError) Unwrap() error { return e.Err }
