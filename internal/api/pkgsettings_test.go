package api

import (
	"strings"
	"testing"
)

func TestParsePackageManagers(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
	}{
		{"", "guarded"},
		{"guarded", "guarded"},
		{"strict", "strict"},
		{"off", "off"},
	} {
		got, err := ParsePackageManagers(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("ParsePackageManagers(%q) = %q, %v; want %q, nil", tc.in, got, err, tc.want)
		}
	}

	for _, bad := range []string{"on", "true", "Guarded", "none", " strict"} {
		_, err := ParsePackageManagers(bad)
		if err == nil {
			t.Errorf("ParsePackageManagers(%q) succeeded, want error", bad)
			continue
		}
		if !strings.Contains(err.Error(), "guarded, strict or off") || !strings.Contains(err.Error(), "Try:") {
			t.Errorf("ParsePackageManagers(%q) err = %q, want spec message with Try:", bad, err)
		}
	}
}

func TestParseMinReleaseAge(t *testing.T) {
	for _, tc := range []struct {
		in        string
		wantDays  int
		wantCanon string
	}{
		{"", 7, "7d"},
		{"0d", 0, "0d"},
		{"1d", 1, "1d"},
		{"7d", 7, "7d"},
		{"365d", 365, "365d"},
	} {
		days, canon, err := ParseMinReleaseAge(tc.in)
		if err != nil || days != tc.wantDays || canon != tc.wantCanon {
			t.Errorf("ParseMinReleaseAge(%q) = (%d, %q, %v); want (%d, %q, nil)", tc.in, days, canon, err, tc.wantDays, tc.wantCanon)
		}
	}

	for _, bad := range []string{"90m", "7", "7D", "07d", "-1d", "+7d", "366d", "1.5d", "d", "7d ", "abc"} {
		_, _, err := ParseMinReleaseAge(bad)
		if err == nil {
			t.Errorf("ParseMinReleaseAge(%q) succeeded, want error", bad)
			continue
		}
		if !strings.Contains(err.Error(), "whole number of days from 0d to 365d") || !strings.Contains(err.Error(), "Try:") {
			t.Errorf("ParseMinReleaseAge(%q) err = %q, want spec message with Try:", bad, err)
		}
	}
}

func TestFormatPackageSummary(t *testing.T) {
	for _, tc := range []struct {
		level, age, want string
	}{
		{"", "", "guarded, 7 days"},
		{"guarded", "7d", "guarded, 7 days"},
		{"guarded", "1d", "guarded, 1 day"},
		{"guarded", "0d", "guarded, 0 days"},
		{"strict", "7d", "strict, 7 days"},
		{"off", "7d", "off"},
	} {
		if got := FormatPackageSummary(tc.level, tc.age); got != tc.want {
			t.Errorf("FormatPackageSummary(%q, %q) = %q, want %q", tc.level, tc.age, got, tc.want)
		}
	}
}
