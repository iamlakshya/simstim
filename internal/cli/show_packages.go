package cli

import (
	"fmt"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/proto"
)

// showPackages describes only the settings installed at this running start.
// Current policy can change before the guest's package settings change.
func showPackages(s client.Session) string {
	plan := s.PackageManagers
	if plan == nil && (s.Wall == nil || s.Wall.Level == "none") {
		return ""
	}
	if !s.Alive || plan == nil || proto.ValidatePackageManagersV1(plan) != nil {
		return "unknown (no live start settings)"
	}
	days, err := proto.ParsePackageReleaseAge(plan.MinReleaseAge)
	if err != nil {
		return "unknown (no live start settings)"
	}
	age := fmt.Sprintf("%d days", days)
	if days == 1 {
		age = "1 day"
	} else if days == 0 {
		age = "no minimum age"
	}
	return plan.Mode + ", " + age + " (from current start)"
}
