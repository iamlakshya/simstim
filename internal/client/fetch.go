package client

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client/helpers"
	"github.com/iamlakshya/simstim/internal/version"
)

// EnsureHelpers makes the host helpers available to a client built without
// them (go install): it fetches the bundle of the client's own release once,
// checks its signature and checksum, and caches it. A client that embeds its
// helpers does nothing here.
func EnsureHelpers(ctx context.Context) error {
	if helpers.Embedded() {
		return nil
	}
	base := ReleaseURL()
	if base == "" {
		return &UserError{
			Msg:  "this build has no host helpers, and no release to fetch them from",
			Hint: "Install a release (see the install guide), or build from source with `just build`.",
			Code: "no_helper",
		}
	}
	if err := helpers.Fetch(ctx, version.Version, base, CacheDir()); err != nil {
		return &UserError{
			Msg:  "could not fetch the host helpers for " + version.Version + ": " + err.Error(),
			Hint: "Check your network, or install a release with the installer.",
			Code: "fetch_failed",
		}
	}
	return nil
}

// ReleaseURL is where this client's release files are: the environment
// variable <NAME>_RELEASES_URL (for a mirror), or the GitHub release of the
// client's version, from its module path. It is empty for a development build.
func ReleaseURL() string {
	if v := os.Getenv(brand.Env("RELEASES_URL")); v != "" {
		return v
	}
	mod := version.Module()
	if !strings.HasPrefix(mod, "github.com/") || version.Version == "" || strings.Contains(version.Version, "dev") {
		return ""
	}
	return fmt.Sprintf("https://%s/releases/download/v%s", mod, version.Version)
}
