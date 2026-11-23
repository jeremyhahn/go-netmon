// Package credential keeps secrets in go-enclave protected memory.
//
// A Secret holds its material sealed: encrypted on the heap under a key the
// process keeps in locked memory, and decrypted into a locked enclave only for
// the duration of Secret.Use. Nothing in this package keeps the plaintext in a
// Go string or an ordinary []byte.
//
// The package depends on go-enclave's interfaces (Sealed, ByteAccess) and
// declares the one it needs of a sealer, Sealer. The concrete go-enclave sealer
// is constructed in exactly one place, Default; everything else receives a
// Sealer by injection.
package credential

import (
	"context"
	"sync"

	enclave "github.com/jeremyhahn/go-enclave"
)

// Sealer seals material into go-enclave protected memory. go-enclave's own
// Sealer satisfies it.
type Sealer interface {
	// Seal encrypts data and returns the sealed value. It takes ownership of
	// data and wipes it.
	Seal(ctx context.Context, data []byte) (enclave.Sealed, error)
}

var (
	defaultOnce   sync.Once
	defaultSealer Sealer
)

// Default returns the process's sealer: go-enclave's pure-software provider
// in root-key mode. It is the only place this program constructs a concrete
// go-enclave sealer.
func Default() Sealer {
	defaultOnce.Do(func() {
		s, err := enclave.NewSealer()
		if err != nil {
			// NewSealer with no options is documented not to fail. Should it
			// ever, every seal reports the reason instead of the process
			// holding credentials in the clear.
			defaultSealer = failedSealer{err: err}
			return
		}
		defaultSealer = s
	})
	return defaultSealer
}

// failedSealer is the default sealer go-enclave refused to construct.
type failedSealer struct{ err error }

func (f failedSealer) Seal(_ context.Context, data []byte) (enclave.Sealed, error) {
	enclave.Wipe(data)
	return nil, f.err
}

// Secret is one sealed credential. A nil *Secret is the absent credential:
// Use hands its function an empty view and Destroy does nothing.
type Secret struct {
	sealed enclave.Sealed
	size   int // plaintext bytes, for LockedCost
}

// Seal seals plaintext and wipes it, on every path. Empty plaintext is no
// credential at all and returns a nil Secret.
func Seal(ctx context.Context, s Sealer, plaintext []byte) (*Secret, error) {
	if len(plaintext) == 0 {
		return nil, nil
	}
	if s == nil {
		enclave.Wipe(plaintext)
		return nil, &SealError{Err: ErrNoSealer}
	}
	size := len(plaintext)
	sealed, err := s.Seal(ctx, plaintext)
	// The sealer wipes what it takes; this covers a sealer that failed before
	// it got that far.
	enclave.Wipe(plaintext)
	if err != nil {
		return nil, &SealError{Err: err}
	}
	return &Secret{sealed: sealed, size: size}, nil
}

// SealString seals a credential that arrived as a string. The string itself
// cannot be wiped; the caller drops its references to it.
func SealString(ctx context.Context, s Sealer, value string) (*Secret, error) {
	return Seal(ctx, s, []byte(value))
}

// Use opens the secret into a locked enclave, calls fn with a view of it and
// destroys the enclave when fn returns. The view is valid only during fn: fn
// must not retain it, or anything built over it, after it returns.
func (s *Secret) Use(ctx context.Context, fn func(plaintext []byte) error) error {
	if s == nil {
		return fn(nil)
	}
	opened, err := openSealed(ctx, s.sealed)
	if err != nil {
		return &OpenError{Err: err}
	}
	defer func() { _ = opened.Destroy() }()
	return opened.WithBytes(fn)
}

// Alive reports whether the secret still holds its sealed material. The
// absent credential is not alive.
func (s *Secret) Alive() bool {
	return s != nil && s.sealed.IsAlive()
}

// Destroy wipes the sealed material. It is idempotent.
func (s *Secret) Destroy() error {
	if s == nil {
		return nil
	}
	return s.sealed.Destroy()
}

// LockedCost is the locked memory, in bytes, that opening s holds. A nil
// Secret costs nothing.
func (s *Secret) LockedCost() int64 {
	if s == nil {
		return 0
	}
	return LockedCost(s.size)
}

// openSealed opens a sealed value. It is a variable so tests can make the
// kernel refuse the lock.
var openSealed = func(ctx context.Context, s enclave.Sealed) (enclave.ByteAccess, error) {
	return s.Open(ctx)
}

// UseAll opens every secret and calls fn with their plaintexts, in order, a
// nil secret as nil. The opens hold their LockedCost against budget, waiting
// until it is free, for as long as fn runs; so many callers can share an
// RLIMIT_MEMLOCK that holds only a few of them at once. The plaintexts are
// valid only until fn returns, as for Use.
func UseAll(ctx context.Context, budget *LockedBudget, secrets []*Secret, fn func(plaintexts [][]byte) error) error {
	var cost int64
	for _, s := range secrets {
		cost += s.LockedCost()
	}
	release, err := budget.Acquire(ctx, cost)
	if err != nil {
		return err
	}
	defer release()
	plaintexts := make([][]byte, len(secrets))
	defer clear(plaintexts)
	return useFrom(ctx, 0, secrets, plaintexts, fn)
}

// useFrom opens secrets[i:] inside one another, so all are open when fn runs.
func useFrom(ctx context.Context, i int, secrets []*Secret, plaintexts [][]byte, fn func([][]byte) error) error {
	if i == len(secrets) {
		return fn(plaintexts)
	}
	return secrets[i].Use(ctx, func(p []byte) error {
		plaintexts[i] = p
		return useFrom(ctx, i+1, secrets, plaintexts, fn)
	})
}
