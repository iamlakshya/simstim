//go:build unix

package guest

import (
	"os/exec"
	"strings"
	"syscall"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
)

func TestBrowserHelperPrintsURL(t *testing.T) {
	dir := t.TempDir()
	if err := writeBrowserHelper(dir); err != nil {
		t.Fatalf("writeBrowserHelper: %v", err)
	}
	// Run in a new session with no controlling /dev/tty so the helper falls
	// back to stdout even when `go test` runs inside a PTY.
	cmd := exec.Command(BrowserHelperPath(dir), "https://example.invalid/callback")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run browser helper: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "Open in your browser: https://example.invalid/callback") {
		t.Fatalf("browser helper output = %q", out)
	}
}

func TestApplyStartPackageSettingsProtectsIdentityAndBrowser(t *testing.T) {
	home := t.TempDir()
	baseEnv := []string{
		"HOME=" + home,
		"BROWSER=/run/test/browser",
		brand.Env("HOST") + "=real-host",
		brand.Env("AGENT") + "=claude",
		brand.Env("SANDBOX") + "=real-box",
		brand.Env("SESSION") + "=abcd2345",
	}
	req := StartReq{
		Session:         "abcd2345",
		PackageManagers: "guarded",
		MinReleaseAge:   "7d",
		UserEnv: []string{
			"BROWSER=/bin/spoofed",
			brand.Env("HOST") + "=spoofed-host",
			brand.Env("AGENT") + "=spoofed-agent",
			brand.Env("SANDBOX") + "=spoofed-box",
			brand.Env("SESSION") + "=spoofed-id",
			brand.Env("CUSTOM") + "=spoofed-custom",
			"GOPROXY=https://corp.example",
		},
	}
	got, err := applyStartPackageSettings(req, baseEnv)
	if err != nil {
		t.Fatalf("applyStartPackageSettings: %v", err)
	}
	m := parseEnvSlice(got)
	if m["BROWSER"] != "/run/test/browser" {
		t.Errorf("BROWSER = %q, want /run/test/browser", m["BROWSER"])
	}
	if m[brand.Env("HOST")] != "real-host" ||
		m[brand.Env("AGENT")] != "claude" ||
		m[brand.Env("SANDBOX")] != "real-box" ||
		m[brand.Env("SESSION")] != "abcd2345" {
		t.Errorf("identity vars overridden: %+v", m)
	}
	if _, leaked := m[brand.Env("CUSTOM")]; leaked {
		t.Errorf("brand-prefixed user env leaked: %+v", m)
	}
	if m["GOPROXY"] != "https://corp.example" {
		t.Errorf("GOPROXY = %q, want https://corp.example", m["GOPROXY"])
	}
}
