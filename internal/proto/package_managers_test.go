package proto

import (
	"errors"
	"testing"
)

func TestPackageReleaseAgeUsesWholeSupportedDaysWithoutRounding(t *testing.T) {
	for input, expected := range map[string]int{"0d": 0, "1d": 1, "7d": 7, "365d": 365} {
		got, err := ParsePackageReleaseAge(input)
		if err != nil || got != expected {
			t.Fatalf("age %q = %d %v", input, got, err)
		}
	}
	for _, input := range []string{"", "90m", "24h", "7", "1.5d", "-1d", "+7d", "366d", "999999999999999999999d", "7D", " 7d"} {
		var failure *Error
		if _, err := ParsePackageReleaseAge(input); !errors.As(err, &failure) || failure.Code != "release_age_invalid" || failure.Hint == "" {
			t.Fatalf("invalid age %q lost its public refusal: %v", input, err)
		}
	}
}

func TestPackageManagerModeUsesOnlyTheExistingThreeLevels(t *testing.T) {
	for _, mode := range []string{"guarded", "strict", "off"} {
		if err := ValidatePackageManagerMode(mode); err != nil {
			t.Fatal(mode, err)
		}
	}
	for _, mode := range []string{"", "balanced", "on", "Guarded"} {
		var failure *Error
		if err := ValidatePackageManagerMode(mode); !errors.As(err, &failure) || failure.Code != "package_level_invalid" || failure.Hint == "" {
			t.Fatalf("invalid mode lost its exact code: %q %v", mode, err)
		}
	}
}

func TestPackageManagerPlanRetainsOneCanonicalUTCStart(t *testing.T) {
	input := &PackageManagersV1{Version: 1, Mode: "guarded", MinReleaseAge: "7d", StartedAt: "2026-11-15T12:00:00Z"}
	if err := ValidatePackageManagersV1(input); err != nil {
		t.Fatal(err)
	}
	copy := ClonePackageManagersV1(input)
	copy.Mode = "off"
	if input.Mode != "guarded" {
		t.Fatal("start plan clone changed the original")
	}
	for _, mutate := range []func(*PackageManagersV1){
		func(p *PackageManagersV1) { p.Version = 2 },
		func(p *PackageManagersV1) { p.StartedAt = "" },
		func(p *PackageManagersV1) { p.StartedAt = "2026-11-15T12:00:00+00:00" },
		func(p *PackageManagersV1) { p.StartedAt = "2026-11-15T13:00:00+01:00" },
	} {
		changed := ClonePackageManagersV1(input)
		mutate(changed)
		if err := ValidatePackageManagersV1(changed); err == nil {
			t.Fatal("an unsupported or inexact start plan was accepted")
		}
	}
}
