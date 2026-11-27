package egress

import (
	"errors"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestCanonicalPolicyValidationUsesSpecificPackageSettingCodes(t *testing.T) {
	for _, test := range []struct {
		value api.LayerValues
		code  string
	}{
		{value: api.LayerValues{Network: api.NetworkValues{MinReleaseAge: packageValue("90m")}}, code: "release_age_invalid"},
		{value: api.LayerValues{Network: api.NetworkValues{MinReleaseAge: packageValue("366d")}}, code: "release_age_invalid"},
		{value: api.LayerValues{Sandbox: api.SandboxValues{PackageManagers: packageValue("on")}}, code: "package_level_invalid"},
	} {
		var failure *proto.Error
		if err := ValidateLayerValues(test.value); !errors.As(err, &failure) || failure.Code != test.code || failure.Hint == "" {
			t.Fatalf("canonical package value refusal = %v; want %s", err, test.code)
		}
	}
	for _, value := range []string{"0d", "1d", "7d", "365d"} {
		if err := ValidateLayerValues(api.LayerValues{Network: api.NetworkValues{MinReleaseAge: &value}}); err != nil {
			t.Fatal(value, err)
		}
	}
}

func packageValue(value string) *string { return &value }
