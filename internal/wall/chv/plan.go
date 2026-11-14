package chv

import (
	"github.com/iamlakshya/simstim/internal/supply"
	"github.com/iamlakshya/simstim/internal/vmneed"
)

var bootRoles = vmneed.Roles()

func validateBootRuntime(manifest supply.Manifest, arch string) error {
	return vmneed.ValidateRuntime(manifest, arch)
}
