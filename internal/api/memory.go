package api

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// MinMemoryMax is the smallest memory cap a session may ask for: 256 MiB.
// The session's holder and shadow terminal share the cap with the agent.
const MinMemoryMax = 256 << 20

// MemoryAuto is a memory cap that asks for the host's default, where a size
// would apply otherwise. For a new session, it wins over config.toml, as
// `--memory-max auto` does. For a resume, it wins over the size that the
// session asked for at its start.
const MemoryAuto = -1

// CPUsAuto resets a session's CPU cap to the default on resume.
const CPUsAuto = -1

// MemorySizes lists the forms of a size that ParseSize and ParseMemoryMax take,
// for hints.
const MemorySizes = "512M, 4G, 4Gi, 4GiB, 4GB or 1.5G"

var (
	// memorySize is a size as systemd reads MemoryMax=: a number and a unit,
	// K, M, G or T, in powers of 1024. "Gi", "GiB" and "GB" mean the same as
	// "G".
	memorySize = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?) ?([kmgt])(?:ib?|b)?$`)
	// bareNumber is a size without its unit.
	bareNumber = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+)?$`)
	// errSizeTooSmall marks a size that comes to less than one byte.
	errSizeTooSmall = errors.New("less than 1 byte")
)

// ParseSize reads a size with a unit in powers of 1024, such as "512M", "4Gi",
// "4GiB" or "1.5G". Case does not matter. A size needs its unit, and must come
// to at least one byte.
func ParseSize(v string) (int64, error) {
	s := strings.ToLower(strings.TrimSpace(v))
	m := memorySize.FindStringSubmatch(s)
	if m == nil {
		if bareNumber.MatchString(s) {
			return 0, fmt.Errorf("%q has no unit", v)
		}
		return 0, fmt.Errorf("%q is not a size", v)
	}
	n, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, fmt.Errorf("%q is not a size", v)
	}
	shift := 10 * (strings.Index("kmgt", m[2]) + 1)
	bytes := n * float64(int64(1)<<shift)
	switch {
	case bytes >= 1<<62:
		return 0, fmt.Errorf("%q is too big", v)
	case bytes < 1:
		return 0, fmt.Errorf("%q is %w", v, errSizeTooSmall)
	}
	return int64(bytes), nil
}

// ParseMemoryMax reads a session's memory cap: "auto" (or nothing) for the
// host's default, which is 0, or a size such as "512M", "4Gi" or "1.5GiB".
// Case does not matter. A size needs its unit, and must be MinMemoryMax or
// more. "max" and "infinity" mean no limit to systemd. They are refused
// here, since auto already lets a session use all that the host gives its
// sessions (decision 0024).
func ParseMemoryMax(v string) (int64, error) {
	s := strings.ToLower(strings.TrimSpace(v))
	switch s {
	case "", "auto":
		return 0, nil
	case "max", "infinity", "inf", "unlimited", "none":
		return 0, fmt.Errorf("%q is not a size: auto already lets a session use all the memory that the host gives its sessions", v)
	}
	bytes, err := ParseSize(v)
	if err != nil {
		if errors.Is(err, errSizeTooSmall) {
			return 0, fmt.Errorf("%q is below the smallest cap, 256M", v)
		}
		return 0, err
	}
	if bytes < MinMemoryMax {
		return 0, fmt.Errorf("%q is below the smallest cap, 256M", v)
	}
	return bytes, nil
}

// ParseByteSize parses a positive byte size with a unit (K, M, G or T in powers
// of 1024, such as "64M", "512M", "10G"). Empty string returns 0. minBytes
// enforces a lower bound when > 0.
func ParseByteSize(v string, minBytes int64) (int64, error) {
	if strings.TrimSpace(v) == "" {
		return 0, nil
	}
	bytes, err := ParseSize(v)
	if err != nil {
		if errors.Is(err, errSizeTooSmall) {
			return 0, fmt.Errorf("%q is below the smallest allowed size", v)
		}
		return 0, err
	}
	if minBytes > 0 && bytes < minBytes {
		return 0, fmt.Errorf("%q is below the smallest allowed size", v)
	}
	return bytes, nil
}
