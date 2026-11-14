// Copyright 2009 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// licence that can be found in GO-LICENSE.

// Adapted from Go 1.26.11 src/internal/strconv. See GO-LICENSE.
package tomlinspect

// decimal power of ten to binary power of two.
var powtab = []int{1, 3, 6, 9, 13, 16, 19, 23, 26}

func (d *decimal) floatBits(flt *floatInfo) (b uint64, overflow bool) {
	var exp int
	var mant uint64

	// Zero is always a special case.
	if d.nd == 0 {
		mant = 0
		exp = flt.bias
		goto out
	}

	// Obvious overflow/underflow.
	// These bounds are for 64-bit floats.
	// Will have to change if we want to support 80-bit floats in the future.
	if d.dp > 310 {
		goto overflow
	}
	if d.dp < -330 {
		// zero
		mant = 0
		exp = flt.bias
		goto out
	}

	// Scale by powers of two until in range [0.5, 1.0)
	exp = 0
	for d.dp > 0 {
		var n int
		if d.dp >= len(powtab) {
			n = 27
		} else {
			n = powtab[d.dp]
		}
		d.Shift(-n)
		exp += n
	}
	for d.dp < 0 || d.dp == 0 && d.d[0] < '5' {
		var n int
		if -d.dp >= len(powtab) {
			n = 27
		} else {
			n = powtab[-d.dp]
		}
		d.Shift(n)
		exp -= n
	}

	// Our range is [0.5,1) but floating point range is [1,2).
	exp--

	// Minimum representable exponent is flt.bias+1.
	// If the exponent is smaller, move it up and
	// adjust d accordingly.
	if exp < flt.bias+1 {
		n := flt.bias + 1 - exp
		d.Shift(-n)
		exp += n
	}

	if exp-flt.bias >= 1<<flt.expbits-1 {
		goto overflow
	}

	// Extract 1+flt.mantbits bits.
	d.Shift(int(1 + flt.mantbits))
	mant = d.RoundedInteger()

	// Rounding might have added a bit; shift down.
	if mant == 2<<flt.mantbits {
		mant >>= 1
		exp++
		if exp-flt.bias >= 1<<flt.expbits-1 {
			goto overflow
		}
	}

	// Denormalized?
	if mant&(1<<flt.mantbits) == 0 {
		exp = flt.bias
	}
	goto out

overflow:
	// ±Inf
	mant = 0
	exp = 1<<flt.expbits - 1 + flt.bias
	overflow = true

out:
	// Assemble bits.
	bits := mant & (uint64(1)<<flt.mantbits - 1)
	bits |= uint64((exp-flt.bias)&(1<<flt.expbits-1)) << flt.mantbits
	if d.neg {
		bits |= 1 << flt.mantbits << flt.expbits
	}
	return bits, overflow
}
