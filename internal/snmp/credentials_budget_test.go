package snmp

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	enclave "github.com/jeremyhahn/go-enclave"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-netmon/internal/config"
	"github.com/jeremyhahn/go-netmon/internal/credential"
)

// withLockedBudget makes every seal and open in this test wait on a budget
// sized from an RLIMIT_MEMLOCK of limit bytes. Tests that call it must not
// run in parallel.
func withLockedBudget(t *testing.T, limit int64) *credential.LockedBudget {
	t.Helper()
	budget, err := credential.NewLockedBudget(enclave.LockedMemoryLimit{Bytes: limit, Bounded: true})
	require.NoError(t, err)
	real := lockedBudget
	t.Cleanup(func() { lockedBudget = real })
	lockedBudget = func() (*credential.LockedBudget, error) { return budget, nil }
	return budget
}

func v3SecretDevice() *config.Device {
	return &config.Device{
		Name: "r1", SNMPVersion: "3", Community: "c0mm",
		Username: "u", AuthPassword: "authpass1", PrivPassword: "privpass1",
	}
}

func v3LockedCost() int64 {
	return credential.LockedCost(len("c0mm")) + credential.LockedCost(len("authpass1")) + credential.LockedCost(len("privpass1"))
}

func TestConcurrentUseWithATinyBudgetWaitsAndAllFinish(t *testing.T) {
	creds, err := SealCredentials(context.Background(), credential.Default(), v3SecretDevice())
	require.NoError(t, err)
	t.Cleanup(func() { _ = creds.Destroy() })
	require.Equal(t, v3LockedCost(), creds.lockedCost())

	// Room for one device's credentials to be open at a time.
	withLockedBudget(t, credential.LimitFor(creds.lockedCost()))

	const workers = 64
	var inFlight, maxInFlight, done atomic.Int32
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- creds.use(context.Background(), func(s openSecrets) error {
				n := inFlight.Add(1)
				defer inFlight.Add(-1)
				for {
					m := maxInFlight.Load()
					if n <= m || maxInFlight.CompareAndSwap(m, n) {
						break
					}
				}
				assert.Equal(t, openSecrets{community: "c0mm", authPassword: "authpass1", privPassword: "privpass1"}, s)
				time.Sleep(time.Millisecond) // the network request
				done.Add(1)
				return nil
			})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err, "a worker waits for budget, it does not fail")
	}
	assert.Equal(t, int32(workers), done.Load())
	assert.Equal(t, int32(1), maxInFlight.Load())
}

func TestSealCredentialsWaitsWhileAnotherWorkerHoldsTheBudget(t *testing.T) {
	budget := withLockedBudget(t, credential.LimitFor(v3LockedCost()))
	capacity, _ := budget.Capacity()
	held, err := budget.Acquire(context.Background(), capacity) // another worker's poll
	require.NoError(t, err)

	type result struct {
		creds *Credentials
		err   error
	}
	got := make(chan result, 1)
	go func() {
		creds, err := SealCredentials(context.Background(), credential.Default(), v3SecretDevice())
		got <- result{creds, err}
	}()
	select {
	case r := <-got:
		t.Fatalf("sealing did not wait for the budget: %v", r.err)
	case <-time.After(20 * time.Millisecond):
	}
	held()
	r := <-got
	require.NoError(t, r.err, "sealing at start does not abort because another worker held pages")
	t.Cleanup(func() { _ = r.creds.Destroy() })
	assert.True(t, r.creds.Alive())
}

func TestSealCredentialsRefusesAnOpenTheLimitCanNeverHold(t *testing.T) {
	// Room for the community alone, not for all three secrets.
	limit := credential.LimitFor(credential.LockedCost(len("c0mm")))
	withLockedBudget(t, limit)

	device := v3SecretDevice()
	_, err := SealCredentials(context.Background(), credential.Default(), device)

	var lmErr *credential.LockedMemoryError
	require.ErrorAs(t, err, &lmErr)
	assert.ErrorIs(t, err, credential.ErrLockedMemoryTooSmall)
	var credErr *CredentialError
	require.ErrorAs(t, err, &credErr)
	assert.Equal(t, "r1", credErr.Device)
	assert.Equal(t, limit, lmErr.Limit)
	assert.Equal(t, credential.LimitFor(v3LockedCost()), lmErr.Need)
	assert.Equal(t, "c0mm", device.Community, "the device is left as it was")
}

func TestRequestWaitingForTheBudgetEndsWithItsContext(t *testing.T) {
	cases := []struct {
		name string
		ctx  func() (context.Context, context.CancelFunc)
		want error
	}{
		{"canceled", func() (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithCancel(context.Background())
			time.AfterFunc(20*time.Millisecond, cancel)
			return ctx, cancel
		}, context.Canceled},
		{"deadline", func() (context.Context, context.CancelFunc) {
			return context.WithTimeout(context.Background(), 20*time.Millisecond)
		}, context.DeadlineExceeded},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			budget := withLockedBudget(t, credential.LimitFor(credential.LockedCost(len("public"))))
			client, err := NewClient(&config.Device{
				Name: "waiting", Address: "127.0.0.1:161", SNMPVersion: "2c", Community: "public",
			}, agentTimeout, 0)
			require.NoError(t, err)
			t.Cleanup(func() { _ = client.Close() })

			// Every other worker's poll holds the whole budget.
			capacity, _ := budget.Capacity()
			held, err := budget.Acquire(context.Background(), capacity)
			require.NoError(t, err)

			ctx, cancel := tc.ctx()
			defer cancel()
			requests := map[string]func() error{
				"GetMultipleContext": func() error {
					_, getErr := client.GetMultipleContext(ctx, []string{sysDescrOID})
					return getErr
				},
				"BulkWalkAllContext": func() error {
					_, walkErr := client.BulkWalkAllContext(ctx, "1.3.6.1.2.1.1")
					return walkErr
				},
			}
			for name, request := range requests {
				start := time.Now()
				reqErr := request()
				assert.Less(t, time.Since(start), 5*time.Second, name+" returns when its context ends")
				assert.ErrorIs(t, reqErr, tc.want, name)
				var credErr *CredentialError
				if assert.ErrorAs(t, reqErr, &credErr, name) {
					assert.Equal(t, "waiting", credErr.Device)
				}
			}

			// The abandoned waits gave back nothing they never held: the
			// budget is still exhausted until its holder lets go.
			probe, cancelProbe := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancelProbe()
			_, err = budget.Acquire(probe, 1)
			require.ErrorIs(t, err, context.DeadlineExceeded)

			held()
			release, err := budget.Acquire(context.Background(), capacity)
			require.NoError(t, err, "and nothing beyond it")
			release()
		})
	}
}
