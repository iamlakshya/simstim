package process

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/iamlakshya/simstim/internal/wall"
)

func isolate(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR", "XDG_CONFIG_DIRS", "XDG_DATA_DIRS"} {
		t.Setenv(key, filepath.Join(root, key))
	}
}

func fakeSystem(files map[string]string) probeSystem {
	return probeSystem{
		os: "linux", kernel: "6.12.1-example", landlock: func() int { return 6 },
		readFile: func(path string) ([]byte, error) {
			if text, found := files[path]; found {
				return []byte(text), nil
			}
			return nil, fmt.Errorf("fake file missing")
		},
		smoke: func(context.Context) smokeResult { return smokeResult{UserNS: true, Seccomp: true} },
	}
}

func TestProbePrerequisites(t *testing.T) {
	isolate(t)
	for _, tt := range []struct {
		name   string
		change func(*probeSystem)
		ready  string
		reason string
	}{
		{"ready runtime absent", func(*probeSystem) {}, "true", "the process sandbox runtime is not built into this helper"},
		{"old kernel", func(s *probeSystem) { s.kernel = "5.12.9" }, "false", "the process wall needs Linux 5.13 or later"},
		{"invalid kernel", func(s *probeSystem) { s.kernel = "unknown" }, "false", "the process wall needs Linux 5.13 or later"},
		{"other OS", func(s *probeSystem) { s.os = "darwin" }, "false", "the process wall needs Linux"},
		{"namespace failure", func(s *probeSystem) { s.smoke = func(context.Context) smokeResult { return smokeResult{} } }, "false", "a child could not make user and mount namespaces and mount tmpfs"},
		{"landlock absent", func(s *probeSystem) { s.landlock = func() int { return 0 } }, "false", "the host has no usable Landlock ABI"},
		{"seccomp failure", func(s *probeSystem) {
			s.smoke = func(context.Context) smokeResult { return smokeResult{UserNS: true} }
		}, "false", "a child could not load a seccomp filter"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			sys := fakeSystem(nil)
			tt.change(&sys)
			p := probe(context.Background(), sys)
			if p.OK || p.Driver != "process" || p.Level != wall.Process || p.Facts["prerequisite_ready"] != tt.ready || p.Reason != tt.reason || p.Fix == "" {
				t.Errorf("probe: %+v", p)
			}
		})
	}
}

func TestProbeUsesSmokeRatherThanSysctl(t *testing.T) {
	isolate(t)
	files := map[string]string{
		"/proc/sys/kernel/apparmor_restrict_unprivileged_userns": "1\n",
		"/proc/sys/user/max_user_namespaces":                     "0\n",
	}
	p := probe(context.Background(), fakeSystem(files))
	if p.Facts["prerequisite_ready"] != "true" || p.Facts["apparmor_userns"] != "true" || p.Facts["max_user_namespaces"] != "0" {
		t.Errorf("successful smoke lost to sysctl: %+v", p)
	}
	files["/proc/sys/user/max_user_namespaces"] = "100000\n"
	sys := fakeSystem(files)
	sys.smoke = func(context.Context) smokeResult { return smokeResult{} }
	p = probe(context.Background(), sys)
	if p.Facts["userns"] != "false" || p.Facts["prerequisite_ready"] != "false" {
		t.Errorf("failed smoke lost to sysctl: %+v", p)
	}
}

func TestProbeCancellation(t *testing.T) {
	isolate(t)
	sys := fakeSystem(nil)
	sys.smoke = func(context.Context) smokeResult {
		t.Fatal("cancelled probe started a child")
		return smokeResult{}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if p := probe(ctx, sys); p.OK || p.Facts["prerequisite_ready"] != "false" {
		t.Errorf("cancelled probe: %+v", p)
	}
}
