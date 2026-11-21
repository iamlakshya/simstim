package client

import (
	"context"
	"fmt"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client/helpers"
	"github.com/iamlakshya/simstim/internal/version"
)

// HelperVersion names the folder the helper is installed into on hosts. It
// changes with every build of the helpers, so running sessions keep their own
// binary while new sessions use the new one.
func HelperVersion() string {
	h := helpers.Version()
	if h == "" {
		return version.Version
	}
	v := strings.NewReplacer("/", "-", "+", "-").Replace(version.Version)
	return v + "-" + h[:8]
}

// Platform is a host's operating system and architecture.
type Platform struct {
	OS, Arch string
	// Installed is true when the matching helper is already on the host.
	Installed bool
}

// Probe finds the host's platform and whether the helper is installed.
func Probe(ctx context.Context, s Transport) (Platform, error) {
	if err := EnsureHelpers(ctx); err != nil {
		return Platform{}, err
	}
	const marker = "__HELPER_PRESENT__"
	cmd := fmt.Sprintf(`uname -sm; test -x %s && echo %s; true`, RemoteHelper(HelperVersion()), marker)
	out, err := s.Run(ctx, cmd, nil)
	if err != nil {
		return Platform{}, err
	}
	var p Platform
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == marker {
			p.Installed = true
			continue
		}
		f := strings.Fields(line)
		if len(f) == 2 && p.OS == "" {
			p.OS, p.Arch = platformOf(f[0], f[1])
		}
	}
	if p.OS == "" {
		return p, fmt.Errorf("could not read the host's platform from %+q", CleanLine(string(out)))
	}
	return p, nil
}

// platformOf reads the system and the machine that uname prints. A value
// that no helper is built for is kept to show in an error, cleaned and cut
// short, since it is the host's own text. The supported platforms are
// linux on amd64 and arm64 (helpers.For).
func platformOf(system, machine string) (goos, arch string) {
	goos = strings.ToLower(system)
	switch machine {
	case "x86_64", "amd64":
		arch = "amd64"
	case "aarch64", "arm64":
		arch = "arm64"
	default:
		arch = api.CleanText(machine, 32)
	}
	if goos != "linux" && goos != "darwin" {
		goos = api.CleanText(goos, 32)
	}
	return goos, arch
}

// Install installs the checked helper and records its original ownership.
// Existing versions retain their own files and native lifetimes.
func Install(ctx context.Context, s Transport, p Platform) error {
	return InstallHelper(ctx, s, p)
}

// Prune runs the helper's prune: each install leaves the old versions
// behind, and the new helper removes those that nothing uses. A failure
// here never fails the install.
func Prune(ctx context.Context, s Transport) {
	_, _ = s.Run(ctx, RemoteHelper(HelperVersion())+" prune", nil)
}

// InstallHelper uploads the helper for p, and the shadow terminal when this
// build has one, checks each SHA-256 on the host, and moves them into place.
// It removes nothing, so a newer client's helper stays (cli-spec 11.4).
func InstallHelper(ctx context.Context, s Transport, p Platform) error {
	bin, ok := helpers.For(p.OS, p.Arch)
	if !ok {
		return &UserError{
			Msg:  fmt.Sprintf("this build has no %s helper for %s/%s", brand.Name, p.OS, p.Arch),
			Hint: "Use a Linux host on amd64 or arm64, or run `just build` to embed the helpers.",
			Code: "no_helper",
		}
	}
	files := []installCarriedV1{{brand.VTBinary, nil}, {brand.UsernsBinary, nil}, {brand.HostBinary, bin}}
	if vt, ok := helpers.VT(p.OS, p.Arch); ok {
		files[0].data = vt
	}
	if launcher, ok := helpers.Userns(p.OS, p.Arch); ok {
		files[1].data = launcher
	}
	return installOwnedHelperV1(ctx, s, p, files)
}
