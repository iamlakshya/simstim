// Copyright 2009 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// licence that can be found in GO-LICENSE.

// Adapted from Go 1.26.11 src/internal/strconv. See GO-LICENSE.
package tomlinspect

const nativeIntSize = 32 << (^uint(0) >> 63)
const nativeMaxUint64 = 1<<64 - 1

func lower(c byte) byte { return c | ('x' - 'X') }

// ParseUint is like [ParseInt] but for unsigned numbers.
//
// A sign prefix is not permitted.
func parseUint(s numberSpan, base int, bitSize int) (uint64, error) {
	const fnParseUint = "ParseUint"

	if s.len() == 0 {
		return 0, ErrSyntax
	}

	base0 := base == 0

	s0 := s
	switch {
	case 2 <= base && base <= 36:
		// valid base; nothing to do

	case base == 0:
		// Look for octal, hex prefix.
		base = 10
		if s.at(0) == '0' {
			switch {
			case s.len() >= 3 && lower(s.at(1)) == 'b':
				base = 2
				s = s.after(2)
			case s.len() >= 3 && lower(s.at(1)) == 'o':
				base = 8
				s = s.after(2)
			case s.len() >= 3 && lower(s.at(1)) == 'x':
				base = 16
				s = s.after(2)
			default:
				base = 8
				s = s.after(1)
			}
		}

	default:
		panic("unreachable pinned numeric base")
	}

	if bitSize == 0 {
		bitSize = nativeIntSize
	} else if bitSize < 0 || bitSize > 64 {
		panic("unreachable pinned numeric bit size")
	}

	// Cutoff is the smallest number such that cutoff*base > nativeMaxUint64.
	// Use compile-time constants for common cases.
	var cutoff uint64
	switch base {
	case 10:
		cutoff = nativeMaxUint64/10 + 1
	case 16:
		cutoff = nativeMaxUint64/16 + 1
	default:
		cutoff = nativeMaxUint64/uint64(base) + 1
	}

	maxVal := uint64(1)<<uint(bitSize) - 1

	underscores := false
	var n uint64
	for at := 0; at < s.len(); at++ {
		c := s.at(at)
		var d byte
		switch {
		case c == '_' && base0:
			underscores = true
			continue
		case '0' <= c && c <= '9':
			d = c - '0'
		case 'a' <= lower(c) && lower(c) <= 'z':
			d = lower(c) - 'a' + 10
		default:
			return 0, ErrSyntax
		}

		if d >= byte(base) {
			return 0, ErrSyntax
		}

		if n >= cutoff {
			// n*base overflows
			return maxVal, ErrNumberRange
		}
		n *= uint64(base)

		n1 := n + uint64(d)
		if n1 < n || n1 > maxVal {
			// n+d overflows
			return maxVal, ErrNumberRange
		}
		n = n1
	}

	if underscores && !underscoreOK(s0) {
		return 0, ErrSyntax
	}

	return n, nil
}

// ParseInt interprets a string s in the given base (0, 2 to 36) and
// bit size (0 to 64) and returns the corresponding value i.
//
// The string may begin with a leading sign: "+" or "-".
//
// If the base argument is 0, the true base is implied by the string's
// prefix following the sign (if present): 2 for "0b", 8 for "0" or "0o",
// 16 for "0x", and 10 otherwise. Also, for argument base 0 only,
// underscore characters are permitted as defined by the Go syntax for
// [integer literals].
//
// The bitSize argument specifies the integer type
// that the result must fit into. Bit sizes 0, 8, 16, 32, and 64
// correspond to int, int8, int16, int32, and int64.
// If bitSize is below 0 or above 64, an error is returned.
//
// The errors that ParseInt returns have concrete type [*NumError]
// and include err.Num = s. If s is empty or contains invalid
// digits, err.Err = [ErrSyntax] and the returned value is 0;
// if the value corresponding to s cannot be represented by a
// signed integer of the given size, err.Err = [ErrNumberRange] and the
// returned value is the maximum magnitude integer of the
// appropriate bitSize and sign.
//
// [integer literals]: https://go.dev/ref/spec#Integer_literals
func parseInt(s numberSpan, base int, bitSize int) (i int64, err error) {
	const fnParseInt = "ParseInt"

	if s.len() == 0 {
		return 0, ErrSyntax
	}

	// Pick off leading sign.
	neg := false
	switch s.at(0) {
	case '+':
		s = s.after(1)
	case '-':
		s = s.after(1)
		neg = true
	}

	// Convert unsigned and check range.
	var un uint64
	un, err = parseUint(s, base, bitSize)
	if err != nil && err != ErrNumberRange {
		return 0, err
	}

	if bitSize == 0 {
		bitSize = nativeIntSize
	}

	cutoff := uint64(1 << uint(bitSize-1))
	if !neg && un >= cutoff {
		return int64(cutoff - 1), ErrNumberRange
	}
	if neg && un > cutoff {
		return -int64(cutoff), ErrNumberRange
	}
	n := int64(un)
	if neg {
		n = -n
	}
	return n, nil
}

// underscoreOK reports whether the underscores in s are allowed.
// Checking them in this one function lets all the parsers skip over them simply.
// Underscore must appear only between digits or between a base prefix and a digit.
func underscoreOK(s numberSpan) bool {
	// saw tracks the last character (class) we saw:
	// ^ for beginning of number,
	// 0 for a digit or base prefix,
	// _ for an underscore,
	// ! for none of the above.
	saw := '^'
	i := 0

	// Optional sign.
	if s.len() >= 1 && (s.at(0) == '-' || s.at(0) == '+') {
		s = s.after(1)
	}

	// Optional base prefix.
	hex := false
	if s.len() >= 2 && s.at(0) == '0' && (lower(s.at(1)) == 'b' || lower(s.at(1)) == 'o' || lower(s.at(1)) == 'x') {
		i = 2
		saw = '0' // base prefix counts as a digit for "underscore as digit separator"
		hex = lower(s.at(1)) == 'x'
	}

	// Number proper.
	for ; i < s.len(); i++ {
		// Digits are always okay.
		if '0' <= s.at(i) && s.at(i) <= '9' || hex && 'a' <= lower(s.at(i)) && lower(s.at(i)) <= 'f' {
			saw = '0'
			continue
		}
		// Underscore must follow digit.
		if s.at(i) == '_' {
			if saw != '0' {
				return false
			}
			saw = '_'
			continue
		}
		// Underscore must also be followed by digit.
		if saw == '_' {
			return false
		}
		// Saw non-digit, non-underscore.
		saw = '!'
	}
	return saw != '_'
}
