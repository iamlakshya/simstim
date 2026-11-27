// Copyright 2009 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// licence that can be found in GO-LICENSE.

// Adapted from Go 1.26.11 src/internal/strconv. See GO-LICENSE.
package tomlinspect

func (b *decimal) set(s numberSpan) (ok bool) {
	i := 0
	b.neg = false
	b.trunc = false

	// optional sign
	if i >= s.len() {
		return
	}
	switch s.at(i) {
	case '+':
		i++
	case '-':
		i++
		b.neg = true
	}

	// digits
	sawdot := false
	sawdigits := false
	for ; i < s.len(); i++ {
		switch {
		case s.at(i) == '_':
			// readFloat already checked underscores
			continue
		case s.at(i) == '.':
			if sawdot {
				return
			}
			sawdot = true
			b.dp = b.nd
			continue

		case '0' <= s.at(i) && s.at(i) <= '9':
			sawdigits = true
			if s.at(i) == '0' && b.nd == 0 { // ignore leading zeros
				b.dp--
				continue
			}
			if b.nd < len(b.d) {
				b.d[b.nd] = s.at(i)
				b.nd++
			} else if s.at(i) != '0' {
				b.trunc = true
			}
			continue
		}
		break
	}
	if !sawdigits {
		return
	}
	if !sawdot {
		b.dp = b.nd
	}

	// optional exponent moves decimal point.
	// if we read a very large, very long number,
	// just be sure to move the decimal point by
	// a lot (say, 100000).  it doesn't matter if it's
	// not the exact number.
	if i < s.len() && lower(s.at(i)) == 'e' {
		i++
		if i >= s.len() {
			return
		}
		esign := 1
		switch s.at(i) {
		case '+':
			i++
		case '-':
			i++
			esign = -1
		}
		if i >= s.len() || s.at(i) < '0' || s.at(i) > '9' {
			return
		}
		e := 0
		for ; i < s.len() && ('0' <= s.at(i) && s.at(i) <= '9' || s.at(i) == '_'); i++ {
			if s.at(i) == '_' {
				// readFloat already checked underscores
				continue
			}
			if e < 10000 {
				e = e*10 + int(s.at(i)) - '0'
			}
		}
		b.dp += e * esign
	}

	if i != s.len() {
		return
	}

	ok = true
	return
}

// readFloat reads a decimal or hexadecimal mantissa and exponent from a float
// string representation in s; the number may be followed by other characters.
// readFloat reports the number of bytes consumed (i), and whether the number
// is valid (ok).
func readFloat(s numberSpan) (mantissa uint64, exp int, neg, trunc, hex bool, i int, ok bool) {
	underscores := false

	// optional sign
	if i >= s.len() {
		return
	}
	switch s.at(i) {
	case '+':
		i++
	case '-':
		i++
		neg = true
	}

	// digits
	base := uint64(10)
	maxMantDigits := 19 // 10^19 fits in uint64
	expChar := byte('e')
	if i+2 < s.len() && s.at(i) == '0' && lower(s.at(i+1)) == 'x' {
		base = 16
		maxMantDigits = 16 // 16^16 fits in uint64
		i += 2
		expChar = 'p'
		hex = true
	}
	sawdot := false
	sawdigits := false
	nd := 0
	ndMant := 0
	dp := 0
loop:
	for ; i < s.len(); i++ {
		switch c := s.at(i); true {
		case c == '_':
			underscores = true
			continue

		case c == '.':
			if sawdot {
				break loop
			}
			sawdot = true
			dp = nd
			continue

		case '0' <= c && c <= '9':
			sawdigits = true
			if c == '0' && nd == 0 { // ignore leading zeros
				dp--
				continue
			}
			nd++
			if ndMant < maxMantDigits {
				mantissa *= base
				mantissa += uint64(c - '0')
				ndMant++
			} else if c != '0' {
				trunc = true
			}
			continue

		case base == 16 && 'a' <= lower(c) && lower(c) <= 'f':
			sawdigits = true
			nd++
			if ndMant < maxMantDigits {
				mantissa *= 16
				mantissa += uint64(lower(c) - 'a' + 10)
				ndMant++
			} else {
				trunc = true
			}
			continue
		}
		break
	}
	if !sawdigits {
		return
	}
	if !sawdot {
		dp = nd
	}

	if base == 16 {
		dp *= 4
		ndMant *= 4
	}

	// optional exponent moves decimal point.
	// if we read a very large, very long number,
	// just be sure to move the decimal point by
	// a lot (say, 100000).  it doesn't matter if it's
	// not the exact number.
	if i < s.len() && lower(s.at(i)) == expChar {
		i++
		if i >= s.len() {
			return
		}
		esign := 1
		switch s.at(i) {
		case '+':
			i++
		case '-':
			i++
			esign = -1
		}
		if i >= s.len() || s.at(i) < '0' || s.at(i) > '9' {
			return
		}
		e := 0
		for ; i < s.len() && ('0' <= s.at(i) && s.at(i) <= '9' || s.at(i) == '_'); i++ {
			if s.at(i) == '_' {
				underscores = true
				continue
			}
			if e < 10000 {
				e = e*10 + int(s.at(i)) - '0'
			}
		}
		dp += e * esign
	} else if base == 16 {
		// Must have exponent.
		return
	}

	if mantissa != 0 {
		exp = dp - ndMant
	}

	if underscores && !underscoreOK(s.prefix(i)) {
		return
	}

	ok = true
	return
}
