package credential

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	enclave "github.com/jeremyhahn/go-enclave"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func bounded(bytes int64) enclave.LockedMemoryLimit {
	return enclave.LockedMemoryLimit{Bytes: bytes, Bounded: true}
}

func TestLockedCostIsThePlaintextPagesAndTheKeyPage(t *testing.T) {
	page := enclave.LockedSize(1)
	assert.Equal(t, 2*page, LockedCost(1))
	assert.Equal(t, 2*page, LockedCost(int(page)))
	assert.Equal(t, 3*page, LockedCost(int(page)+1))
	assert.Zero(t, LockedCost(0), "an absent secret opens nothing")
}

func TestLimitForAddsTheSealerKeys(t *testing.T) {
	page := enclave.LockedSize(1)
	assert.Equal(t, LockedCost(1)+sealerKeyPages*page, LimitFor(LockedCost(1)))
}

func TestBudgetBelowOneOpenIsTypedError(t *testing.T) {
	limit := LimitFor(LockedCost(1)) - 1
	_, err := NewLockedBudget(bounded(limit))
	var lmErr *LockedMemoryError
	require.ErrorAs(t, err, &lmErr)
	assert.ErrorIs(t, err, ErrLockedMemoryTooSmall)
	assert.Equal(t, limit, lmErr.Limit)
	assert.Equal(t, LimitFor(LockedCost(1)), lmErr.Need)
	assert.Contains(t, err.Error(), "RLIMIT_MEMLOCK")
	assert.Contains(t, err.Error(), "memlock", "the message says how to raise it")
}

func TestUnboundedLimitIsNoBound(t *testing.T) {
	b, err := NewLockedBudget(enclave.LockedMemoryLimit{Bounded: false})
	require.NoError(t, err)
	_, isBounded := b.Capacity()
	assert.False(t, isBounded)
	require.NoError(t, b.Fits(1<<62))
	release, err := b.Acquire(context.Background(), 1<<62)
	require.NoError(t, err)
	release()
}

func TestAcquireBeyondTheBudgetFailsAtOnce(t *testing.T) {
	b, err := NewLockedBudget(bounded(LimitFor(LockedCost(1))))
	require.NoError(t, err)
	_, err = b.Acquire(context.Background(), 2*LockedCost(1))
	assert.ErrorIs(t, err, ErrLockedMemoryTooSmall, "a request that can never fit must not wait forever")
}

func TestAcquireWaitsForBudgetAndRespectsContext(t *testing.T) {
	b, err := NewLockedBudget(bounded(LimitFor(LockedCost(1))))
	require.NoError(t, err)
	release, err := b.Acquire(context.Background(), LockedCost(1))
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = b.Acquire(ctx, LockedCost(1))
	assert.ErrorIs(t, err, context.DeadlineExceeded)

	got := make(chan error, 1)
	go func() {
		r, err := b.Acquire(context.Background(), LockedCost(1))
		if err == nil {
			r()
		}
		got <- err
	}()
	release()
	release() // idempotent: must not hand back the bytes twice
	require.NoError(t, <-got)
}

func TestUseAllWithATinyBudgetRunsEveryCallerInTurn(t *testing.T) {
	ctx := context.Background()
	a, err := SealString(ctx, Default(), "community")
	require.NoError(t, err)
	t.Cleanup(func() { _ = a.Destroy() })
	b, err := SealString(ctx, Default(), "passphrase")
	require.NoError(t, err)
	t.Cleanup(func() { _ = b.Destroy() })
	secrets := []*Secret{a, nil, b}

	// Room for exactly one caller's opens at a time.
	budget, err := NewLockedBudget(bounded(LimitFor(a.LockedCost() + b.LockedCost())))
	require.NoError(t, err)

	const callers = 32
	var inFlight, maxInFlight, done atomic.Int32
	var wg sync.WaitGroup
	errs := make(chan error, callers)
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- UseAll(ctx, budget, secrets, func(p [][]byte) error {
				n := inFlight.Add(1)
				defer inFlight.Add(-1)
				for {
					m := maxInFlight.Load()
					if n <= m || maxInFlight.CompareAndSwap(m, n) {
						break
					}
				}
				if string(p[0]) != "community" || p[1] != nil || string(p[2]) != "passphrase" {
					return errors.New("wrong plaintexts")
				}
				time.Sleep(time.Millisecond)
				done.Add(1)
				return nil
			})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	assert.Equal(t, int32(callers), done.Load())
	assert.Equal(t, int32(1), maxInFlight.Load())
}

func TestUseAllReleasesTheBudgetWhenALockFails(t *testing.T) {
	ctx := context.Background()
	a, err := SealString(ctx, Default(), "first")
	require.NoError(t, err)
	t.Cleanup(func() { _ = a.Destroy() })
	b, err := SealString(ctx, Default(), "second")
	require.NoError(t, err)
	t.Cleanup(func() { _ = b.Destroy() })

	budget, err := NewLockedBudget(bounded(LimitFor(a.LockedCost() + b.LockedCost())))
	require.NoError(t, err)

	// The kernel refuses to lock the second secret's pages.
	real := openSealed
	t.Cleanup(func() { openSealed = real })
	openSealed = func(ctx context.Context, s enclave.Sealed) (enclave.ByteAccess, error) {
		if s == b.sealed {
			return nil, enclave.ErrLockedMemoryUnavailable
		}
		return real(ctx, s)
	}

	called := false
	err = UseAll(ctx, budget, []*Secret{a, b}, func([][]byte) error { called = true; return nil })
	var openErr *OpenError
	require.ErrorAs(t, err, &openErr)
	assert.ErrorIs(t, err, enclave.ErrLockedMemoryUnavailable)
	assert.False(t, called)

	capacity, _ := budget.Capacity()
	waitCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	release, err := budget.Acquire(waitCtx, capacity)
	require.NoError(t, err, "the failed open gave its budget back")
	release()

	openSealed = real
	require.NoError(t, UseAll(ctx, budget, []*Secret{a, b}, func(p [][]byte) error {
		assert.Equal(t, "first", string(p[0]))
		assert.Equal(t, "second", string(p[1]))
		return nil
	}))
}

func TestProcessBudgetReportsADiscoveryFailure(t *testing.T) {
	_, err := processBudgetFrom(func() (enclave.LockedMemoryLimit, error) {
		return enclave.LockedMemoryLimit{}, enclave.ErrLimitDiscoveryFailed
	})
	assert.ErrorIs(t, err, enclave.ErrLimitDiscoveryFailed)

	b, err := processBudgetFrom(func() (enclave.LockedMemoryLimit, error) {
		return bounded(64 << 10), nil
	})
	require.NoError(t, err)
	capacity, isBounded := b.Capacity()
	assert.True(t, isBounded)
	assert.Equal(t, int64(64<<10)-sealerKeyPages*enclave.LockedSize(1), capacity)
}
