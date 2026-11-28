package client

import (
	"maps"
	"slices"

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

// CheckSandboxSystem validates sandbox.system and returns a UserError with code
// "system_invalid" when invalid (spec 6.6, 9.1).
func CheckSandboxSystem(raw string) (string, error) {
	sys, err := api.ParseSandboxSystem(raw)
	if err != nil {
		return "", &UserError{
			Msg:  api.SandboxSystemMessage(raw),
			Hint: api.SandboxSystemHint,
			Code: "system_invalid",
			Kind: KindUsage,
		}
	}
	return sys, nil
}

// ValidatePackageSettings checks sandbox and network policy defaults in s,
// returning a UserError on invalid values.
func ValidatePackageSettings(s Settings) error {
	if _, err := CheckPackageManagers(s.Sandbox.PackageManagers); err != nil {
		return err
	}
	if _, err := CheckSandboxSystem(s.Sandbox.System); err != nil {
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

// CheckSandboxKeep validates every [sandbox] keep entry and returns normalized
// paths, or a KindRefused UserError with code "keep_refused" (spec 6.11).
func CheckSandboxKeep(keep []string) ([]string, error) {
	if len(keep) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(keep))
	for _, raw := range keep {
		clean, err := api.CheckKeepPath(raw)
		if err != nil {
			return nil, &UserError{
				Msg:  err.Error(),
				Hint: "Remove shell startup and hook files (such as .bashrc or .profile) from [sandbox] keep in config.toml.",
				Code: api.KeepRefusedCode,
				Kind: KindRefused,
			}
		}
		if !slices.Contains(out, clean) {
			out = append(out, clean)
		}
	}
	return out, nil
}

// withPackageSettings fills and validates a session's package manager, microvm
// defaults, network budget and sandbox keep/shell defaults from config.toml
// when the session is walled or explicitly specifies them.
func withPackageSettings(a NewArgs) (NewArgs, error) {
	walled := a.Wall != "" && a.Wall != "none"
	s := LoadSettings()
	if len(a.Keep) == 0 && walled && len(s.Sandbox.Keep) > 0 {
		a.Keep = slices.Clone(s.Sandbox.Keep)
	}
	if len(a.Keep) > 0 {
		keep, err := CheckSandboxKeep(a.Keep)
		if err != nil {
			return a, err
		}
		a.Keep = keep
	}
	if a.Shell == "" && (walled || a.In != "") && s.Sandbox.Shell != "" {
		a.Shell = s.Sandbox.Shell
	}
	if !walled && a.PackageManagers == "" && a.MinReleaseAge == "" &&
		a.ModelBudget == 0 && a.UploadBudget == "" && len(a.SandboxEnv) == 0 &&
		a.Image == "" && !a.KeepSystem && !a.Docker && !a.Sudo {
		return a, nil
	}
	if a.PackageManagers == "" {
		a.PackageManagers = s.Sandbox.PackageManagers
	}
	lvl, err := CheckPackageManagers(a.PackageManagers)
	if err != nil {
		return a, err
	}
	a.PackageManagers = lvl

	sys, err := CheckSandboxSystem(s.Sandbox.System)
	if err != nil {
		return a, err
	}
	if !a.KeepSystem && sys == api.SandboxSystemKeep && a.Wall == "microvm" {
		a.KeepSystem = true
	}
	if a.Image == "" && s.Sandbox.Image != "" && a.Wall == "microvm" {
		a.Image = s.Sandbox.Image
	}
	if !a.Sudo && s.Sandbox.Sudo && walled {
		a.Sudo = true
	}
	if !a.Docker && s.Sandbox.Docker && walled {
		a.Docker = true
	}

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
