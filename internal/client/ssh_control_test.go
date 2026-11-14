//go:build !windows

package client

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/testhome"
)

// The full temporary bind budget includes the epoch, nf prefix, route
// hash and OpenSSH suffix. A test owns both candidate locations.
func TestControlDirFitsASocketPath(t *testing.T) {
	sshDirs(t)
	short, err := os.MkdirTemp(testhome.Root(), "c")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(short) })
	t.Setenv("XDG_CACHE_HOME", short)
	want := filepath.Join(short, brand.Name, "cm")
	if len(want)+1+26+1+3+40+17 > 103 {
		want = controlFallbackDir(uint32(os.Getuid()))
	}
	if got := controlDir(); got != want {
		t.Errorf("short cache: %q, want %q", got, want)
	}
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), strings.Repeat("x", 80)))
	d := controlDir()
	if want := controlFallbackDir(uint32(os.Getuid())); d != want {
		t.Errorf("long cache: %q, want owned fallback %q", d, want)
	}
	if !controlFits(d) || !privateDir(d) {
		t.Errorf("unsafe or over-budget control base %q", d)
	}
}

// The historical runtime test now covers the actual H3 fallback base.
// Unsafe permissions, links and foreign ownership refuse sharing.
func TestControlDirWantsASafeRuntimeFolder(t *testing.T) {
	sshDirs(t)
	base := controlFallbackDir(uint32(os.Getuid()))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), strings.Repeat("x", 80)))
	other := t.TempDir()
	for _, c := range []struct {
		name    string
		make    func() error
		foreign bool
		safe    bool
	}{
		{"private", func() error { return os.Mkdir(base, 0700) }, false, true},
		{"world-writable", func() error { return os.Mkdir(base, 0777) }, false, false},
		{"group-writable", func() error { return os.Mkdir(base, 0770) }, false, false},
		{"world-writable without runtime", func() error { t.Setenv("XDG_RUNTIME_DIR", ""); return os.Mkdir(base, 0777) }, false, false},
		{"a link", func() error { return os.Symlink(other, base) }, false, false},
		{"another user's", func() error { return os.Mkdir(base, 0700) }, true, false},
	} {
		_ = os.RemoveAll(base)
		if err := c.make(); err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(c.name, "world-writable") {
			if err := os.Chmod(base, 0777); err != nil {
				t.Fatal(err)
			}
		}
		if c.name == "group-writable" {
			if err := os.Chmod(base, 0770); err != nil {
				t.Fatal(err)
			}
		}
		owner := ownerOf
		if c.foreign {
			ownerOf = func(info os.FileInfo) int {
				if info.Name() == filepath.Base(base) {
					return os.Getuid() + 1
				}
				return owner(info)
			}
		}
		got := controlDir()
		ownerOf = owner
		want := ""
		if c.safe {
			want = base
		}
		if got != want {
			t.Errorf("%s base: %q, want %q", c.name, got, want)
		}
		if entries, err := os.ReadDir(other); err != nil || len(entries) != 0 {
			t.Errorf("followed a link into another directory: %v %v", entries, err)
		}
	}
}

