package cli

import "testing"

func TestPackageManagerFindingsKeepTheirCodeAndSourceLine(t *testing.T) {
	for _, test := range []struct {
		name, body, code string
	}{
		{"duration", "[network]\nmin_release_age = '90m'\n", "release_age_invalid"},
		{"range", "[network]\nmin_release_age = '366d'\n", "release_age_invalid"},
		{"level", "[sandbox]\npackage_managers = 'unknown'\n", "package_level_invalid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			found := layerFindings(t, "config.toml", test.body)
			if len(found) != 1 || found[0].Code != test.code || found[0].Line != 2 {
				t.Fatalf("finding lost its precise code or line: %+v", found)
			}
		})
	}
}
