package metrics

import (
	"sort"

	"github.com/prometheus/prometheus/prompb"
)

// maxRequestBytes bounds one remote-write request's marshaled protobuf, before snappy.
const maxRequestBytes = 4 << 20

// requestSamples is how many samples one request may carry: batch_size.
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

// orderForSplitting sorts batch in place so each series is contiguous and ascending, making request boundaries safe.
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

// nextRequest returns the next request bounded by sample count and marshaled size; a single oversized sample is sent alone.
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
