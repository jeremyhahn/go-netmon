package poller

import (
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/jeremyhahn/go-netmon/internal/snmp"
)

// Unreachable-device policy: abandon a cycle after silenceThreshold unanswered operations with no answer at all, and back off by doubling up to maxBackoff; reset on any answer.
const (
	// silenceThreshold is how many operations in a row may go unanswered, with nothing answered, before the rest of the cycle is skipped.
	silenceThreshold = 3

	// maxBackoff caps how long an unreachable device is left alone.
	maxBackoff = 15 * time.Minute

	// maxBackoffShift bounds the doubling so a device that has been down for
	// weeks cannot shift a duration past the width of an int64.
	maxBackoffShift = 32
)

// sysUpTimeOID is sysUpTime.0: the object every SNMP agent implements, and so
// the cheapest possible question to ask one. See worker.abandon.
const sysUpTimeOID = "1.3.6.1.2.1.1.3.0"

// deviceUnreachable is 1 while a device answers nothing; published at 0 on registration, deleted on removal.
var deviceUnreachable = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "gonetmon_device_unreachable",
		Help: "1 while a device answers no SNMP at all and is being retried on a backoff, 0 while it answers.",
	},
	[]string{"device"},
)

func init() {
	prometheus.MustRegister(deviceUnreachable)
}

// cycle tracks whether the device answered anything this poll; refusals and partial walks count as answers.
type cycle struct {
	answered int
	silent   int  // consecutive unanswered operations
	probed   bool // the device has already been asked directly whether it is there
}

// record files the outcome of one SNMP operation — a chunk of scalar GETs, or
// one table walk — and reports whether the rest of the cycle is now pointless.
func (c *cycle) record(err error) bool {
	if err == nil || !snmp.Unanswered(err) {
		c.answered++
		c.silent = 0
		return false
	}

	c.silent++
	return c.abandoned()
}

// abandoned reports that the device has been silent long enough for the
// remaining operations to be a waste of the poll interval.
func (c *cycle) abandoned() bool {
	return c.answered == 0 && c.silent >= silenceThreshold
}

// unreachable reports a cycle the device answered no part of.
func (c *cycle) unreachable() bool {
	return c.answered == 0 && c.silent > 0
}

// reachState is the device state and next retry, replaced whole so status reads are consistent.
type reachState struct {
	// consecutive is how many cycles in a row the device answered nothing.
	consecutive uint32

	// delay is the wait this outcome earned and nextPoll when it expires, fixed when recorded.
	delay    time.Duration
	nextPoll time.Time
}

// reachability is a worker's lock-free atomic snapshot of whether its device answers and when to retry; one writer.
type reachability struct {
	device   string
	interval time.Duration
	ceiling  time.Duration

	state atomic.Pointer[reachState]
}

// newReachability starts a device reachable and publishes its gauge; nextPoll stays zero until the first cycle.
func newReachability(device string, interval time.Duration) *reachability {
	deviceUnreachable.WithLabelValues(device).Set(0)

	r := &reachability{
		device:   device,
		interval: interval,
		ceiling:  maxBackoff,
	}
	r.state.Store(&reachState{delay: interval})
	return r
}

// record files one cycle's outcome and returns the wait it earned; any answer clears backoff; the gauge is set every cycle.
func (r *reachability) record(answered bool) time.Duration {
	consecutive := uint32(0)
	if !answered {
		consecutive = r.state.Load().consecutive + 1
	}

	delay := backoff(r.interval, r.ceiling, consecutive)
	r.state.Store(&reachState{
		consecutive: consecutive,
		delay:       delay,
		nextPoll:    time.Now().Add(delay),
	})

	if consecutive > 0 {
		deviceUnreachable.WithLabelValues(r.device).Set(1)
	} else {
		deviceUnreachable.WithLabelValues(r.device).Set(0)
	}
	return delay
}

// snapshot returns one consistent view of the device's state. A status read
// takes this rather than calling the accessors below in turn, so that the row
// it reports is a state the device was actually in.
func (r *reachability) snapshot() reachState { return *r.state.Load() }

// unreachable reports whether the device answered nothing in its last cycle.
func (r *reachability) unreachable() bool { return r.state.Load().consecutive > 0 }

// failures reports how many cycles in a row the device has answered nothing.
func (r *reachability) failures() int { return int(r.state.Load().consecutive) }

// due reports when the next poll is scheduled, or the zero time before the
// first cycle has been recorded.
func (r *reachability) due() time.Time { return r.state.Load().nextPoll }

// delay reports the wait the last recorded cycle earned, which is the device's
// own interval until one has been.
func (r *reachability) delay() time.Duration { return r.state.Load().delay }

// forget drops a device's published reachability, so a removed device leaves no
// series behind.
func (r *reachability) forget() {
	deviceUnreachable.DeleteLabelValues(r.device)
}

// backoff returns the wait after n silent cycles: the poll interval doubled n times, capped at ceiling but never below the interval.
func backoff(interval, ceiling time.Duration, n uint32) time.Duration {
	switch {
	case n == 0:
		return interval
	case interval >= ceiling:
		return interval
	case n > maxBackoffShift:
		return ceiling
	}

	delay := interval << (n - 1)
	if delay <= 0 || delay > ceiling {
		// delay <= 0 is the shift having run off the end of an int64.
		return ceiling
	}
	return delay
}
