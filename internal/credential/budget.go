package credential

import (
	"context"
	"log/slog"
	"sync"

	enclave "github.com/jeremyhahn/go-enclave"
	"golang.org/x/sync/semaphore"
)

// LockedBudget makes concurrent secret opens wait for RLIMIT_MEMLOCK headroom instead of failing.

// keyPageBytes is the locked memory one seal or open of a value holds for its
// data key, which go-enclave unwraps into a locked page of its own.
var keyPageBytes = enclave.LockedSize(enclave.RootKeyBytes)

// sealerKeyPages is the locked memory the sealer holds for the life of the
// process, outside any seal or open: its wrapping key and current KEK, and one
// page more while it mints the next KEK.
const sealerKeyPages = 3

// LockedCost is the locked memory opening an n-byte secret holds: plaintext pages plus the data key's page; absent costs 0.
func LockedCost(n int) int64 {
	if n <= 0 {
		return 0
	}
	return enclave.LockedSize(n) + keyPageBytes
}

// LimitFor is the RLIMIT_MEMLOCK a process needs to hold cost bytes of opens
// at once: cost plus the sealer's own keys.
func LimitFor(cost int64) int64 {
	return cost + sealerKeyPages*keyPageBytes
}

// LockedBudget bounds the locked memory that seals and opens hold at once, to
// what RLIMIT_MEMLOCK leaves once the sealer's keys are locked. A caller asks
// for what it is about to lock and waits until that much is free.
type LockedBudget struct {
	limit    enclave.LockedMemoryLimit
	capacity int64
	sem      *semaphore.Weighted // nil when no ceiling binds
}

// NewLockedBudget sizes a budget from limit. A limit that binds nothing gives a
// budget that never waits. A limit too small for even the smallest secret to
// be opened is a LockedMemoryError.
func NewLockedBudget(limit enclave.LockedMemoryLimit) (*LockedBudget, error) {
	if !limit.Bounded {
		return &LockedBudget{limit: limit}, nil
	}
	b := &LockedBudget{limit: limit, capacity: limit.Bytes - sealerKeyPages*keyPageBytes}
	if err := b.Fits(LockedCost(1)); err != nil {
		return nil, err
	}
	b.sem = semaphore.NewWeighted(b.capacity)
	return b, nil
}

// Capacity reports the bytes of seals and opens the budget admits at once, and
// whether it bounds them at all.
func (b *LockedBudget) Capacity() (bytes int64, bounded bool) {
	return b.capacity, b.limit.Bounded
}

// Fits reports, as a LockedMemoryError, a cost the budget can never admit.
func (b *LockedBudget) Fits(cost int64) error {
	if !b.limit.Bounded || cost <= b.capacity {
		return nil
	}
	return &LockedMemoryError{Limit: b.limit.Bytes, Need: LimitFor(cost)}
}

// Acquire waits for cost bytes and holds them until the idempotent release; it fails at once for a cost that can never fit.
func (b *LockedBudget) Acquire(ctx context.Context, cost int64) (release func(), err error) {
	if b.sem == nil || cost <= 0 {
		return func() {}, nil
	}
	if err := b.Fits(cost); err != nil {
		return nil, err
	}
	if err := b.sem.Acquire(ctx, cost); err != nil {
		return nil, err
	}
	var once sync.Once
	return func() { once.Do(func() { b.sem.Release(cost) }) }, nil
}

var (
	processBudgetOnce sync.Once
	processBudget     *LockedBudget
	processBudgetErr  error
)

// ProcessBudget is the process's one LockedBudget, sized from the
// RLIMIT_MEMLOCK the process has when it is first asked for, which it logs.
// Every seal and open of a credential goes through it.
func ProcessBudget() (*LockedBudget, error) {
	processBudgetOnce.Do(func() {
		processBudget, processBudgetErr = processBudgetFrom(enclave.DiscoverLockedMemoryLimit)
	})
	return processBudget, processBudgetErr
}

func processBudgetFrom(discover func() (enclave.LockedMemoryLimit, error)) (*LockedBudget, error) {
	limit, err := discover()
	if err != nil {
		return nil, err
	}
	b, err := NewLockedBudget(limit)
	if err != nil {
		return nil, err
	}
	if limit.Bounded {
		slog.Info("credential locked-memory budget",
			"rlimit_memlock_bytes", limit.Bytes,
			"budget_bytes", b.capacity,
			"page_bytes", keyPageBytes)
	} else {
		slog.Info("credential locked-memory budget unbounded", "source", limit.Source.String())
	}
	return b, nil
}
