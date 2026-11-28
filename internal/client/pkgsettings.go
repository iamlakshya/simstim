package client

import (
	"maps"

	"github.com/iamlakshya/simstim/internal/api"
)

// CheckPackageManagers validates sandbox.package_managers and returns a
// UserError with code "package_level_invalid" when invalid (spec 7.10).
func CheckPackageManagers(raw string) (string, error) {
	lvl, err := api.ParsePackageManagers(raw)
	if err != nil {
		return "", &UserError{
			Msg:  api.PackageLevelMessage(raw),
			Hint: api.PackageLevelHint,
			Code: "package_level_invalid",
			Kind: KindUsage,
		}
	}
	return lvl, nil
}

// CheckMinReleaseAge validates network.min_release_age and returns a
// UserError with code "release_age_invalid" when invalid (spec 7.10).
func CheckMinReleaseAge(raw string) (days int, canonical string, err error) {
	days, canonical, err = api.ParseMinReleaseAge(raw)
	if err != nil {
		return 0, "", &UserError{
			Msg:  api.ReleaseAgeMessage(raw),
			Hint: api.ReleaseAgeHint,
			Code: "release_age_invalid",
			Kind: KindUsage,
		}
	}
	return days, canonical, nil
}

// ValidatePackageSettings checks s.Sandbox.PackageManagers and
// s.Network.MinReleaseAge, returning a spec 7.10 UserError on invalid values.
func ValidatePackageSettings(s Settings) error {
	if _, err := CheckPackageManagers(s.Sandbox.PackageManagers); err != nil {
		return err
	}
	if _, _, err := CheckMinReleaseAge(s.Network.MinReleaseAge); err != nil {
		return err
	}
	return nil
}

// withPackageSettings fills and validates a session's package manager settings
// from config.toml when the session is walled or explicitly specifies them.
func withPackageSettings(a NewArgs) (NewArgs, error) {
	walled := a.Wall != "" && a.Wall != "none"
	if !walled && a.PackageManagers == "" && a.MinReleaseAge == "" && len(a.SandboxEnv) == 0 {
		return a, nil
	}
	s := LoadSettings()
	if a.PackageManagers == "" {
		a.PackageManagers = s.Sandbox.PackageManagers
	}
	lvl, err := CheckPackageManagers(a.PackageManagers)
	if err != nil {
		return a, err
	}
	a.PackageManagers = lvl

	if a.MinReleaseAge == "" {
		a.MinReleaseAge = s.Network.MinReleaseAge
	}
	_, age, err := CheckMinReleaseAge(a.MinReleaseAge)
	if err != nil {
		return a, err
	}
	a.MinReleaseAge = age

	if len(s.Sandbox.Env) > 0 {
		merged := maps.Clone(s.Sandbox.Env)
		maps.Copy(merged, a.SandboxEnv)
		a.SandboxEnv = merged
	}
	return a, nil
}
