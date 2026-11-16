package egress

import "github.com/iamlakshya/simstim/internal/proto"

// The actual schema visitor invokes this before its generic scalar choices.
// These two keys have public error codes and one low-level day/enum grammar.
func validatePackageManagerValue(key, value string) (bool, error) {
	switch key {
	case "sandbox.package_managers":
		return true, proto.ValidatePackageManagerMode(value)
	case "network.min_release_age":
		_, err := proto.ParsePackageReleaseAge(value)
		return true, err
	default:
		return false, nil
	}
}
