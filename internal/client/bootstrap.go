package client

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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

// Install uploads the helper for p, and the shadow terminal when this build
// has one, checks each SHA-256 on the host, and moves them into place. It never
// runs during a background reconnect.
func Install(ctx context.Context, s Transport, p Platform) error {
	bin, ok := helpers.For(p.OS, p.Arch)
	if !ok {
		return &UserError{
			Msg:  fmt.Sprintf("this build has no %s helper for %s/%s", brand.Name, p.OS, p.Arch),
			Hint: "Use a Linux host on amd64 or arm64, or run `just build` to embed the helpers.",
			Code: "no_helper",
		}
	}
	files := []struct {
		name string
		data []byte
	}{{brand.VTBinary, nil}, {brand.HostBinary, bin}}
	if vt, ok := helpers.VT(p.OS, p.Arch); ok {
		files[0].data = vt
	}
	ver := HelperVersion()
	for i, f := range files {
		if f.data == nil {
			continue
		}
		last := i == len(files)-1
		if err := upload(ctx, s, ver, f.name, f.data, last); err != nil {
			return err
		}
	}
	// Each install leaves the old versions behind. The new helper removes
	// those that nothing uses; a failure here never fails the install.
	_, _ = s.Run(ctx, RemoteHelper(ver)+" prune", nil)
	return nil
}

// upload sends one file into the versioned helper folder. The helper itself
// goes last, because its presence is what marks the version as installed.
func upload(ctx context.Context, s Transport, ver, name string, data []byte, last bool) error {
	sum := sha256.Sum256(data)
	hexsum := hex.EncodeToString(sum[:])
	dir := fmt.Sprintf(`"$HOME/.local/share/%s/bin/%s"`, brand.Name, ver)
	steps := []string{
		"set -e",
		"umask 077",
		"d=" + dir,
		`mkdir -p "$d"`,
		`t="$d/.upload.$$"`,
		`cat > "$t"`,
		fmt.Sprintf(`echo "%s  $t" | sha256sum -c --quiet - || { rm -f "$t"; echo "checksum mismatch" >&2; exit 3; }`, hexsum),
		`chmod 755 "$t"`,
		fmt.Sprintf(`mv -f "$t" "$d/%s"`, name),
	}
	if last {
		steps = append(steps, fmt.Sprintf(`ln -sfn %q "$HOME/.local/share/%s/bin/current"`, ver, brand.Name))
	}
	steps = append(steps, "echo __INSTALLED__")
	out, err := s.Run(ctx, strings.Join(steps, "; "), bytes.NewReader(data))
	if err != nil {
		return err
	}
	if !strings.Contains(string(out), "__INSTALLED__") {
		return fmt.Errorf("the install of %s did not finish: %s", name, tailStderr(string(out)))
	}
	return nil
}
