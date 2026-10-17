package api

import (
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

// MemorySizes lists the forms of a size that ParseMemoryMax takes, for hints.
const MemorySizes = "512M, 4G, 4Gi, 4GiB, 4GB or 1.5G"

var (
	// memorySize is a size as systemd reads MemoryMax=: a number and a unit,
	// K, M, G or T, in powers of 1024. "Gi", "GiB" and "GB" mean the same as
	// "G".
	memorySize = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?) ?([kmgt])(?:ib?|b)?$`)
	// bareNumber is a size without its unit.
	bareNumber = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+)?$`)
)

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
	case bytes < MinMemoryMax:
		return 0, fmt.Errorf("%q is below the smallest cap, 256M", v)
	}
	return int64(bytes), nil
}
