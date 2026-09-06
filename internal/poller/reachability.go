package poller

import (
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/jeremyhahn/go-netmon/internal/snmp"
)

// The unreachable-device policy.
//
// A device that has stopped answering SNMP costs one full retry budget per
// operation — snmp_timeout × (snmp_retries + 1), 40s with the shipped
// configuration — and a worker used to pay that for every one of the device's
// selected objects, every cycle. Sixty-three objects is forty-two minutes for a
// poll on a sixty-second interval, so the cycle never finished, no result was
// ever produced, and the device reported "last poll never" forever while
// burning a goroutine and a socket.
//
// Two rules fix that, and both are about the device rather than the object:
//
//  1. Abandon the rest of a cycle once the device has left silenceThreshold
//     operations in a row unanswered and answered nothing at all so far. The
//     remaining objects live on the same agent; it is not going to answer them.
//
//  2. Back off between cycles, doubling from the device's own poll interval up
//     to maxBackoff, and drop straight back to the interval the moment the
//     device answers anything.
//
// What is deliberately *not* here is any per-object judgement. A device that
// answers some objects and errors on others — a PDU with half its sensor table
// unimplemented, say — is healthy, and it stays on its normal interval and
// keeps every object it does answer. See cycle.
const (
	// silenceThreshold is how many operations in a row a device may leave
	// unanswered, having answered nothing else in the cycle, before the worker
	// stops asking for the rest of it.
	//
	// Three is one packet-loss burst too many. Each operation is already a
	// whole retry sequence, so this is snmp_retries+1 attempts three times
	// over — twelve unanswered requests spread across two minutes on the
	// shipped configuration — with not one reply of any kind in between. Two
	// would abandon a cycle sooner and cost less; three is the margin for a
	// device whose agent is briefly wedged rather than gone.
	silenceThreshold = 3

	// maxBackoff caps how long an unreachable device is left alone. Long
	// enough that a dead device costs almost nothing, short enough that a
	// device fixed by hand is polling again before the operator has finished
	// writing the ticket.
	maxBackoff = 15 * time.Minute

	// maxBackoffShift bounds the doubling so a device that has been down for
	// weeks cannot shift a duration past the width of an int64.
	maxBackoffShift = 32
)

// sysUpTimeOID is sysUpTime.0: the object every SNMP agent implements, and so
// the cheapest possible question to ask one. See worker.abandon.
const sysUpTimeOID = "1.3.6.1.2.1.1.3.0"

// deviceUnreachable is 1 while a device answers nothing at all, and 0 while it
// answers.
//
// A gauge rather than a counter, unlike gonetmon_mib_module_load_failures_total
// next door, because the question is "is this device down *now*" and that is
// not something a counter can answer without a rate window. How often it has
// happened is already recorded: an abandoned cycle reports a failed poll, so
// gonetmon_polls_total{status="error"} counts them, and before this existed a
// device that never finished a cycle contributed to neither series — which is
// exactly why "unreachable" was indistinguishable from "still starting up".
//
// It is published at 0 when a device is registered so that alerts can be
// written against a series that exists from the start, and deleted when the
// device is removed so a decommissioned box does not stay on a dashboard.
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

// cycle accumulates what one poll learned about whether the device is talking
// at all, which is a different question from whether every object came back.
//
// Anything that arrives from the agent counts as an answer, including a refusal
// and including a table that broke off part-way (see snmp.Unanswered). That is
// what keeps a partially answering device — one that serves its system group
// and times out on a subtree it half implements — on its normal schedule with
// its working objects still collected. Only a device that produced nothing at
// all is judged unreachable.
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

// unreachable reports a cycle the device answered no part of. It is broader
// than abandoned: a device with only one or two objects selected never reaches
// the threshold, but a cycle in which everything attempted went unanswered is
// still a device that is not there.
func (c *cycle) unreachable() bool {
	return c.answered == 0 && c.silent > 0
}

// reachState is everything a status read must see one consistent version of:
// whether the device is answering, and when it will next be tried.
//
// It is replaced whole rather than field by field. Held as two atomics, the
// device's state and its retry were written at two different points in the
// worker's cycle -- silent() in commit, schedule() after poll returned, with
// the export and the result report in between -- so a status request landing
// between them read a device that was unreachable and had no scheduled retry,
// and answered an operator "this device is down, and it will next be tried at
// 0001-01-01T00:00:00Z". That is the pair the whole status endpoint exists to
// report, and it was the one pair that could be read half-written (#33).
type reachState struct {
	// consecutive is how many cycles in a row the device answered nothing.
	consecutive uint32

	// delay is the wait this outcome earned, and nextPoll is when that wait
	// expires. Both are fixed at the instant the outcome is recorded, so the
	// retry the operator is told about is the one the worker actually waits
	// for.
	delay    time.Duration
	nextPoll time.Time
}

// reachability is a worker's memory of whether its device is answering and when
// it should be tried again.
//
// The worker writes it from its polling goroutine while the control API reads
// it to answer a status request, so it is a lock-free atomic snapshot: a status
// lookup must never queue behind a poll, which is the failure this whole change
// exists to stop. There is exactly one writer -- record runs on the worker's
// own goroutine -- so replacing the pointer is all the synchronization needed.
type reachability struct {
	device   string
	interval time.Duration
	ceiling  time.Duration

	state atomic.Pointer[reachState]
}

// newReachability starts a device out reachable, and publishes it as such so
// that an alert on gonetmon_device_unreachable has a series to watch from the
// moment the device is registered.
//
// nextPoll is left as the zero time: nothing has been scheduled until the first
// cycle has been recorded, and due() says so rather than inventing an instant.
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

// record files the outcome of one poll cycle and returns the wait it earned.
//
// A cycle the device took part in clears any backoff immediately: one reply is
// enough to prove the device is back, and making it serve a whole penalty
// interval first would leave a recovered device dark for up to maxBackoff.
//
// The gauge is published on every cycle rather than only on a transition.
// Publishing only on the transition made the series impossible to restore once
// something else had deleted it: a healthy device sits at consecutive == 0
// forever, so the transition never comes round again. Something else does
// delete it -- a worker being replaced forgets the device by NAME as it exits,
// and a device update keeps the name, so the replacement's series can be
// removed out from under it. Setting a gauge is cheap; being absent until the
// device next breaks and recovers is not.
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

// backoff returns the wait after n consecutive cycles in which the device
// answered nothing: its own poll interval while it is answering, then a
// doubling of that interval, never longer than ceiling.
//
// A device polled less often than the ceiling keeps its own interval, because
// backing off to something shorter than the schedule it was given would poll a
// dead device more often than a live one.
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
