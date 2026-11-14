package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/client/helpers"
	"github.com/iamlakshya/simstim/internal/version"
)

// A host whose helper is this client's: host upgrade says so, and changes
// nothing. It does not start the bridge (cli-spec 11.4).
func TestHostUpgradeWithTheSameHelper(t *testing.T) {
	_, tr := withFake(t, nil)
	if err := saveHost(client.Host{Name: "box", Machine: &client.Machine{Provider: "fake", ID: "fake-box"}}); err != nil {
		t.Fatal(err)
	}
	if said := okSaid(t, "host", "upgrade", "box"); said != "box has helper "+client.HelperVersion()+" already.\n" {
		t.Errorf("host upgrade said %q", said)
	}
	out := okJSON(t, "host", "upgrade", "box", "--json")
	checkJSON(t, "host upgrade", out, false)
	var res map[string]any
	if err := json.Unmarshal([]byte(out), &res); err != nil || res["outcome"] != "unchanged" ||
		res["v"] != float64(1) || res["host"] != "box" || res["helper"] != client.HelperVersion() {
		t.Errorf("host upgrade --json = %v, %v", res, err)
	}
	for _, r := range tr.Remotes() {
		if strings.Contains(r, " bridge") || strings.Contains(r, "__INSTALLED__") || strings.Contains(r, " prune") {
			t.Errorf("host upgrade ran %q", r)
		}
	}
}

// A host without this client's helper: host upgrade installs it, asks
// nothing, removes no other helper, and then connects.
func TestHostUpgradeInstalls(t *testing.T) {
	if _, ok := helpers.For("linux", "arm64"); !ok {
		t.Skip("this build has no helper for linux/arm64 (run just helpers)")
	}
	_, tr := withFake(t, nil)
	tr.NoHelper = true
	if err := saveHost(client.Host{Name: "box", Machine: &client.Machine{Provider: "fake", ID: "fake-box"}}); err != nil {
		t.Fatal(err)
	}
	out, errOut, err := runApart(t, "host", "upgrade", "box", "--no-input")
	if err != nil || out != "" || errOut != "Installed "+brand.HostBinary+" "+client.HelperVersion()+" on box.\n" {
		t.Fatalf("host upgrade: %v\nstdout %q\nstderr %q", err, out, errOut)
	}
	remotes := strings.Join(tr.Remotes(), "\n")
	if !strings.Contains(remotes, "__INSTALLED__") || !strings.Contains(remotes, " bridge") {
		t.Errorf("host upgrade did not install, then connect:\n%s", remotes)
	}
	if strings.Contains(remotes, " prune") {
		t.Error("host upgrade pruned the other helpers")
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(okJSON(t, "host", "upgrade", "box", "--json")), &res); err != nil || res["outcome"] != "installed" {
		t.Errorf("host upgrade --json = %v, %v", res, err)
	}
}

// A client built with go install starts with no helpers, and its helper
// version changes when it loads them. host upgrade reports the version
// that it installed, read after the load (cli-spec 11.4, rule 2).
func TestHostUpgradeReportsTheFetchedVersion(t *testing.T) {
	_, tr := withFake(t, nil)
	tr.NoHelper = true
	if err := saveHost(client.Host{Name: "box", Machine: &client.Machine{Provider: "fake", ID: "fake-box"}}); err != nil {
		t.Fatal(err)
	}
	installed := asGoInstall(t)
	var res map[string]any
	if err := json.Unmarshal([]byte(okJSON(t, "host", "upgrade", "box", "--json")), &res); err != nil ||
		res["helper"] != installed || res["outcome"] != upgradeInstalled {
		t.Errorf("host upgrade --json = %v, %v, want the loaded helper %s, installed", res, err, installed)
	}
}

