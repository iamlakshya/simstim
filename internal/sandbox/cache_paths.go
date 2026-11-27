package sandbox

import (
	"fmt"
	"strings"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/files"
)

// protectedHomePaths are installed programs, start-up settings and credential
// homes. A cache root may not include them by naming a broader parent.
var protectedHomePaths = []string{
	".cargo/bin", "go/bin", ".local/bin", ".npm/_npx",
	".local/share/pnpm/pnpm", ".local/share/pnpm/pnpx",
	".gradle/init.d", ".gradle/gradle.properties", ".gradle/jdks", ".gradle/wrapper", ".m2/settings.xml",
	".yarn/releases", ".yarn/plugins", ".yarn/sdks",
	".local/share/yarn/berry/plugins", ".local/share/yarn/berry/releases", ".local/share/yarn/berry/sdks",
	".ssh", ".aws", ".gnupg", ".kube", ".docker",
	".netrc", ".git-credentials", ".npmrc", ".pypirc",
}

// ValidateCachePath checks one actual home-relative cache root. Layer list
// merging strips ! before calling it. It checks the root, not cache contents:
// downloaded source may contain executables or nested Git metadata.
func ValidateCachePath(name string) error {
	if len(name) > 4096 || strings.HasPrefix(name, "!") || strings.HasPrefix(name, "~") || api.CleanText(name, 4096) != name {
		return fmt.Errorf("cache path must be a clean home-relative folder")
	}
	if err := (files.StatePlan{Dirs: []string{name}}).Validate(); err != nil {
		return fmt.Errorf("unsafe cache path %q: %w", name, err)
	}
	for _, part := range strings.Split(name, "/") {
		switch part {
		case "bin", "sbin", "node_modules", ".venv", "venv", "virtualenvs":
			return fmt.Errorf("cache path %q names a folder of programs", name)
		}
	}
	for _, protected := range protectedHomePaths {
		if name == protected || strings.HasPrefix(name, protected+"/") || strings.HasPrefix(protected, name+"/") {
			return fmt.Errorf("cache path %q includes programs, settings or credentials", name)
		}
	}
	for _, agent := range agents.Table {
		for _, installed := range agent.InstallDirs {
			if name == installed || strings.HasPrefix(name, installed+"/") || strings.HasPrefix(installed, name+"/") {
				return fmt.Errorf("cache path %q overlaps an installed program folder", name)
			}
		}
	}
	return nil
}