func TestPrivateDir(t *testing.T) {
	d := t.TempDir()
	open := filepath.Join(d, "open")
	if err := os.Mkdir(open, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(open, 0o755); err != nil {
		t.Fatal(err)
	}
	if privateDir(open) {
		t.Error("a folder others can read is private")
	}
	link := filepath.Join(d, "link")
	priv := filepath.Join(d, "priv")
	if err := os.Mkdir(priv, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(priv, link); err != nil {
		t.Fatal(err)
	}
	if !privateDir(priv) || privateDir(link) {
		t.Errorf("private %v, link %v", privateDir(priv), privateDir(link))
	}
}

// The e2e harness gives every client process its own HOME and XDG folders on
// the laptop, with an Include of the user's ~/.ssh/config in that HOME.
// OpenSSH resolves ~ through the password database (pw_dir), not HOME, so
// ssh -G still finds the user's keys, known_hosts and host aliases without
// reading the user's client config.
func TestWrapperWithIsolatedHomeKeepsPasswordDBSSH(t *testing.T) {
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("ssh is not installed")
	}
	u, err := user.Current()
	if err != nil || u.HomeDir == "" {
		t.Skipf("user.Current: %v", err)
	}
	root := t.TempDir()
	homeDir := filepath.Join(root, "home")
	t.Setenv("HOME", homeDir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	if err := os.MkdirAll(filepath.Join(homeDir, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	userSSH := filepath.Join(root, "user-ssh-config")
	body := "Host e2e-alias\n    HostName 198.51.100.7\n    Port 2222\n    IdentityFile ~/.ssh/id_e2e_probe\n"
	if err := os.WriteFile(userSSH, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	inc := fmt.Sprintf("Include %q\n", userSSH)
	if err := os.WriteFile(filepath.Join(homeDir, ".ssh", "config"), []byte(inc), 0o600); err != nil {
		t.Fatal(err)
	}
	wrapper, err := writeWrapper(Host{Name: "box", SSH: "e2e-alias"})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("ssh", "-G", "-F", wrapper, "e2e-alias")
	cmd.Env = append(os.Environ(), "HOME="+homeDir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("ssh -G failed: %v\n%s", err, out)
	}
	got := string(out)
	for _, want := range []string{
		"hostname 198.51.100.7\n",
		"port 2222\n",
		"identityfile ~/.ssh/id_e2e_probe\n",
		"userknownhostsfile " + filepath.Join(u.HomeDir, ".ssh", "known_hosts"),
	} {
		if !strings.Contains(got, want) {
			t.Errorf("ssh -G output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, homeDir) {
		t.Errorf("ssh -G substituted the isolated HOME %q instead of pw_dir %q:\n%s", homeDir, u.HomeDir, got)
	}
}

func TestRetireMasterStates(t *testing.T) {
	sshDirs(t)
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "cfg"))
	t.Setenv("XDG_CACHE_HOME", shortDir(t))
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	stateFile := filepath.Join(dir, "master.state")
	modeFile := filepath.Join(dir, "master.mode")
	sshBin := filepath.Join(binDir, "ssh")
	script := `#!/bin/sh
mode=$(cat "` + modeFile + `" 2>/dev/null || echo normal)
case " $* " in
  *" -O exit "*)
    case "$mode" in
      hang)
        sleep 5
        exit 0
        ;;
      denied)
        echo "Control socket connect(/tmp/cm): Operation not permitted" >&2
        exit 255
        ;;
      refused)
        echo "Control socket connect(/tmp/cm): Connection refused" >&2
        exit 255
        ;;
      ignore)
        echo "Exit request sent." >&2
        exit 0
        ;;
      *)
        if [ -f "` + stateFile + `" ]; then
          rm -f "` + stateFile + `"
          echo "Exit request sent." >&2
          exit 0
        fi
        echo "Control socket connect(/tmp/cm): No such file or directory" >&2
        exit 255
        ;;
    esac
    ;;
  *" -O check "*)
    if [ -f "` + stateFile + `" ]; then
      echo "Master running (pid=4242)" >&2
      exit 0
    fi
    echo "Control socket connect(/tmp/cm): No such file or directory" >&2
    exit 255
    ;;
esac
exit 0
`
	if err := os.WriteFile(sshBin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	s := &SSH{Host: Host{Name: "box", SSH: "box"}}
	pinFor(t, s.Host.Name)

	// 1. Pre-cancelled context returns immediately without running ssh.
	cctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.RetireMaster(cctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled RetireMaster = %v, want context.Canceled", err)
	}

	// 2. Absent master socket is a clean no-op.
	if err := s.RetireMaster(context.Background()); err != nil {
		t.Fatalf("absent master: %v", err)
	}

	// 3. Stale refused socket is also treated as no running master.
	_ = os.WriteFile(modeFile, []byte("refused"), 0o600)
	if err := s.RetireMaster(context.Background()); err != nil {
		t.Fatalf("refused stale socket: %v", err)
	}

	// 4. Active master is stopped and verified gone.
	_ = os.WriteFile(modeFile, []byte("normal"), 0o600)
	_ = os.WriteFile(stateFile, []byte("1"), 0o600)
	if err := s.RetireMaster(context.Background()); err != nil {
		t.Fatalf("active master retire: %v", err)
	}
	if _, err := os.Stat(stateFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("active master state file was not removed")
	}

	// 5. Permission failure on -O exit fails with reconnect_failed and a hint.
	_ = os.WriteFile(stateFile, []byte("1"), 0o600)
	_ = os.WriteFile(modeFile, []byte("denied"), 0o600)
	err := s.RetireMaster(context.Background())
	var ue *UserError
	if !errors.As(err, &ue) || ue.Code != "reconnect_failed" || !strings.HasPrefix(ue.Hint, "ssh ") || !strings.Contains(ue.Hint, "ControlPath=") {
		t.Fatalf("denied master = %#v (%v)", ue, err)
	}

	// 6. Master that ignores -O exit and stays alive fails with reconnect_failed.
	restore := SetMasterExitTimeout(80 * time.Millisecond)
	defer restore()
	_ = os.WriteFile(modeFile, []byte("ignore"), 0o600)
	err = s.RetireMaster(context.Background())
	if !errors.As(err, &ue) || ue.Code != "reconnect_failed" || !strings.Contains(ue.Msg, "still running") {
		t.Fatalf("ignoring master = %#v (%v)", ue, err)
	}

	// 7. Hung -O exit times out and fails with reconnect_failed.
	_ = os.WriteFile(modeFile, []byte("hang"), 0o600)
	t0 := time.Now()
	err = s.RetireMaster(context.Background())
	if !errors.As(err, &ue) || ue.Code != "reconnect_failed" || !strings.Contains(ue.Msg, "timed out") {
		t.Fatalf("hung master = %#v (%v)", ue, err)
	}
	if time.Since(t0) > 2*time.Second {
		t.Errorf("hung RetireMaster took %v", time.Since(t0))
	}
}
