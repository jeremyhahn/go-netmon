package metrics

import (
	"sort"

	"github.com/prometheus/prometheus/prompb"
)

// maxRequestBytes bounds one remote-write request's MARSHALED protobuf, before
// snappy.
//
// Counting samples is not enough on its own, because a sample is not a fixed
// size. A reading of a counter is a handful of short labels; an entity-sensor
// sample carries entPhysicalName, and an ifAlias is whatever somebody typed
// into the box. A fixed count therefore produces requests of wildly different
// sizes, and nothing in the path noticed.
//
// The bound is on the marshaled size rather than the compressed body because
// that is the number that can be known before the work is done, and because
// snappy's ratio on this data varies with the labels -- bounding the thing that
// varies less is the more useful bound. Compressed, 4 MiB of this protobuf is a
// few hundred kilobytes.
//
// 4 MiB is chosen to be comfortably inside what receivers accept and small
// enough that one refusal is cheap. That second half is the point: a request
// too large to be accepted is refused at the same size on every retry, so it
// can never drain and the backlog grows behind it (#63).
const maxRequestBytes = 4 << 20

// requestSamples is how many samples one request may carry.
//
// It is batch_size, which is what that setting has always appeared to mean and
// did not: batch_size decided when a flush was TRIGGERED, while Flush then took
// the entire buffer and sent it as one request. Anything that accumulated
// between flushes -- a slow remote write, a burst of devices finishing
// together, a retry backlog -- shipped in a single protobuf, and the backlog
// floor alone is 100,000 samples (#63).
func (e *PrometheusExporter) requestSamples() int {
	if e.batchSize <= 0 {
		return defaultBatchSize
	}
	return e.batchSize
}

// keyedSample is one sample beside the series it belongs to, so the series is
// computed once rather than at every comparison.
type keyedSample struct {
	key    string
	sample *Sample
}

// orderForSplitting sorts a batch in place so that every series' readings are
// contiguous and ascending.
//
// This is what makes splitting a batch safe, and it is not optional. Requests
// go to the endpoint in order, and each one advances the endpoint's head for
// the series it carries. A boundary chosen in buffer order can therefore put a
// series' NEWER reading in the earlier request and its OLDER one in the later
// -- and the later request is then refused, taking every other reading in it
// with it. That is precisely the failure #69 is about, reached by the fix for
// #63 rather than by a poller.
//
// Sorted this way, a boundary inside a series puts the older readings in the
// earlier request, which is the order the endpoint requires.
//
// It mutates its argument. The caller needs it to: a partially delivered batch
// is put back as a suffix of this slice, and that is only the undelivered
// remainder if the order the chunks were cut from is the order that survives.
func orderForSplitting(samples []*Sample) {
	if len(samples) < 2 {
		return
	}

	keyed := make([]keyedSample, len(samples))
	for i, sample := range samples {
		keyed[i] = keyedSample{key: seriesKey(sample), sample: sample}
	}

	sort.SliceStable(keyed, func(i, j int) bool {
		if keyed[i].key != keyed[j].key {
			return keyed[i].key < keyed[j].key
		}
		return keyed[i].sample.Timestamp.Before(keyed[j].sample.Timestamp)
	})

	for i := range keyed {
		samples[i] = keyed[i].sample
	}
}

// nextRequest returns the next request to send and how many of samples it
// carries, bounded by BOTH sample count and encoded size.
//
// The size bound is applied by building the candidate and asking the generated
// code what it marshals to, then halving until it fits. Estimating the width of
// a protobuf by hand is how an encoding change silently becomes a bound that
// does not bind; asking is exact, and it costs one Size() call per request in
// the ordinary case where the count bound is the binding one.
//
// A single sample larger than the byte bound is sent anyway. A bound that
// refuses to emit anything is not a bound, it is a stall: the batch would never
// drain and the reading would be retried at the same size forever, which is the
// defect this exists to remove rather than reproduce one sample at a time.
func (e *PrometheusExporter) nextRequest(samples []*Sample) (int, *prompb.WriteRequest) {
	count := e.requestSamples()
	if count > len(samples) {
		count = len(samples)
	}

	for {
		request := &prompb.WriteRequest{Timeseries: e.samplesToTimeSeries(samples[:count])}
		if count == 1 || request.Size() <= maxRequestBytes {
			return count, request
		}
		count /= 2
	}
}
