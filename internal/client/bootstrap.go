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
	// Installed is true when the matching helper is already on the host and,
	// when this client has helper digests, its SHA-256 matches.
	Installed bool
	// DigestMismatch is true when a helper binary is at the expected path on
	// the host, but its SHA-256 does not match this client's digest.
	DigestMismatch bool
}

const (
	helperPresentMarker        = "__HELPER_PRESENT__"
	helperDigestMismatchMarker = "__HELPER_DIGEST_MISMATCH__"
	helperDigestNoToolMarker   = "__HELPER_DIGEST_NO_TOOL__"
)

// helperDigests returns the expected SHA-256 digests of the host helpers for
// this build (embedded, or loaded from the release cache). Tests replace it.
var helperDigests = helpers.Digests

// ensureHelpers ensures host helpers are available before probing a host. Tests replace it.
var ensureHelpers = EnsureHelpers

// unameCasePattern returns the POSIX case pattern matching `uname -s`:`uname -m`
// for a Go OS and architecture.
func unameCasePattern(goos, goarch string) string {
	var osNames []string
	switch goos {
	case "linux":
		osNames = []string{"Linux"}
	case "darwin":
		osNames = []string{"Darwin"}
	default:
		osNames = []string{goos}
	}
	var archNames []string
	switch goarch {
	case "amd64":
		archNames = []string{"x86_64", "amd64"}
	case "arm64":
		archNames = []string{"aarch64", "arm64"}
	default:
		archNames = []string{goarch}
	}
	patterns := make([]string, 0, len(osNames)*len(archNames))
	for _, o := range osNames {
		for _, a := range archNames {
			patterns = append(patterns, o+":"+a)
		}
	}
	return strings.Join(patterns, "|")
}

// helperDigestWantShell builds a POSIX shell fragment that sets $want to the
// expected SHA-256 hex digest for the host's `uname -s`:`uname -m`, or leaves
// it empty if digests has no entry for this platform.
func helperDigestWantShell(digests []helpers.HelperDigest) string {
	var b strings.Builder
	b.WriteString(`want=""; case "$(uname -s):$(uname -m)" in `)
	for _, d := range digests {
		fmt.Fprintf(&b, `%s) want=%s ;; `, unameCasePattern(d.OS, d.Arch), d.SHA256)
	}
	b.WriteString("esac")
	return b.String()
}

// probeCommand builds the remote POSIX shell command that reads the host's
// platform and checks whether the matching helper is installed. When digests
// is non-empty, an installed helper counts as present only when its SHA-256
// matches the digest for the host's platform.
func probeCommand(ver string, digests []helpers.HelperDigest) string {
	helper := RemoteHelper(ver)
	if len(digests) == 0 {
		return fmt.Sprintf(`uname -sm; test -x %s && echo %s; true`, helper, helperPresentMarker)
	}
	return strings.Join([]string{
		"uname -sm",
		"h=" + helper,
		`if [ -f "$h" ] && [ -x "$h" ]; then ` +
			helperDigestWantShell(digests) + `; ` +
			`if [ -z "$want" ]; then echo ` + helperDigestMismatchMarker + `; ` +
			`elif command -v sha256sum >/dev/null 2>&1; then ` +
			`if echo "$want  $h" | sha256sum -c - >/dev/null 2>&1; then echo ` + helperPresentMarker + `; else echo ` + helperDigestMismatchMarker + `; fi; ` +
			`elif command -v shasum >/dev/null 2>&1; then ` +
			`if echo "$want  $h" | shasum -a 256 -c - >/dev/null 2>&1; then echo ` + helperPresentMarker + `; else echo ` + helperDigestMismatchMarker + `; fi; ` +
			`else echo ` + helperDigestNoToolMarker + `; fi; fi`,
		"true",
	}, "; ")
}

// bridgeCommand builds the remote POSIX shell command that starts the host
// helper's bridge. When digests is non-empty, it checks the helper's SHA-256
// on the host with sha256sum -c (or shasum -a 256 -c) and starts the bridge
// only when the digest matches (spec 14.9.2).
func bridgeCommand(ver string, idleExit int, digests []helpers.HelperDigest) string {
	args := "bridge"
	if idleExit > 0 {
		args = fmt.Sprintf("bridge --idle-exit %d", idleExit)
	}
	helper := RemoteHelper(ver)
	if len(digests) == 0 {
		return helper + " " + args
	}
	return strings.Join([]string{
		"h=" + helper,
		`if [ ! -e "$h" ]; then echo "No such file or directory: $h" >&2; exit 127; fi`,
		`if [ ! -f "$h" ]; then echo "` + helperDigestMismatchMarker + `" >&2; exit 66; fi`,
		helperDigestWantShell(digests),
		`if [ -z "$want" ]; then echo "` + helperDigestMismatchMarker + `" >&2; exit 66; fi`,
		`if command -v sha256sum >/dev/null 2>&1; then echo "$want  $h" | sha256sum -c - >/dev/null 2>&1 || { echo "` + helperDigestMismatchMarker + `" >&2; exit 66; }; elif command -v shasum >/dev/null 2>&1; then echo "$want  $h" | shasum -a 256 -c - >/dev/null 2>&1 || { echo "` + helperDigestMismatchMarker + `" >&2; exit 66; }; else echo "` + helperDigestNoToolMarker + `" >&2; exit 66; fi`,
		`exec "$h" ` + args,
	}, "; ")
}

// Probe finds the host's platform and whether the helper is installed.
func Probe(ctx context.Context, s Transport) (Platform, error) {
	if err := ensureHelpers(ctx); err != nil {
		return Platform{}, err
	}
	cmd := probeCommand(HelperVersion(), helperDigests())
	out, err := s.Run(ctx, cmd, nil)
	if err != nil {
		return Platform{}, err
	}
	var p Platform
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		switch line {
		case helperPresentMarker:
			p.Installed = true
			continue
		case helperDigestMismatchMarker:
			p.DigestMismatch = true
			continue
		case helperDigestNoToolMarker:
			return Platform{}, &UserError{
				Msg:  fmt.Sprintf("cannot check the %s helper's SHA-256 on the host: neither sha256sum nor shasum is installed", brand.Name),
				Hint: "Install coreutils (sha256sum) on the host, then try again.",
				Code: "digest_mismatch",
				Kind: KindFailed,
			}
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
