package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/client/helpers"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
)

func TestConfigCheckImagesFetchSetting(t *testing.T) {
	hosttest.New(t)
	writeConfigFiles(t, "[images]\nfetch = \"peer\"\n", "")
	out, _, code := runConfigCheckMain(t, "config", "check", "--json")
	if code != 1 {
		t.Fatalf("invalid images.fetch code = %d, want 1\n%s", code, out)
	}
	var rep configReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range rep.Findings {
		if f.Code == "bad_value" && f.Line == 2 && strings.Contains(f.Message, "images.fetch") {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing images.fetch finding in %+v", rep.Findings)
	}

	for _, valid := range []string{"auto", "host", "laptop"} {
		writeConfigFiles(t, "[images]\nfetch = \""+valid+"\"\n", "")
		out, _, code = runConfigCheckMain(t, "config", "check", "--json")
		if code != 0 {
			t.Fatalf("valid images.fetch=%s failed: code=%d\n%s", valid, code, out)
		}
	}
}

func TestHostImagesFlagValidation(t *testing.T) {
	newTestHost(t)
	if got, code := lineErr(t, "host", "add", "mybox", "--images", "peer"); code != 2 || !strings.Contains(got, "--images") || strings.Count(got, "Try:") != 1 {
		t.Fatalf("host add --images peer: exit %d\n%s", code, got)
	}
	if got, code := lineErr(t, "host", "upgrade", "box", "--images", "peer"); code != 2 || !strings.Contains(got, "--images") || strings.Count(got, "Try:") != 1 {
		t.Fatalf("host upgrade --images peer: exit %d\n%s", code, got)
	}
}

func TestHostImagesFlagCompletion(t *testing.T) {
	completionFixture(t)
	for _, args := range [][]string{
		{"host", "add", "--images", ""},
		{"host", "upgrade", "atlas", "--images", ""},
	} {
		out, errOut, err := runApart(t, append([]string{cobra.ShellCompRequestCmd}, args...)...)
		if err != nil {
			t.Fatalf("__complete %v: %v\nstderr: %s", args, err, errOut)
		}
		if !isQuietCompStderr(errOut) {
			t.Errorf("__complete %v unexpected stderr: %q", args, errOut)
		}
		for _, want := range []string{"auto\t", "host\t", "laptop\t"} {
			if !strings.Contains(out, want) {
				t.Errorf("__complete %v missing %q in:\n%s", args, want, out)
			}
		}
		if !strings.Contains(out, ":4\n") {
			t.Errorf("__complete %v missing NoFileComp directive (:4) in:\n%s", args, out)
		}
	}
}

func TestHostUpgradeInstallsMissingRuntimeFiles(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("embedded linux helper test requires Linux")
	}
	bin, ok := helpers.For("linux", runtime.GOARCH)
	if !ok || len(bin) == 0 {
		t.Skip("this build has no embedded helper for linux/" + runtime.GOARCH)
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	// Pre-populate the laptop runtime cache so laptop mode installs over the
	// local shell transport without needing external HTTPS or test hooks in client.
	kernelBytes := bytes.Repeat([]byte("K"), 2048)
	vmmBytes := bytes.Repeat([]byte("V"), 1024)
	kSum := sha256.Sum256(kernelBytes)
	vSum := sha256.Sum256(vmmBytes)
	kHex := hex.EncodeToString(kSum[:])
	vHex := hex.EncodeToString(vSum[:])

	if err := os.MkdirAll(client.RuntimeCacheDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(client.RuntimeCacheDir(), kHex), kernelBytes, 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(client.RuntimeCacheDir(), vHex), vmmBytes, 0o444); err != nil {
		t.Fatal(err)
	}

	// Serve a dummy URL (unused because laptop cache already holds verified bytes).
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	defer srv.Close()

	oldManifest := runtimeManifestFunc
	runtimeManifestFunc = func() api.RuntimeManifest {
		return api.RuntimeManifest{
			Release: "0.9.0",
			Runtime: "2026.11.0",
			Items: []api.RuntimeAsset{
				{Kind: "kernel", Name: "guest", Arch: runtime.GOARCH, SHA256: kHex, Size: int64(len(kernelBytes)), URL: srv.URL + "/guest"},
				{Kind: "vmm", Name: "cloud-hypervisor", Arch: runtime.GOARCH, SHA256: vHex, Size: int64(len(vmmBytes)), URL: srv.URL + "/vmm"},
			},
		}
	}
	t.Cleanup(func() { runtimeManifestFunc = oldManifest })

	home := t.TempDir()
	// Ensure host helper doesn't share the test process's XDG_DATA_HOME/XDG_STATE_HOME.
	oldProviders := providers
	client.RegisterProvider("rtsh", func(client.Host) (client.Lifecycle, error) {
		return &isolatedShellLifecycle{home: home}, nil
	})
	t.Cleanup(func() { providers = oldProviders })

	ctx := context.Background()
	h := client.Host{Name: "rtbox", Machine: &client.Machine{Provider: "rtsh", ID: "rt-1"}}
	if err := saveHost(ctx, h); err != nil {
		t.Fatal(err)
	}

	// First host upgrade installs both the helper and the missing runtime files.
	out, errOut, err := runApart(t, "host", "upgrade", "rtbox", "--images", "laptop", "--json")
	if err != nil {
		t.Fatalf("host upgrade with runtime: %v\nstderr: %s", err, errOut)
	}
	if errOut != "" {
		t.Errorf("host upgrade --json wrote stderr: %q", errOut)
	}
	checkJSON(t, "host upgrade runtime --json", out, false)
	var res struct {
		Host         string                     `json:"host"`
		Outcome      string                     `json:"outcome"`
		RuntimeLabel string                     `json:"runtime_label"`
		Runtime      []api.RuntimeInstalledFile `json:"runtime"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err)
	}
	if res.Outcome != "installed" || res.RuntimeLabel != "2026.11.0" || len(res.Runtime) != 2 {
		t.Fatalf("host upgrade --json = %+v", res)
	}

	artDir := filepath.Join(home, ".local", "share", brand.Name, "artefacts", "sha256")
	if fi, err := os.Stat(filepath.Join(artDir, kHex)); err != nil || fi.Mode().Perm() != 0o444 {
		t.Fatalf("kernel on host = %v, err=%v", fi, err)
	}
	if fi, err := os.Stat(filepath.Join(artDir, vHex)); err != nil || fi.Mode().Perm() != 0o555 {
		t.Fatalf("vmm on host = %v, err=%v", fi, err)
	}

	// Second host upgrade sees helper and runtime both present.
	out2, errOut2, err := runApart(t, "host", "upgrade", "rtbox", "--images", "laptop")
	if err != nil {
		t.Fatalf("second host upgrade: %v\nstdout: %s\nstderr: %s", err, out2, errOut2)
	}
	if !strings.Contains(errOut2, "already") {
		t.Errorf("second host upgrade stderr = %q", errOut2)
	}

	// Removing one runtime file exercises the singular count message when the
	// helper is already present.
	if err := os.Remove(filepath.Join(artDir, kHex)); err != nil {
		t.Fatal(err)
	}
	_, errOut3, err := runApart(t, "host", "upgrade", "rtbox", "--images", "laptop")
	if err != nil {
		t.Fatalf("third host upgrade: %v\nstderr: %s", err, errOut3)
	}
	if !strings.Contains(errOut3, "installed 1 runtime file.") || strings.Contains(errOut3, "1 runtime files") {
		t.Errorf("third host upgrade stderr = %q, want singular 'installed 1 runtime file.'", errOut3)
	}
}

type isolatedShellLifecycle struct{ home string }

func (isolatedShellLifecycle) Status(context.Context) (string, error) {
	return client.MachineRunning, nil
}
func (isolatedShellLifecycle) Start(context.Context) error   { return nil }
func (isolatedShellLifecycle) Stop(context.Context) error    { return nil }
func (isolatedShellLifecycle) Destroy(context.Context) error { return nil }
func (l isolatedShellLifecycle) Transport(context.Context, bool) (client.Transport, error) {
	return &isolatedShellTransport{localShellTransport: localShellTransport(l)}, nil
}

type isolatedShellTransport struct {
	localShellTransport
}

func (s *isolatedShellTransport) cleanEnv() []string {
	var env []string
	for _, e := range s.env() {
		if strings.HasPrefix(e, "XDG_DATA_HOME=") || strings.HasPrefix(e, "XDG_STATE_HOME=") || strings.HasPrefix(e, "XDG_CONFIG_HOME=") || strings.HasPrefix(e, "XDG_CACHE_HOME=") {
			continue
		}
		env = append(env, e)
	}
	return env
}

func (s *isolatedShellTransport) Start(ctx context.Context, remote string) (client.Proc, error) {
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", remote)
	cmd.Env = s.cleanEnv()
	return client.CommandProc(cmd, nil)
}

func (s *isolatedShellTransport) Run(ctx context.Context, remote string, stdin io.Reader) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", remote)
	cmd.Env = s.cleanEnv()
	cmd.Stdin = stdin
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return out.Bytes(), err
	}
	return out.Bytes(), nil
}
