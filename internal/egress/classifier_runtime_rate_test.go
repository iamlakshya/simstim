package egress

import (
	"math"
	"testing"
	"time"
)

func TestClassifierRateBoundsBurstWithoutCappingConfiguredRateAtFlowCapacity(t *testing.T) {
	var rate classifierRate
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for index := range 257 {
		actual := rate.charge(now, 256)
		if index < 256 && actual != classifierRateAllowed || index == 256 && actual != classifierRateLimited {
			t.Fatal("configured rate was capped or exceeded")
		}
	}
	if rate.charge(now.Add(time.Minute-time.Nanosecond), 256) != classifierRateLimited {
		t.Fatal("burst quota was released before a full minute")
	}
	if rate.charge(now.Add(time.Minute), 256) != classifierRateAllowed {
		t.Fatal("idle minute did not release quota")
	}
}

func TestClassifierRateBucketLatestCallConservativelyBoundsTheWholeWindow(t *testing.T) {
	var rate classifierRate
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if rate.charge(now, 2) != classifierRateAllowed || rate.charge(now.Add(900*time.Millisecond), 2) != classifierRateAllowed {
		t.Fatal("initial quota unavailable")
	}
	if rate.charge(now.Add(time.Minute), 2) != classifierRateLimited {
		t.Fatal("bucket released its later call too early")
	}
	if rate.charge(now.Add(time.Minute+900*time.Millisecond), 2) != classifierRateAllowed {
		t.Fatal("bucket retained quota past its latest call's minute")
	}
}

func TestClassifierRateZeroRollbackOverflowAndLongIdleFailClosed(t *testing.T) {
	var rate classifierRate
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if rate.charge(now, 0) != classifierRateLimited {
		t.Fatal("explicit zero rate made a call")
	}
	if rate.charge(now.Add(-time.Nanosecond), 20) != classifierRateClock {
		t.Fatal("clock rollback reset quota")
	}
	if rate.charge(now.Add(time.Hour), 20) != classifierRateAllowed {
		t.Fatal("long idle failed to restore quota")
	}
	rate.buckets[1] = classifierRateBucket{latest: now.Add(time.Hour), count: math.MaxUint64}
	rate.buckets[2] = classifierRateBucket{latest: now.Add(time.Hour), count: 1}
	if rate.charge(now.Add(time.Hour), 20) != classifierRateOverflow {
		t.Fatal("corrupt count arithmetic wrapped into permission")
	}
}

func TestClassifierRateEveryRollingWindowContainsAtMostConfiguredCalls(t *testing.T) {
	var rate classifierRate
	epoch := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var accepted []time.Time
	for step := range 2000 {
		now := epoch.Add(time.Duration(step) * 137 * time.Millisecond)
		if rate.charge(now, 20) == classifierRateAllowed {
			accepted = append(accepted, now)
		}
		count := 0
		for _, at := range accepted {
			if at.After(now.Add(-time.Minute)) && !at.After(now) {
				count++
			}
		}
		if count > 20 {
			t.Fatal("accepted history exceeded configured rolling-minute bound")
		}
	}
}
