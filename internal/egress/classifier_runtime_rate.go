package egress

import (
	"math"
	"time"
)

type classifierRateResult uint8

const (
	classifierRateAllowed classifierRateResult = iota
	classifierRateLimited
	classifierRateClock
	classifierRateOverflow
)

type classifierRateBucket struct {
	second int64
	latest time.Time
	count  uint64
}

// classifierRate retains at most 61 seconds of counts, independently of the
// configured rate and the separate active-job limit. A bucket expires only
// 60s after its latest charged call, so every call in the rolling 60s window
// remains counted. This can delay quota release by less than one second; it
// never admits more than the configured rate in a rolling minute.
// The runtime's leaf mutex owns every operation.
type classifierRate struct {
	started bool
	epoch   time.Time
	last    time.Time
	buckets [61]classifierRateBucket
}

func (rate *classifierRate) charge(now time.Time, limit int) classifierRateResult {
	if limit < 0 {
		return classifierRateOverflow
	}
	if rate.started && now.Before(rate.last) {
		return classifierRateClock
	}
	if !rate.started {
		rate.started, rate.epoch = true, now
	}
	rate.last = now
	if limit == 0 {
		return classifierRateLimited
	}
	var total uint64
	for index := range rate.buckets {
		bucket := &rate.buckets[index]
		if bucket.count == 0 {
			continue
		}
		if now.Before(bucket.latest) {
			return classifierRateClock
		}
		if now.Sub(bucket.latest) >= time.Minute {
			*bucket = classifierRateBucket{}
			continue
		}
		if math.MaxUint64-total < bucket.count {
			return classifierRateOverflow
		}
		total += bucket.count
	}
	if total >= uint64(limit) {
		return classifierRateLimited
	}
	if total == 0 && now.Sub(rate.epoch) >= time.Minute {
		rate.epoch = now
	}
	elapsed := now.Sub(rate.epoch)
	if elapsed < 0 {
		return classifierRateClock
	}
	second := int64(elapsed / time.Second)
	bucket := &rate.buckets[second%int64(len(rate.buckets))]
	if bucket.count != 0 && bucket.second != second {
		return classifierRateClock
	}
	if bucket.count == math.MaxUint64 {
		return classifierRateOverflow
	}
	bucket.second, bucket.latest = second, now
	bucket.count++
	return classifierRateAllowed
}
