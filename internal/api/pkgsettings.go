package api

import (
	"fmt"
	"strconv"
	"strings"
)

// Package manager guard levels for sandbox.package_managers (isolation spec 7.10).
const (
	PackageLevelGuarded    = "guarded"
	PackageLevelStrict     = "strict"
	PackageLevelOff        = "off"
	DefaultPackageManagers = PackageLevelGuarded

	DefaultMinReleaseAge     = "7d"
	DefaultMinReleaseAgeDays = 7
	MaxMinReleaseAgeDays     = 365

	PackageLevelHint = `Set sandbox.package_managers = "guarded" in config.toml`
	ReleaseAgeHint   = `Set network.min_release_age = "7d" in config.toml`
)

// PackageLevelMessage returns the spec 7.10 error message for an invalid
// sandbox.package_managers value, without a trailing Try: hint.
func PackageLevelMessage(raw string) string {
	return fmt.Sprintf("sandbox.package_managers is %q, and it must be guarded, strict or off.", raw)
}

// ReleaseAgeMessage returns the spec 7.10 error message for an invalid
// network.min_release_age value, without a trailing Try: hint.
func ReleaseAgeMessage(raw string) string {
	return fmt.Sprintf("network.min_release_age is %q, and it must be a whole number of days from 0d to 365d.", raw)
}

// ParsePackageManagers validates sandbox.package_managers and applies the
// built-in default ("guarded") when raw is empty.
func ParsePackageManagers(raw string) (string, error) {
	if raw == "" {
		return DefaultPackageManagers, nil
	}
	switch raw {
	case PackageLevelGuarded, PackageLevelStrict, PackageLevelOff:
		return raw, nil
	default:
		return "", fmt.Errorf("%s Try: %s", PackageLevelMessage(raw), PackageLevelHint)
	}
}

// ParseMinReleaseAge validates network.min_release_age ("0d" to "365d") and
// applies the built-in default ("7d") when raw is empty.
func ParseMinReleaseAge(raw string) (days int, canonical string, err error) {
	if raw == "" {
		return DefaultMinReleaseAgeDays, DefaultMinReleaseAge, nil
	}
	numStr, ok := strings.CutSuffix(raw, "d")
	if !ok || numStr == "" || (len(numStr) > 1 && numStr[0] == '0') {
		return 0, "", fmt.Errorf("%s Try: %s", ReleaseAgeMessage(raw), ReleaseAgeHint)
	}
	for i := range len(numStr) {
		if numStr[i] < '0' || numStr[i] > '9' {
			return 0, "", fmt.Errorf("%s Try: %s", ReleaseAgeMessage(raw), ReleaseAgeHint)
		}
	}
	n, convErr := strconv.Atoi(numStr)
	if convErr != nil || n < 0 || n > MaxMinReleaseAgeDays {
		return 0, "", fmt.Errorf("%s Try: %s", ReleaseAgeMessage(raw), ReleaseAgeHint)
	}
	return n, fmt.Sprintf("%dd", n), nil
}

// FormatPackageSummary formats a session's package manager policy for display,
// such as "guarded, 7 days" or "off" (isolation spec 7.10).
func FormatPackageSummary(level, age string) string {
	lvl, err := ParsePackageManagers(level)
	if err != nil {
		return level
	}
	if lvl == PackageLevelOff {
		return PackageLevelOff
	}
	days, _, err := ParseMinReleaseAge(age)
	if err != nil {
		return lvl
	}
	unit := "days"
	if days == 1 {
		unit = "day"
	}
	return fmt.Sprintf("%s, %d %s", lvl, days, unit)
}
