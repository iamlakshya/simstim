package client

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestCancelledForgetKeepsItsConfig(t *testing.T) {
	ownLockFiles(t, time.Second, time.Minute)
	box := Host{Name: "box", SSH: "box"}
	if err := SaveHosts(context.Background(), []Host{box}); err != nil {
		t.Fatal(err)
	}
	path, err := writeWrapper(box)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	old := beforeHostsSave
	beforeHostsSave = cancel
	t.Cleanup(func() { beforeHostsSave = old })
	if err := UpdateHosts(ctx, HostChange{Kind: "host rm", Was: &box}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled forget: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("cancelled forget removed its config: %v", err)
	}
	if _, err := FindHost(box.Name); err != nil {
		t.Errorf("cancelled forget removed its saved entry: %v", err)
	}
}

func TestForgetPreservesAnotherHostsConfigAlias(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "current", true: "legacy"}[legacy], func(t *testing.T) {
			ownLockFiles(t, time.Second, time.Minute)
			box := Host{Name: "box", SSH: "box"}
			path, err := writeWrapper(box)
			if err != nil {
				t.Fatal(err)
			}
			if legacy {
				old := oldWrapperPath(box.Name)
				if err := os.Rename(path, old); err != nil {
					t.Fatal(err)
				}
				path = old
			}
			alias := filepath.Join(t.TempDir(), "config")
			if err := os.Link(path, alias); err != nil {
				t.Fatal(err)
			}
			other := Host{Name: "other", SSH: "other", SSHConfig: alias}
			if err := SaveHosts(context.Background(), []Host{box, other}); err != nil {
				t.Fatal(err)
			}
			if err := UpdateHosts(context.Background(), HostChange{Kind: "host rm", Was: &box}); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(path); err != nil {
				t.Errorf("forgot a config that another host uses: %v", err)
			}
		})
	}
}

func TestDoctorPreservesConfigAliases(t *testing.T) {
	for _, kind := range []string{"hard link", "linked folder", "letter case"} {
		t.Run(kind, func(t *testing.T) {
			ownLockFiles(t, time.Second, time.Minute)
			path, err := writeWrapper(Host{Name: "gone", SSH: "gone"})
			if err != nil {
				t.Fatal(err)
			}
			alias := filepath.Join(t.TempDir(), "config")
			switch kind {
			case "hard link":
				err = os.Link(path, alias)
			case "linked folder":
				err = os.Symlink(filepath.Dir(path), alias)
				alias = filepath.Join(alias, filepath.Base(path))
			case "letter case":
				alias = filepath.Join(filepath.Dir(path), "CONFIG-GONE-"+filepath.Base(path)[len("config-gone-"):])
				if _, err := os.Stat(alias); err != nil {
					t.Skip("the file system distinguishes letter case")
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := SaveHosts(context.Background(), []Host{{Name: "kept", SSH: "kept", SSHConfig: alias}}); err != nil {
				t.Fatal(err)
			}
			if fixes := sshConfigFix(); len(fixes) != 0 {
				t.Errorf("doctor would remove a config that is used through %s: %+v", kind, fixes)
			}
		})
	}
}

func TestForgetPreservesAnUnownedCurrentConfig(t *testing.T) {
	ownLockFiles(t, time.Second, time.Minute)
	box := Host{Name: "box", SSH: "box"}
	path, err := writeWrapper(box)
	if err != nil {
		t.Fatal(err)
	}
	const text = "Host box\n    HostName 192.0.2.1\n"
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SaveHosts(context.Background(), []Host{box}); err != nil {
		t.Fatal(err)
	}
	if err := UpdateHosts(context.Background(), HostChange{Kind: "host rm", Was: &box}); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != text {
		t.Errorf("forget changed an unowned config: %q, %v", data, err)
	}
}

func TestWindowsHomeConfigPath(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows home path expansion")
	}
	t.Setenv("USERPROFILE", t.TempDir())
	if got, want := expand(`~\ssh\config`), filepath.Join(home(), "ssh", "config"); got != want {
		t.Errorf("home config path = %q, want %q", got, want)
	}
}
