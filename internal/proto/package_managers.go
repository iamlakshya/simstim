package proto

import (
	"fmt"
	"strconv"
	"time"
)

// Built-in package manager policy defaults apply only to a known walled start.
const (
	DefaultPackageManagerMode = "guarded"
	DefaultPackageReleaseAge  = "7d"
)

// PackageManagersV1 is one private cold-start settings plan. StartedAt is the
// supervisor's fixed UTC start time, not a fresh guest clock or saved policy.
// It carries no caller path, environment entries, account or permissions.
type PackageManagersV1 struct {
	Version       int    `json:"v"`
	Mode          string `json:"mode"`
	MinReleaseAge string `json:"min_release_age"`
	StartedAt     string `json:"started_at"`
}

// ClonePackageManagersV1 owns the optional start plan; strings are immutable.
func ClonePackageManagersV1(input *PackageManagersV1) *PackageManagersV1 {
	if input == nil {
		return nil
	}
	output := *input
	return &output
}

// ValidatePackageManagerMode checks the canonical existing policy enum.
func ValidatePackageManagerMode(mode string) error {
	switch mode {
	case "guarded", "strict", "off":
		return nil
	default:
		return &Error{Code: "package_level_invalid", Message: fmt.Sprintf("sandbox.package_managers is %q, and it must be guarded, strict or off", mode), Hint: "Set sandbox.package_managers = \"guarded\" in config.toml."}
	}
}

// ParsePackageReleaseAge parses the one whole-day policy grammar, 0d to 365d.
// Empty is invalid when explicitly configured; the producer selects 7d only
// for an absent original setting. No duration, rounding or clamping is used.
func ParsePackageReleaseAge(value string) (int, error) {
	invalid := func() (int, error) {
		return 0, &Error{Code: "release_age_invalid", Message: fmt.Sprintf("network.min_release_age is %q, and it must be a whole number of days from 0d to 365d", value), Hint: "Set network.min_release_age = \"7d\" in config.toml."}
	}
	if len(value) < 2 || value[len(value)-1] != 'd' {
		return invalid()
	}
	for _, digit := range value[:len(value)-1] {
		if digit < '0' || digit > '9' {
			return invalid()
		}
	}
	days, err := strconv.Atoi(value[:len(value)-1])
	if err != nil || days < 0 || days > 365 {
		return invalid()
	}
	return days, nil
}

// ValidatePackageManagersV1 checks a complete known private start plan.
// Nil belongs only to legacy/none callers; supported walled starts supply V1.
func ValidatePackageManagersV1(input *PackageManagersV1) error {
	if input == nil {
		return nil
	}
	if input.Version != 1 {
		return &Error{Code: "wall_setup", Message: "the package manager settings version is unsupported", Hint: "Start the sandbox with matching host and guest helpers."}
	}
	if err := ValidatePackageManagerMode(input.Mode); err != nil {
		return err
	}
	if _, err := ParsePackageReleaseAge(input.MinReleaseAge); err != nil {
		return err
	}
	started, err := time.Parse(time.RFC3339Nano, input.StartedAt)
	if err != nil || started.IsZero() || started.UTC().Format(time.RFC3339Nano) != input.StartedAt {
		return &Error{Code: "wall_setup", Message: "the package manager settings have no exact UTC start time", Hint: "Start a fresh sandbox with matching host and guest helpers."}
	}
	return nil
}