// asGoInstall makes this process act as a client built with go install,
// until the test ends: it has no helpers of its own, and finds the
// release's in its cache, as an earlier fetch left them. It returns the
// version of the helper that the client loads from the cache, which the
// embedded helpers had too.
func asGoInstall(t *testing.T) string {
	t.Helper()
	if _, ok := helpers.For("linux", "arm64"); !ok {
		t.Skip("this build has no helper for linux/arm64 (run just helpers)")
	}
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	installed := client.HelperVersion()
	files := map[string][]byte{}
	for _, name := range []string{"host-linux-amd64", "host-linux-arm64", "vt-linux-amd64", "vt-linux-arm64"} {
		if b, ok := helpers.File(name); ok {
			files[name] = b
		}
	}
	t.Cleanup(helpers.HideEmbedded())
	if v := client.HelperVersion(); v != version.Version || v == installed {
		t.Fatalf("with no helpers, the version is %q", v)
	}
	cache := filepath.Join(client.CacheDir(), "helpers", version.Version)
	if err := os.MkdirAll(cache, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, b := range files {
		if err := os.WriteFile(filepath.Join(cache, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(brand.Env("RELEASES_URL"), "http://127.0.0.1:9/never-fetched")
	return installed
}

// changesOnTheHost lists the commands that installed or pruned a helper,
// or probed for an install, on the fake host.
func changesOnTheHost(remotes []string) []string {
	var out []string
	for _, r := range remotes {
		if strings.Contains(r, "__INSTALLED__") || strings.Contains(r, " prune") || strings.Contains(r, "uname -sm") {
			out = append(out, r)
		}
	}
	return out
}

// downTransport is a host that does not answer.
type downTransport struct{}

func (downTransport) Start(context.Context, string) (client.Proc, error) {
	return nil, errors.New("connection refused")
}

func (downTransport) Run(context.Context, string, io.Reader) ([]byte, error) {
	return nil, errors.New("connection refused")
}

func (downTransport) ReachHint() string { return "check the fake" }

// A host that does not answer fails with exit code 1.
func TestHostUpgradeOfAHostThatDoesNotAnswer(t *testing.T) {
	m, _ := withFake(t, nil)
	m.T = downTransport{}
	if err := saveHost(client.Host{Name: "box", Machine: &client.Machine{Provider: "fake", ID: "fake-box"}}); err != nil {
		t.Fatal(err)
	}
	out, errOut, code := mainRun(t, "host", "upgrade", "box")
	if code != 1 || out != "" || !strings.Contains(errOut, "connection refused") || !strings.Contains(errOut, "Try: ") {
		t.Errorf("exit %d\nstdout %q\nstderr %q", code, out, errOut)
	}
	if err := installError("box", errors.New("no space left on device")); !strings.Contains(err.Error(), "could not install the helper on box: no space left on device") {
		t.Errorf("installError = %v", err)
	}
}

// The check dial after the install never installs again, and never
// prunes: when the helper is not there after all, upgrade fails, and says
// that it did not start (cli-spec 11.4, rule 4).
func TestHostUpgradeWhenTheHelperDoesNotStart(t *testing.T) {
	if _, ok := helpers.For("linux", "arm64"); !ok {
		t.Skip("this build has no helper for linux/arm64 (run just helpers)")
	}
	_, tr := withFake(t, nil)
	tr.NoHelper, tr.Helper = true, "an-other-version"
	box := client.Host{Name: "box", Machine: &client.Machine{Provider: "fake", ID: "fake-box"}}
	if err := saveHost(box); err != nil {
		t.Fatal(err)
	}
	_, err := upgradeHost(context.Background(), box)
	var ue *client.UserError
	if !errors.As(err, &ue) || ue.Code != "helper_failed" || !strings.Contains(ue.Msg, "installed the helper "+client.HelperVersion()+" on box, but it did not start") ||
		ue.Hint != brand.Name+" host doctor box" {
		t.Fatalf("err = %+v", err)
	}
	installs := 0
	for _, r := range tr.Remotes() {
		if strings.Contains(r, " prune") {
			t.Errorf("host upgrade pruned: %q", r)
		}
		if strings.Contains(r, "__INSTALLED__") {
			installs++
		}
	}
	if installs == 0 {
		t.Error("host upgrade did not install the helper")
	}
}
