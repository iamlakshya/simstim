package process

import (
	"context"
	"errors"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/wall"
)

type fakeFileInfo struct{}

func (fakeFileInfo) Name() string       { return "f" }
func (fakeFileInfo) Size() int64        { return 0 }
func (fakeFileInfo) Mode() os.FileMode  { return 0o644 }
func (fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (fakeFileInfo) IsDir() bool        { return false }
func (fakeFileInfo) Sys() any           { return nil }

func baseDeps(files map[string]string, bins map[string]string, run func(name string, args []string) ([]byte, error)) ProbeDeps {
	return ProbeDeps{
		GOOS:          "linux",
		KernelRelease: func() string { return "6.12.118+deb13-cloud-amd64" },
		ReadFile: func(p string) ([]byte, error) {
			v, ok := files[p]
			if !ok {
				return nil, os.ErrNotExist
			}
			return []byte(v), nil
		},
		Stat: func(p string) (os.FileInfo, error) {
			if _, ok := files[p]; ok {
				return fakeFileInfo{}, nil
			}
			return nil, os.ErrNotExist
		},
		LookPath: func(name string) (string, error) {
			if p, ok := bins[name]; ok {
				return p, nil
			}
			return "", errors.New("not found")
		},
		LandlockABI: func() int { return 6 },
		SeccompOK:   func() bool { return true },
		RunCmd: func(_ context.Context, name string, args ...string) ([]byte, error) {
			return run(name, args)
		},
	}
}

func TestProbeWithScenarios(t *testing.T) {
	ctx := context.Background()

	t.Run("debian 13 with working bwrap", func(t *testing.T) {
		d := baseDeps(
			map[string]string{
				etcOSRelease:  "ID=debian\nVERSION_ID=\"13\"\n",
				procMaxUserNS: "15588\n",
			},
			map[string]string{"bwrap": "/usr/bin/bwrap", "true": "/usr/bin/true"},
			func(name string, args []string) ([]byte, error) {
				if name == "/usr/bin/bwrap" && len(args) == 1 && args[0] == "--version" {
					return []byte("bubblewrap 0.11.0\n"), nil
				}
				return nil, nil
			},
		)
		p := ProbeWith(ctx, d)
		if !p.OK || p.Level != wall.Process || p.Version != "0.11.0" {
			t.Fatalf("probe = %+v, want OK with version 0.11.0", p)
		}
		if p.Facts["userns"] != "true" || p.Facts["landlock_abi"] != "6" || p.Facts["seccomp"] != "true" {
			t.Errorf("facts = %+v", p.Facts)
		}
	})

	t.Run("debian 13 without bwrap", func(t *testing.T) {
		d := baseDeps(
			map[string]string{
				etcOSRelease:  "ID=debian\nVERSION_ID=\"13\"\n",
				procMaxUserNS: "15588\n",
			},
			map[string]string{"unshare": "/usr/bin/unshare"},
			func(string, []string) ([]byte, error) { return nil, nil },
		)
		p := ProbeWith(ctx, d)
		if p.OK {
			t.Fatalf("probe OK without bwrap: %+v", p)
		}
		if !strings.Contains(p.Reason, "bubblewrap") || p.Fix != "sudo apt install bubblewrap" {
			t.Errorf("reason=%q fix=%q", p.Reason, p.Fix)
		}
		if p.Facts["userns"] != "true" {
			t.Errorf("expected direct userns check to pass: %+v", p.Facts)
		}
	})

	t.Run("ubuntu 24.04 apparmor blocks bwrap without profile", func(t *testing.T) {
		d := baseDeps(
			map[string]string{
				etcOSRelease:       "ID=ubuntu\nVERSION_ID=\"24.04\"\n",
				procMaxUserNS:      "31176\n",
				procAppArmorUserNS: "1\n",
			},
			map[string]string{"bwrap": "/usr/bin/bwrap", "true": "/usr/bin/true"},
			func(_ string, args []string) ([]byte, error) {
				if len(args) == 1 && args[0] == "--version" {
					return []byte("bubblewrap 0.9.0\n"), nil
				}
				return []byte("bwrap: setting up uid map: Permission denied\n"), errors.New("exit status 1")
			},
		)
		p := ProbeWith(ctx, d)
		if p.OK {
			t.Fatalf("probe should fail under Ubuntu AppArmor without profile: %+v", p)
		}
		if p.Facts["apparmor_userns"] != "true" || p.Facts["bwrap_apparmor"] != "blocked" {
			t.Errorf("facts = %+v", p.Facts)
		}
		if !strings.Contains(p.Reason, "AppArmor") || !strings.Contains(p.Fix, "apparmor-profiles") || !strings.Contains(p.Fix, "bwrap-userns-restrict") {
			t.Errorf("reason=%q fix=%q", p.Reason, p.Fix)
		}
	})

	t.Run("ubuntu 24.04 plain bwrap profile on disk points apparmor_parser to it", func(t *testing.T) {
		d := baseDeps(
			map[string]string{
				etcOSRelease:       "ID=ubuntu\nVERSION_ID=\"24.04\"\n",
				procMaxUserNS:      "31176\n",
				procAppArmorUserNS: "1\n",
				appArmorBwrapPlain: "profile bwrap ...",
			},
			map[string]string{"bwrap": "/usr/bin/bwrap", "true": "/usr/bin/true"},
			func(_ string, args []string) ([]byte, error) {
				if len(args) == 1 && args[0] == "--version" {
					return []byte("bubblewrap 0.9.0\n"), nil
				}
				return []byte("bwrap: setting up uid map: Permission denied\n"), errors.New("exit status 1")
			},
		)
		p := ProbeWith(ctx, d)
		wantFix := "sudo apparmor_parser -r " + appArmorBwrapPlain
		if p.OK || p.Fix != wantFix {
			t.Errorf("probe = %+v, want Fix=%q", p, wantFix)
		}
	})

	t.Run("ubuntu 24.04 extra profile present on disk", func(t *testing.T) {
		d := baseDeps(
			map[string]string{
				etcOSRelease:       "ID=ubuntu\nVERSION_ID=\"24.04\"\n",
				procMaxUserNS:      "31176\n",
				procAppArmorUserNS: "1\n",
				appArmorBwrapExtra: "profile bwrap ...",
			},
			map[string]string{"bwrap": "/usr/bin/bwrap", "true": "/usr/bin/true"},
			func(_ string, args []string) ([]byte, error) {
				if len(args) == 1 && args[0] == "--version" {
					return []byte("bubblewrap 0.9.0\n"), nil
				}
				return []byte("bwrap: Creating new namespace failed: Permission denied\n"), errors.New("exit status 1")
			},
		)
		p := ProbeWith(ctx, d)
		if p.OK || !strings.HasPrefix(p.Fix, "sudo cp "+appArmorBwrapExtra) {
			t.Errorf("probe = %+v", p)
		}
	})

	t.Run("ubuntu 24.04 missing bwrap suggests bubblewrap and apparmor-profiles", func(t *testing.T) {
		d := baseDeps(
			map[string]string{
				etcOSRelease:       "ID=ubuntu\nVERSION_ID=\"24.04\"\n",
				procMaxUserNS:      "31176\n",
				procAppArmorUserNS: "1\n",
			},
			map[string]string{"unshare": "/usr/bin/unshare"},
			func(string, []string) ([]byte, error) { return nil, errors.New("EPERM") },
		)
		p := ProbeWith(ctx, d)
		if p.OK || !strings.Contains(p.Fix, "sudo apt install bubblewrap apparmor-profiles") {
			t.Errorf("probe = %+v", p)
		}
	})

	t.Run("ubuntu 25.04 with loaded bwrap-userns-restrict succeeds", func(t *testing.T) {
		d := baseDeps(
			map[string]string{
				etcOSRelease:           "ID=ubuntu\nVERSION_ID=\"25.04\"\n",
				procMaxUserNS:          "31176\n",
				procAppArmorUserNS:     "1\n",
				procAppArmorUnconfined: "1\n",
				appArmorBwrapRestrict:  "profile bwrap ...",
			},
			map[string]string{"bwrap": "/usr/bin/bwrap", "true": "/usr/bin/true"},
			func(_ string, args []string) ([]byte, error) {
				if len(args) == 1 && args[0] == "--version" {
					return []byte("bubblewrap 0.11.0\n"), nil
				}
				return nil, nil
			},
		)
		p := ProbeWith(ctx, d)
		if !p.OK || p.Facts["apparmor_userns"] != "true" || p.Facts["bwrap_apparmor"] != "allowed" {
			t.Fatalf("probe = %+v", p)
		}
	})

	t.Run("old kernel rejected", func(t *testing.T) {
		d := baseDeps(nil, nil, func(string, []string) ([]byte, error) { return nil, nil })
		d.KernelRelease = func() string { return "5.10.0-28-amd64" }
		p := ProbeWith(ctx, d)
		if p.OK || !strings.Contains(p.Reason, "5.13") {
			t.Errorf("probe = %+v", p)
		}
	})

	t.Run("no landlock rejected", func(t *testing.T) {
		d := baseDeps(nil, map[string]string{"bwrap": "/usr/bin/bwrap"}, func(string, []string) ([]byte, error) { return nil, nil })
		d.LandlockABI = func() int { return 0 }
		p := ProbeWith(ctx, d)
		if p.OK || !strings.Contains(p.Reason, "Landlock") {
			t.Errorf("probe = %+v", p)
		}
	})

	t.Run("no seccomp rejected", func(t *testing.T) {
		d := baseDeps(nil, map[string]string{"bwrap": "/usr/bin/bwrap"}, func(string, []string) ([]byte, error) { return nil, nil })
		d.SeccompOK = func() bool { return false }
		p := ProbeWith(ctx, d)
		if p.OK || !strings.Contains(p.Reason, "seccomp") {
			t.Errorf("probe = %+v", p)
		}
	})

	t.Run("max_user_namespaces zero", func(t *testing.T) {
		d := baseDeps(
			map[string]string{procMaxUserNS: "0\n"},
			map[string]string{"bwrap": "/usr/bin/bwrap"},
			func(string, []string) ([]byte, error) { return nil, errors.New("EPERM") },
		)
		p := ProbeWith(ctx, d)
		if p.OK || !strings.Contains(p.Reason, "max_user_namespaces") {
			t.Errorf("probe = %+v", p)
		}
	})

	t.Run("unprivileged_userns_clone zero", func(t *testing.T) {
		d := baseDeps(
			map[string]string{procUnprivUserNSClone: "0\n"},
			map[string]string{"bwrap": "/usr/bin/bwrap"},
			func(string, []string) ([]byte, error) { return nil, errors.New("EPERM") },
		)
		p := ProbeWith(ctx, d)
		if p.OK || !strings.Contains(p.Reason, "unprivileged_userns_clone") {
			t.Errorf("probe = %+v", p)
		}
	})

	t.Run("passing bwrap smoke test decides over sysctl values", func(t *testing.T) {
		for name, files := range map[string]map[string]string{
			"max_user_namespaces=0":       {procMaxUserNS: "0\n"},
			"unprivileged_userns_clone=0": {procUnprivUserNSClone: "0\n"},
		} {
			t.Run(name, func(t *testing.T) {
				d := baseDeps(
					files,
					map[string]string{"bwrap": "/usr/bin/bwrap", "true": "/usr/bin/true"},
					func(_ string, args []string) ([]byte, error) {
						if len(args) == 1 && args[0] == "--version" {
							return []byte("bubblewrap 0.11.0\n"), nil
						}
						return nil, nil
					},
				)
				p := ProbeWith(ctx, d)
				if !p.OK || p.Facts["userns"] != "true" {
					t.Errorf("probe = %+v, want OK=true and facts[userns]=true", p)
				}
			})
		}
	})

	t.Run("docker container refuses unshare", func(t *testing.T) {
		d := baseDeps(
			map[string]string{etcOSRelease: "ID=debian\n", procMaxUserNS: "15588\n"},
			map[string]string{"bwrap": "/usr/bin/bwrap", "true": "/usr/bin/true"},
			func(_ string, args []string) ([]byte, error) {
				if len(args) == 1 && args[0] == "--version" {
					return []byte("bubblewrap 0.11.0\n"), nil
				}
				return []byte("bwrap: Creating new namespace failed: Operation not permitted\n"), errors.New("exit status 1")
			},
		)
		p := ProbeWith(ctx, d)
		if p.OK || !strings.Contains(p.Reason, "Operation not permitted") {
			t.Errorf("probe = %+v", p)
		}
	})

	t.Run("distro package hints", func(t *testing.T) {
		for id, want := range map[string]string{
			"fedora":   "sudo dnf install bubblewrap",
			"arch":     "sudo pacman -S bubblewrap",
			"alpine":   "sudo apk add bubblewrap",
			"opensuse": "sudo zypper install bubblewrap",
			"nixos":    "install the bubblewrap package",
		} {
			d := baseDeps(map[string]string{etcOSRelease: "ID=" + id + "\n"}, nil, func(string, []string) ([]byte, error) { return nil, errors.New("no") })
			p := ProbeWith(ctx, d)
			if p.Fix != want {
				t.Errorf("distro %s: fix = %q, want %q", id, p.Fix, want)
			}
		}
	})

	t.Run("non-linux host", func(t *testing.T) {
		d := baseDeps(nil, nil, nil)
		d.GOOS = "darwin"
		p := ProbeWith(ctx, d)
		if p.OK || !strings.Contains(p.Reason, "Linux") {
			t.Errorf("probe = %+v", p)
		}
	})
}

func TestLiveProcessProbe(t *testing.T) {
	p := Driver{}.Probe(context.Background())
	if p.Driver != DriverName || p.Level != wall.Process {
		t.Fatalf("probe header = %+v", p)
	}
	if runtime.GOOS == "linux" {
		if p.Facts["kernel"] == "" {
			t.Errorf("missing kernel fact: %+v", p.Facts)
		}
		if p.Facts["seccomp"] != "true" {
			t.Errorf("expected seccomp on Linux test runner: %+v", p.Facts)
		}
	}
}
