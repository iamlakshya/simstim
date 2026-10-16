package diag

import (
	"context"
	"math"
	"runtime/metrics"
	"time"
)

// MetricsEvery is how often the runtime category samples Go's metrics.
const MetricsEvery = time.Second

// The three runtime metrics that the runtime category records each second
// (decision 0025).
const (
	metricHeap  = "/memory/classes/heap/objects:bytes"
	metricGC    = "/sched/pauses/total/gc:seconds"
	metricSched = "/sched/latencies:seconds"
)

// sampleMetrics writes a metrics record every MetricsEvery until ctx ends.
func sampleMetrics(ctx context.Context, f *File) {
	s := []metrics.Sample{{Name: metricHeap}, {Name: metricGC}, {Name: metricSched}}
	var gc, sched []uint64 // the histograms' counts at the last sample
	t := time.NewTicker(MetricsEvery)
	defer t.Stop()
	for first := true; ; first = false {
		metrics.Read(s)
		m := &Metrics{}
		if s[0].Value.Kind() == metrics.KindUint64 {
			m.HeapObjects = int64(min(s[0].Value.Uint64(), math.MaxInt64))
		}
		var p99 int64
		m.GCPauses, m.GCPauseMax, _ = histogramDelta(s[1].Value, &gc)
		_, m.SchedMax, p99 = histogramDelta(s[2].Value, &sched)
		m.SchedP99 = p99
		if !first {
			f.Add(Record{T: Now(), Cat: CatRuntime, Ev: "metrics", RT: m})
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// histogramDelta compares a histogram with its counts at the last sample,
// in prev, and keeps the new counts there. It returns how many values came
// in since, the largest of them and their 99th percentile, in ns. Each is
// the upper bound of its bucket, or the lower bound of the last bucket.
func histogramDelta(v metrics.Value, prev *[]uint64) (count, largest, p99 int64) {
	if v.Kind() != metrics.KindFloat64Histogram {
		return 0, 0, 0
	}
	return histogramDeltaOf(v.Float64Histogram(), prev)
}

func histogramDeltaOf(h *metrics.Float64Histogram, prev *[]uint64) (count, largest, p99 int64) {
	delta := make([]uint64, len(h.Counts))
	var total uint64
	for i, c := range h.Counts {
		if i < len(*prev) && c >= (*prev)[i] {
			c -= (*prev)[i]
		}
		delta[i] = c
		total += c
	}
	*prev = append((*prev)[:0], h.Counts...)
	if total == 0 {
		return 0, 0, 0
	}
	bound := func(i int) int64 {
		b := h.Buckets[i+1]
		if math.IsInf(b, 1) {
			b = h.Buckets[i]
		}
		return int64(b * 1e9)
	}
	var seen uint64
	want := (total*99 + 99) / 100
	for i, c := range delta {
		if c == 0 {
			continue
		}
		seen += c
		if p99 == 0 && seen >= want {
			p99 = bound(i)
		}
		largest = bound(i)
	}
	return int64(min(total, math.MaxInt64)), largest, p99
}
