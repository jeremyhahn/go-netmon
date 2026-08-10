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

func (e *SNMPAgentListenError) Error() string {
	return "testsupport: listen for snmp agent: " + e.Err.Error()
}

func (e *SNMPAgentListenError) Unwrap() error { return e.Err }
