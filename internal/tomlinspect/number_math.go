// Copyright 2009 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// licence that can be found in GO-LICENSE.

// Adapted from Go 1.26.11 src/internal/strconv. See GO-LICENSE.
package tomlinspect

// A uint128 is a 128-bit uint.
// The fields are exported to make them visible to package strconv_test.
type uint128 struct {
	Hi uint64
	Lo uint64
}

// pow10 returns the 128-bit mantissa and binary exponent of 10**e.
// That is, 10^e = mant/2^128 * 2**exp.
// If e is out of range, pow10 returns ok=false.
func pow10(e int) (mant uint128, exp int, ok bool) {
	if e < -348 || e > 347 {
		return
	}
	if e < 0 {
		return pow10Negative[e+348], 1 + mulLog2_10(e), true
	}
	return pow10Positive[e], 1 + mulLog2_10(e), true
}

// mulLog2_10 returns math.Floor(x * log(10)/log(2)) for an integer x in
// the range -500 <= x && x <= +500.
//
// The range restriction lets us work in faster integer arithmetic instead of
// slower floating point arithmetic. Correctness is verified by unit tests.
func mulLog2_10(x int) int {
	// log(10)/log(2) ≈ 3.32192809489 ≈ 108853 / 2^15
	return (x * 108853) >> 15
}
