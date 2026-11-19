package guest

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/files"
	"github.com/iamlakshya/simstim/internal/proto"
)

// New package settings forms are closed before infrastructure or Store work.
// Missing fields retain legacy decoding, while null/aliases cannot donate it.
func validatePackageManagerWire(data []byte, args proto.CtlStartArgs) error {
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil {
		return packageManagerFailure("the member package settings are malformed")
	}
	present := false
	for name := range fields {
		present = present || strings.EqualFold(name, "package_managers")
	}
	if !present {
		return nil
	}
	if !api.ExactJSONFields(data, proto.CtlStartArgs{}) || len(fields["package_managers"]) == 0 || bytes.Equal(bytes.TrimSpace(fields["package_managers"]), []byte("null")) || args.PackageManagers == nil {
		return packageManagerFailure("the member package settings fields are unsupported or ambiguous")
	}
	return proto.ValidatePackageManagersV1(args.PackageManagers)
}

func (g *Init) validatePackageStateFiles(plan *proto.PackageManagersV1, names []string) error {
	if plan == nil && !g.packageSettingsRequired {
		return nil
	}
	paths, err := proto.PackageManagerPaths()
	if err != nil {
		return err
	}
	state, err := files.ExcludeGeneratedHomeState(files.StatePlan{Files: names}, paths)
	if err != nil {
		return err
	}
	if len(state.Files) != len(names) {
		return packageManagerFailure("generated package settings cannot be restored from an earlier start")
	}
	return nil
}
