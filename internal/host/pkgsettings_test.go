package host

import (
	"errors"
	"testing"

	"github.com/iamlakshya/simstim/internal/proto"
)

func TestNewWalledRejectsInvalidPackageSettings(t *testing.T) {
	_, err := newWalled(NewArgs{
		Name:            "bad-level",
		Cwd:             "~",
		Argv:            []string{"true"},
		Wall:            "plain",
		PackageManagers: "on",
	})
	var pe *proto.Error
	if !errors.As(err, &pe) || pe.Code != "package_level_invalid" || pe.Hint == "" {
		t.Fatalf("newWalled(on) = %#v, want proto.Error{Code:package_level_invalid}", err)
	}

	_, err = newWalled(NewArgs{
		Name:          "bad-age",
		Cwd:           "~",
		Argv:          []string{"true"},
		Wall:          "plain",
		MinReleaseAge: "90m",
	})
	pe = nil
	if !errors.As(err, &pe) || pe.Code != "release_age_invalid" || pe.Hint == "" {
		t.Fatalf("newWalled(90m) = %#v, want proto.Error{Code:release_age_invalid}", err)
	}
}
