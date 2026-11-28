package jail

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

type fakeFileInfo struct {
	name string
	mode fs.FileMode
}

func (f fakeFileInfo) Name() string       { return f.name }
func (f fakeFileInfo) Size() int64        { return 0 }
func (f fakeFileInfo) Mode() fs.FileMode  { return f.mode }
func (f fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (f fakeFileInfo) IsDir() bool        { return f.mode.IsDir() }
func (f fakeFileInfo) Sys() any           { return nil }

func fakeFS(existing map[string]fs.FileMode, symlinks map[string]string) jailFS {
	return jailFS{
		lstat: func(p string) (fs.FileInfo, error) {
			if _, ok := symlinks[p]; ok {
				return fakeFileInfo{name: filepath.Base(p), mode: fs.ModeSymlink | 0o777}, nil
			}
			if m, ok := existing[p]; ok {
				return fakeFileInfo{name: filepath.Base(p), mode: m}, nil
			}
			return nil, fs.ErrNotExist
		},
		stat: func(p string) (fs.FileInfo, error) {
			if target, ok := symlinks[p]; ok {
				if !filepath.IsAbs(target) {
					target = filepath.Join(filepath.Dir(p), target)
				}
				p = target
			}
			if m, ok := existing[p]; ok {
				return fakeFileInfo{name: filepath.Base(p), mode: m}, nil
			}
			return nil, fs.ErrNotExist
		},
		readlink: func(p string) (string, error) {
			if t, ok := symlinks[p]; ok {
				return t, nil
			}
			return "", fs.ErrNotExist
		},
		evalSymlinks: func(p string) (string, error) {
			if t, ok := symlinks[p]; ok {
				return t, nil
			}
			return p, nil
		},
		lookPath: func(name string) (string, error) {
			if filepath.IsAbs(name) {
				return name, nil
			}
			return "/usr/bin/" + name, nil
		},
		homeDir: func() (string, error) { return "/home/user", nil },
		getenv: func(k string) string {
			if k == "XDG_RUNTIME_DIR" {
				return "/run/user/1000"
			}
			return ""
		},
	}
}

func TestValidateSpec(t *testing.T) {
	fsOps := fakeFS(map[string]fs.FileMode{"/usr": fs.ModeDir | 0o755}, nil)

	cases := []struct {
		name    string
		spec    Spec
		wantErr string
	}{
		{
			name:    "empty argv",
			spec:    Spec{},
			wantErr: "missing command argv",
		},
		{
			name:    "unknown profile",
			spec:    Spec{Argv: []string{"/usr/bin/true"}, Seccomp: Profile("bogus")},
			wantErr: "unknown seccomp profile",
		},
		{
			name:    "vmm refuses host net",
			spec:    Spec{Argv: []string{"/usr/bin/chv"}, Seccomp: ProfileVMM, Net: NetHost},
			wantErr: "cannot use host network",
		},
		{
			name:    "converter refuses host net",
			spec:    Spec{Argv: []string{"/usr/bin/conv"}, Seccomp: ProfileConverter, Net: NetHost},
			wantErr: "cannot use host network",
		},
		{
			name:    "relative dir",
			spec:    Spec{Argv: []string{"/usr/bin/true"}, Dir: "rel/dir"},
			wantErr: "working directory",
		},
		{
			name:    "relative bind src",
			spec:    Spec{Argv: []string{"/usr/bin/true"}, Binds: []Bind{{Src: "rel/file"}}},
			wantErr: "bind source",
		},
		{
			name:    "refuse root bind",
			spec:    Spec{Argv: []string{"/usr/bin/true"}, Binds: []Bind{{Src: "/"}}},
			wantErr: "refusing to expose /",
		},
		{
			name:    "refuse home bind",
			spec:    Spec{Argv: []string{"/usr/bin/true"}, Binds: []Bind{{Src: "/home/user"}}},
			wantErr: "host home folder",
		},
		{
			name:    "refuse xdg runtime dir bind",
			spec:    Spec{Argv: []string{"/usr/bin/true"}, Binds: []Bind{{Src: "/run/user/1000"}}},
			wantErr: "XDG_RUNTIME_DIR",
		},
		{
			name:    "refuse vhost-net bind",
			spec:    Spec{Argv: []string{"/usr/bin/true"}, Binds: []Bind{{Src: "/dev/vhost-net", Mode: BindDev}}},
			wantErr: "vhost device",
		},
		{
			name:    "refuse vhost-vsock rule",
			spec:    Spec{Argv: []string{"/usr/bin/true"}, Rules: []PathRule{{Path: "/dev/vhost-vsock", Write: true}}},
			wantErr: "vhost device",
		},
		{
			name:    "invalid env",
			spec:    Spec{Argv: []string{"/usr/bin/true"}, Env: []string{"NO_EQUALS"}},
			wantErr: "invalid environment entry",
		},
		{
			name: "allow specific subdir under home and runtime",
			spec: Spec{
				Argv: []string{"/opt/bin/vmm"},
				Binds: []Bind{
					{Src: "/home/user/.local/share/app/sessions/s1", Mode: BindRW},
					{Src: "/run/user/1000/app/v", Mode: BindRW},
					{Src: "/dev/kvm", Mode: BindDev},
				},
				Seccomp: ProfileVMM,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateSpec(tc.spec, fsOps)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("validateSpec() unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("validateSpec() = nil, want error containing %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("validateSpec() error = %q, want substring %q", err.Error(), tc.wantErr)
			}
			if !strings.Contains(err.Error(), "Try:") {
				t.Errorf("validateSpec() error missing Try: hint: %q", err.Error())
			}
		})
	}
}

func TestBuildBwrapArgsProfiles(t *testing.T) {
	fsOps := fakeFS(
		map[string]fs.FileMode{
			"/usr":                 fs.ModeDir | 0o755,
			"/usr/bin":             fs.ModeDir | 0o755,
			"/usr/lib":             fs.ModeDir | 0o755,
			"/usr/lib64":           fs.ModeDir | 0o755,
			"/etc/ld.so.cache":     0o644,
			"/etc/localtime":       0o644,
			"/etc/resolv.conf":     0o644,
			"/etc/hosts":           0o644,
			"/etc/ssl":             fs.ModeDir | 0o755,
			"/etc/ca-certificates": fs.ModeDir | 0o755,
		},
		map[string]string{
			"/bin":   "usr/bin",
			"/lib":   "usr/lib",
			"/lib64": "usr/lib64",
		},
	)

	t.Run("vmm jail keeps groups and isolates network", func(t *testing.T) {
		spec := VMMSpec(
			[]string{"/opt/vmm/cloud-hypervisor", "--api-socket", "path=/run/user/1000/app/v/s1.vmm"},
			[]string{"/opt/vmm/cloud-hypervisor", "/opt/vmm/vmlinux"},
			[]string{"/run/user/1000/app/v", "/home/user/.local/share/app/sessions/s1"},
		)
		args, err := buildBwrapArgsWithFS(spec, 3, true, fsOps)
		if err != nil {
			t.Fatalf("buildBwrapArgsWithFS: %v", err)
		}
		joined := strings.Join(args, " ")
		for _, want := range []string{
			"--unshare-user",
			"--unshare-ipc",
			"--unshare-pid",
			"--unshare-uts",
			"--unshare-cgroup-try",
			"--unshare-net",
			"--cap-drop ALL",
			"--die-with-parent",
			"--new-session",
			"--disable-userns",
			"--dev-bind /dev/kvm /dev/kvm",
			"--ro-bind /opt/vmm/cloud-hypervisor /opt/vmm/cloud-hypervisor",
			"--ro-bind /opt/vmm/vmlinux /opt/vmm/vmlinux",
			"--bind /run/user/1000/app/v /run/user/1000/app/v",
			"--clearenv",
			"--setenv HOME /tmp",
			"--seccomp 3",
			"-- /opt/vmm/cloud-hypervisor",
		} {
			if !strings.Contains(joined, want) {
				t.Errorf("bwrap args missing %q\nargs: %s", want, joined)
			}
		}
		// vmm jail must never map uid/gid or expose host DNS/TLS or host home.
		for _, forbid := range []string{"--uid", "--gid", "/etc/resolv.conf", "/home/user "} {
			if strings.Contains(joined, forbid) {
				t.Errorf("bwrap args should not contain %q\nargs: %s", forbid, joined)
			}
		}
	})

	t.Run("egress jail keeps host network and mounts TLS and DNS config", func(t *testing.T) {
		spec := EgressSpec([]string{"/home/user/.local/share/app/bin/helper", "egress", "s1"}, nil)
		args, err := buildBwrapArgsWithFS(spec, 4, false, fsOps)
		if err != nil {
			t.Fatalf("buildBwrapArgsWithFS: %v", err)
		}
		joined := strings.Join(args, " ")
		if slices.Contains(args, "--unshare-net") {
			t.Errorf("egress NetHost jail must not pass --unshare-net: %s", joined)
		}
		for _, want := range []string{
			"--ro-bind /etc/resolv.conf /etc/resolv.conf",
			"--ro-bind /etc/ssl /etc/ssl",
			"--ro-bind /home/user/.local/share/app/bin/helper /home/user/.local/share/app/bin/helper",
			"--seccomp 4",
		} {
			if !strings.Contains(joined, want) {
				t.Errorf("egress bwrap args missing %q\nargs: %s", want, joined)
			}
		}
	})

	t.Run("git jail binds repo and sets chdir", func(t *testing.T) {
		spec := GitSpec(
			[]string{"git", "bundle", "create", "/tmp/out/seed.bundle", "HEAD"},
			"/srv/repo",
			[]string{"/srv/repo"},
			[]string{"/tmp/out"},
		)
		args, err := buildBwrapArgsWithFS(spec, -1, false, fsOps)
		if err != nil {
			t.Fatalf("buildBwrapArgsWithFS: %v", err)
		}
		joined := strings.Join(args, " ")
		for _, want := range []string{
			"--unshare-net",
			"--ro-bind /srv/repo /srv/repo",
			"--bind /tmp/out /tmp/out",
			"--chdir /srv/repo",
		} {
			if !strings.Contains(joined, want) {
				t.Errorf("git bwrap args missing %q\nargs: %s", want, joined)
			}
		}
	})
}

func TestCheckNoUserNSLandlockABI(t *testing.T) {
	for _, abi := range []int{0, 1, 2} {
		err := checkNoUserNSLandlockABI(abi)
		if err == nil {
			t.Errorf("checkNoUserNSLandlockABI(%d) = nil, want refusal", abi)
			continue
		}
		if !strings.Contains(err.Error(), "Landlock ABI 3") || !strings.Contains(err.Error(), "Try:") {
			t.Errorf("checkNoUserNSLandlockABI(%d) = %q, want ABI 3 explanation and Try: hint", abi, err.Error())
		}
	}
	for _, abi := range []int{3, 4, 6} {
		if err := checkNoUserNSLandlockABI(abi); err != nil {
			t.Errorf("checkNoUserNSLandlockABI(%d) unexpected error: %v", abi, err)
		}
	}
}

func TestCheckNoUserNSBinds(t *testing.T) {
	if err := checkNoUserNSBinds([]Bind{{Src: "/srv/a"}, {Src: "/srv/b", Dest: "/srv/b"}}); err != nil {
		t.Fatalf("matching binds rejected: %v", err)
	}
	err := checkNoUserNSBinds([]Bind{{Src: "/srv/a", Dest: "/work/a"}})
	if err == nil || !strings.Contains(err.Error(), "cannot remap bind") {
		t.Fatalf("remapped bind = %v, want remap refusal", err)
	}
}

func TestProbeUserNSUsesTmpfsNotSysctl(t *testing.T) {
	var gotBin string
	var gotArgs []string
	ok := probeUserNSWith(context.Background(), "", userNSProbeDeps{
		lookPath: func(name string) (string, error) {
			if name == "bwrap" {
				return "/usr/bin/bwrap", nil
			}
			return "/usr/bin/true", nil
		},
		runCmd: func(_ context.Context, name string, args ...string) error {
			gotBin = name
			gotArgs = slices.Clone(args)
			return nil
		},
	})
	if !ok {
		t.Fatal("probeUserNSWith = false, want true")
	}
	if gotBin != "/usr/bin/bwrap" {
		t.Fatalf("probe binary = %q, want /usr/bin/bwrap", gotBin)
	}
	joined := strings.Join(gotArgs, " ")
	for _, want := range []string{"--unshare-user", "--tmpfs /tmp", "--proc /proc", "-- /usr/bin/true"} {
		if !strings.Contains(joined, want) {
			t.Errorf("probe args missing %q: %s", want, joined)
		}
	}

	// Failing mount (e.g. AppArmor restricting userns) must report false.
	failOK := probeUserNSWith(context.Background(), "/usr/bin/bwrap", userNSProbeDeps{
		lookPath: func(string) (string, error) { return "/bin/true", nil },
		runCmd:   func(context.Context, string, ...string) error { return errors.New("EPERM") },
	})
	if failOK {
		t.Fatal("probeUserNSWith returned true when bwrap smoke failed")
	}

	// Missing bwrap must report false.
	missingOK := probeUserNSWith(context.Background(), "", userNSProbeDeps{
		lookPath: func(string) (string, error) { return "", os.ErrNotExist },
		runCmd:   func(context.Context, string, ...string) error { return nil },
	})
	if missingOK {
		t.Fatal("probeUserNSWith returned true when bwrap is missing")
	}
}

func TestBwrapSupportsDisableUserNS(t *testing.T) {
	cases := []struct {
		ver  string
		want bool
	}{
		{"", false},
		{"0.7.0", false},
		{"0.8.0", true},
		{"0.12.0", true},
		{"1.0.0", true},
		{"garbage", false},
	}
	for _, tc := range cases {
		if got := bwrapSupportsDisableUserNS(tc.ver); got != tc.want {
			t.Errorf("bwrapSupportsDisableUserNS(%q) = %v, want %v", tc.ver, got, tc.want)
		}
	}
}
