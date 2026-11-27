package guest

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/proto"
)

// PackageManagerHelperMode is the closed fixed-account home-settings writer.
const PackageManagerHelperMode = "wall-packages-v1"

func packageManagerFailure(message string) error {
	return &proto.Error{Code: "wall_setup", Message: message, Hint: "Start the sandbox with matching helper files; set sandbox.package_managers = \"off\" if you manage these home settings yourself."}
}

// PackageManagerEnvironment supplies package defaults before the already
// resolved ordinary environment. An explicit user name, even empty, wins.
// Protected identity, CA/proxy and canonical secret delivery run afterwards.
// This function reads no host environment and changes no caller-owned slice.
func PackageManagerEnvironment(plan *proto.PackageManagersV1, environment []string) ([]string, error) {
	defaults, _, err := proto.RenderPackageManagerSettings(plan)
	if err != nil {
		return nil, err
	}
	if len(defaults) == 0 {
		return slices.Clone(environment), nil
	}
	owned := make(map[string]bool, len(environment))
	for _, entry := range environment {
		name, value, found := strings.Cut(entry, "=")
		if !found || owned[name] || api.CheckSecretName(name, true) != api.SecretNameOK || strings.ContainsRune(value, 0) {
			return nil, packageManagerFailure("the original package settings environment is invalid or ambiguous")
		}
		owned[name] = true
	}
	output := make([]string, 0, len(defaults)+len(environment))
	for _, setting := range defaults {
		present := owned[setting.Name]
		if setting.CaseInsensitive && !present {
			for name := range owned {
				if strings.EqualFold(name, setting.Name) {
					present = true
					break
				}
			}
		}
		if !present {
			output = append(output, setting.Name+"="+setting.Value)
		}
	}
	return append(output, environment...), nil
}

func decodePackageManagerPlan(data []byte) (*proto.PackageManagersV1, error) {
	var plan proto.PackageManagersV1
	if len(data) == 0 || len(data) > proto.PackageManagerPayloadMax || !api.ExactJSONFields(data, plan) || json.Unmarshal(data, &plan) != nil {
		return nil, packageManagerFailure("the private package settings plan is malformed")
	}
	if err := proto.ValidatePackageManagersV1(&plan); err != nil {
		return nil, err
	}
	return &plan, nil
}

// RunPackageManagerHelper consumes only the shared V1 data plan. It writes
// fixed home-relative files as the existing UID1000 helper account, never as
// privileged Init or through a caller-provided path, command, mode or env.
func RunPackageManagerHelper(args []string) (returned error) {
	if len(args) != 0 {
		return packageManagerFailure("the package settings helper takes no arguments")
	}
	if err := checkInputHelperAccount(); err != nil {
		return err
	}
	data, err := io.ReadAll(io.LimitReader(os.Stdin, proto.PackageManagerPayloadMax+1))
	if err != nil {
		return packageManagerFailure("the private package settings plan could not be read")
	}
	plan, err := decodePackageManagerPlan(data)
	if err != nil {
		return err
	}
	if plan.Mode == "off" {
		return nil
	}
	root, err := os.OpenRoot(host.GuestHome)
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, root.Close()) }()
	return writePackageManagerFiles(context.Background(), root, plan)
}
