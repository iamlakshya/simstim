package host

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
)

type walledSettings struct {
	pkgLevel     string
	minAge       string
	modelBudget  int64
	uploadBudget string
}

func checkWalledSettings(a NewArgs) (walledSettings, error) {
	if err := checkMicroVMOnlyOptions(a); err != nil {
		return walledSettings{}, err
	}
	pkgLevel, err := api.ParsePackageManagers(a.PackageManagers)
	if err != nil {
		return walledSettings{}, UserError("package_level_invalid", api.PackageLevelMessage(a.PackageManagers), api.PackageLevelHint)
	}
	_, minAge, err := api.ParseMinReleaseAge(a.MinReleaseAge)
	if err != nil {
		return walledSettings{}, UserError("release_age_invalid", api.ReleaseAgeMessage(a.MinReleaseAge), api.ReleaseAgeHint)
	}
	if err := api.CheckModelBudget(a.ModelBudget); err != nil {
		return walledSettings{}, UserError("model_budget_invalid", api.ModelBudgetMessage(a.ModelBudget), api.ModelBudgetHint)
	}
	_, canonUpload, err := api.ParseUploadBudget(a.UploadBudget)
	if err != nil {
		return walledSettings{}, UserError("upload_budget_invalid", api.UploadBudgetMessage(a.UploadBudget), api.UploadBudgetHint)
	}
	return walledSettings{
		pkgLevel:     pkgLevel,
		minAge:       minAge,
		modelBudget:  a.ModelBudget,
		uploadBudget: canonUpload,
	}, nil
}

// checkMicroVMOnlyOptions refuses microvm-only options (sandbox.sudo, --docker,
// --keep-system, --image) on weaker walls or unwalled sessions (spec 7.5, 9.1,
// 17.10, question 98).
func checkMicroVMOnlyOptions(a NewArgs) error {
	if a.Wall == "microvm" {
		return nil
	}
	wallName := a.Wall
	if wallName == "" {
		wallName = "none"
	}
	if a.Sudo {
		return UserError("sudo_wall",
			"sandbox.sudo needs a microvm sandbox, and this session runs at "+wallName,
			"Use a microvm sandbox, or turn sandbox.sudo off in config.toml.")
	}
	if a.Docker {
		return UserError("docker_wall",
			"--docker needs a microvm sandbox, and this session runs at "+wallName,
			"Use a microvm sandbox, or omit --docker.")
	}
	if a.KeepSystem {
		return UserError("keep_system_wall",
			"--keep-system needs a microvm sandbox, and this session runs at "+wallName,
			"Use a microvm sandbox, or omit --keep-system.")
	}
	if a.Image != "" {
		return UserError("image_wall",
			"--image needs a microvm sandbox, and this session runs at "+wallName,
			"Use a microvm sandbox, or omit --image.")
	}
	return nil
}

// buildSessionPolicyTOML serialises the initial session network policy passed
// to the egress process on start.
func buildSessionPolicyTOML(preset string, ws walledSettings) string {
	var b strings.Builder
	b.WriteString("[sandbox]\n")
	fmt.Fprintf(&b, "preset = %s\n", strconv.Quote(preset))
	if ws.pkgLevel != "" {
		fmt.Fprintf(&b, "package_managers = %s\n", strconv.Quote(ws.pkgLevel))
	}
	b.WriteString("\n[network]\n")
	if ws.minAge != "" {
		fmt.Fprintf(&b, "min_release_age = %s\n", strconv.Quote(ws.minAge))
	}
	if ws.modelBudget > 0 {
		fmt.Fprintf(&b, "model_budget = %d\n", ws.modelBudget)
	}
	if ws.uploadBudget != "" {
		fmt.Fprintf(&b, "upload_budget = %s\n", strconv.Quote(ws.uploadBudget))
	}
	return b.String()
}
