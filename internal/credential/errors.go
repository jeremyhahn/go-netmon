package credential

import (
	"errors"
	"fmt"
)

// ErrNoSealer is a seal attempted without a sealer to seal with.
var ErrNoSealer = errors.New("credential: no sealer")

// SealError reports a credential that could not be sealed. The plaintext has
// been wiped regardless.
type SealError struct {
	Err error
}

func (e *SealError) Error() string { return "credential: seal: " + e.Err.Error() }

func (e *SealError) Unwrap() error { return e.Err }

// OpenError reports a sealed credential that could not be opened: destroyed,
// failed authentication, or locked memory refused.
type OpenError struct {
	Err error
}

func (e *OpenError) Error() string { return "credential: open: " + e.Err.Error() }

func (e *OpenError) Unwrap() error { return e.Err }

// ErrLockedMemoryTooSmall reports an RLIMIT_MEMLOCK too small to open a
// credential at all. Every LockedMemoryError matches it.
var ErrLockedMemoryTooSmall = errors.New("credential: RLIMIT_MEMLOCK too small to open a credential")

// LockedMemoryError reports a credential whose open needs more locked memory
// than the process's RLIMIT_MEMLOCK allows, however long it waits.
type LockedMemoryError struct {
	Limit int64 // the soft RLIMIT_MEMLOCK, in bytes
	Need  int64 // the RLIMIT_MEMLOCK the open needs, sealer keys included
}

func (e *LockedMemoryError) Error() string {
	return fmt.Sprintf("credential: RLIMIT_MEMLOCK is %d bytes, below the %d bytes opening a credential needs; "+
		"raise it with `ulimits: memlock` in Docker Compose, `docker run --ulimit memlock=<bytes>`, "+
		"LimitMEMLOCK= in a systemd unit, or `ulimit -l`", e.Limit, e.Need)
}

func (e *LockedMemoryError) Unwrap() error { return ErrLockedMemoryTooSmall }
