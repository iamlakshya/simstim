package lima

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/provider"
)

// limaVersion is a version of Lima, as `limactl --version` prints it.
type limaVersion struct {
	text                string // as printed, such as 2.3.0-rc.1
	major, minor, patch int
	// pre is true for a pre-release, such as 2.3.0-rc.1, which comes
	// before its release.
	pre bool
}

// MinVersion is the oldest Lima that host create uses. Lima 2.3.0 added
// networkOpts.hostLoopback.
const MinVersion = "2.3.0"

var minVersion = limaVersion{text: MinVersion, major: 2, minor: 3}

var (
	// versionRe reads x.y.z, then a pre-release such as -rc.1, then build
	// metadata: .m, which Lima's Makefile adds for a changed tree (the
	// --dirty=.m of its version line), and +..., which a packager may add.
	versionRe = regexp.MustCompile(`^limactl version v?(\d+)\.(\d+)\.(\d+)(-[0-9A-Za-z.-]+)?(\.m)?(\+[0-9A-Za-z.-]+)?$`)
	// describeRe is the suffix of a build from a commit after a tag, as
	// git describe writes it: 2.3.0-5-g1a2b3c4 comes after 2.3.0.
	describeRe = regexp.MustCompile(`^-\d+-g[0-9a-f]+(\.m)?$`)
)

// parseLimaVersion reads the first line of `limactl --version`, such as
// "limactl version 2.3.0".
func parseLimaVersion(s string) (limaVersion, error) {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	line = strings.TrimSpace(line)
	m := versionRe.FindStringSubmatch(line)
	if m == nil {
		return limaVersion{}, fmt.Errorf("not a version of limactl: %q", line)
	}
	v := limaVersion{text: strings.TrimPrefix(strings.TrimPrefix(line, "limactl version "), "v")}
	for i, p := range []*int{&v.major, &v.minor, &v.patch} {
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return limaVersion{}, fmt.Errorf("not a version of limactl: %q", line)
		}
		*p = n
	}
	v.pre = m[4] != "" && !describeRe.MatchString(m[4])
	return v, nil
}

// before reports whether v is older than w. A pre-release comes before its
// release.
func (v limaVersion) before(w limaVersion) bool {
	if v.major != w.major {
		return v.major < w.major
	}
	if v.minor != w.minor {
		return v.minor < w.minor
	}
	if v.patch != w.patch {
		return v.patch < w.patch
	}
	return v.pre && !w.pre
}

// checkVersion refuses a Lima older than MinVersion, and one whose version
// it cannot read. A missing limactl (already a UserError from Exec) or a
// cancelled context returns as it is.
func checkVersion(ctx context.Context) error {
	out, err := provider.Exec(ctx, install, "limactl", "--version")
	var userErr *client.UserError
	if errors.As(err, &userErr) || (err != nil && ctx.Err() != nil) {
		return err
	}
	if err != nil {
		return &client.UserError{Msg: fmt.Sprintf("could not read the version of limactl from %q", client.CleanLine(failure(out, err))), Hint: "limactl --version"}
	}
	text := strings.TrimSpace(string(out))
	v, err := parseLimaVersion(text)
	if err != nil {
		return &client.UserError{Msg: fmt.Sprintf("could not read the version of limactl from %q", client.CleanLine(text)), Hint: "limactl --version"}
	}
	if v.before(minVersion) {
		return tooOld(v.text, goos)
	}
	return nil
}

// tooOld is the error for a Lima before MinVersion. Only macOS mentions
// loopback services, because QEMU on Linux cannot close that path.
func tooOld(version, system string) error {
	why := MinVersion + " or later can keep the VM away from your Mac's loopback services"
	if system != "darwin" {
		why = "it needs " + MinVersion + " or later"
	}
	return &client.UserError{
		Msg:  fmt.Sprintf("Lima %s is too old for host create: %s", version, why),
		Hint: "Update Lima (https://lima-vm.io/docs/installation/), then run this again.",
	}
}

// failure is what a failed limactl printed: its stderr, else its output,
// else how it ended. The caller cleans it to show it.
func failure(out []byte, err error) string {
	var ee *provider.ExecError
	if errors.As(err, &ee) {
		if s := strings.TrimSpace(ee.Stderr); s != "" {
			return s
		}
		if s := strings.TrimSpace(string(out)); s != "" {
			return s
		}
		if ee.Err != nil {
			return ee.Err.Error()
		}
	}
	return err.Error()
}
