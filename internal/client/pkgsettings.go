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

// CheckModelBudget validates network.model_budget and returns a UserError with
// code "model_budget_invalid" when invalid (spec 17.8).
func CheckModelBudget(tokens int64) error {
	if err := api.CheckModelBudget(tokens); err != nil {
		return &UserError{
			Msg:  api.ModelBudgetMessage(tokens),
			Hint: api.ModelBudgetHint,
			Code: "model_budget_invalid",
			Kind: KindUsage,
		}
	}
	return nil
}

// CheckUploadBudget validates network.upload_budget and returns a UserError
// with code "upload_budget_invalid" when invalid (spec 7.2).
func CheckUploadBudget(raw string) (bytes int64, canonical string, err error) {
	bytes, canonical, err = api.ParseUploadBudget(raw)
	if err != nil {
		return 0, "", &UserError{
			Msg:  api.UploadBudgetMessage(raw),
			Hint: api.UploadBudgetHint,
			Code: "upload_budget_invalid",
			Kind: KindUsage,
		}
	}
	return bytes, canonical, nil
}

// ValidatePackageSettings checks sandbox and network policy defaults in s,
// returning a UserError on invalid values.
func ValidatePackageSettings(s Settings) error {
	if _, err := CheckPackageManagers(s.Sandbox.PackageManagers); err != nil {
		return err
	}
	if _, _, err := CheckMinReleaseAge(s.Network.MinReleaseAge); err != nil {
		return err
	}
	if err := CheckModelBudget(s.Network.ModelBudget); err != nil {
		return err
	}
	if _, _, err := CheckUploadBudget(s.Network.UploadBudget); err != nil {
		return err
	}
	return nil
}

// withPackageSettings fills and validates a session's package manager and
// network budget settings from config.toml when the session is walled or
// explicitly specifies them.
func withPackageSettings(a NewArgs) (NewArgs, error) {
	walled := a.Wall != "" && a.Wall != "none"
	if !walled && a.PackageManagers == "" && a.MinReleaseAge == "" &&
		a.ModelBudget == 0 && a.UploadBudget == "" && len(a.SandboxEnv) == 0 {
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

	if a.ModelBudget == 0 {
		a.ModelBudget = s.Network.ModelBudget
	}
	if err := CheckModelBudget(a.ModelBudget); err != nil {
		return a, err
	}

	if a.UploadBudget == "" {
		a.UploadBudget = s.Network.UploadBudget
	}
	_, upBudget, err := CheckUploadBudget(a.UploadBudget)
	if err != nil {
		return a, err
	}
	a.UploadBudget = upBudget

	if len(s.Sandbox.Env) > 0 {
		merged := maps.Clone(s.Sandbox.Env)
		maps.Copy(merged, a.SandboxEnv)
		a.SandboxEnv = merged
	}
	return a, nil
}
