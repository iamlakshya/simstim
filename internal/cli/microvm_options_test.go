package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestNewMicroVMFlagsAndConfigCheck(t *testing.T) {
	root := Root()
	c, _, err := root.Find([]string{"new"})
	if err != nil {
		t.Fatalf("find new: %v", err)
	}
	for _, flagName := range []string{"image", "keep-system", "docker"} {
		if c.Flags().Lookup(flagName) == nil {
			t.Errorf("new command missing --%s flag", flagName)
		}
	}
	if got, err := parseWallFlag("microvm"); err != nil || got != "microvm" {
		t.Fatalf("parseWallFlag(microvm) = %q, %v; want microvm, nil", got, err)
	}
	if _, err := parseWallFlag("bogus"); err == nil {
		t.Fatal("parseWallFlag(bogus) = nil, want error")
	} else {
		var ue *client.UserError
		if !errors.As(err, &ue) || !strings.Contains(ue.Hint, "microvm") {
			t.Fatalf("parseWallFlag(bogus) = %#v, want UserError hint naming microvm", err)
		}
	}

	// config check accepts valid sandbox.system/sudo/docker/image and flags bad sandbox.system.
	h := hosttest.New(t)
	cfgDir := client.ConfigDir()
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	_ = h
	cfgPath := filepath.Join(cfgDir, "config.toml")
	valid := "[sandbox]\nimage = \"buildpack-deps:trixie-scm\"\nsystem = \"keep\"\nsudo = true\ndocker = true\n"
	if err := os.WriteFile(cfgPath, []byte(valid), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, "config", "check"); err != nil {
		t.Fatalf("config check rejected valid microvm settings: %v", err)
	}

	invalid := "[sandbox]\nsystem = \"persist\"\n"
	if err := os.WriteFile(cfgPath, []byte(invalid), 0o600); err != nil {
		t.Fatal(err)
	}
	out, errOut, code := runConfigCheckMain(t, "config", "check")
	combined := out + errOut
	if code != 1 || !strings.Contains(combined, "sandbox.system") || !strings.Contains(combined, "reset or keep") {
		t.Fatalf("config check invalid sandbox.system: code=%d stdout=%q stderr=%q", code, out, errOut)
	}
}

func TestHostRefusesMicroVMOptionsOnNonMicroVMWalls(t *testing.T) {
	hosttest.New(t)

	for _, wall := range []string{"none", "plain", "process", "gvisor"} {
		cases := []struct {
			name     string
			args     host.NewArgs
			wantCode string
			wantKind client.Kind
		}{
			{
				name:     wall + "/sudo",
				args:     host.NewArgs{Name: "t", Cwd: "~", Argv: []string{"true"}, Wall: wall, Sudo: true},
				wantCode: "sudo_wall",
				wantKind: client.KindRefused,
			},
			{
				name:     wall + "/docker",
				args:     host.NewArgs{Name: "t", Cwd: "~", Argv: []string{"true"}, Wall: wall, Docker: true},
				wantCode: "docker_wall",
				wantKind: client.KindRefused,
			},
			{
				name:     wall + "/keep-system",
				args:     host.NewArgs{Name: "t", Cwd: "~", Argv: []string{"true"}, Wall: wall, KeepSystem: true},
				wantCode: "keep_system_wall",
				wantKind: client.KindRefused,
			},
			{
				name:     wall + "/image",
				args:     host.NewArgs{Name: "t", Cwd: "~", Argv: []string{"true"}, Wall: wall, Image: "debian:trixie"},
				wantCode: "image_wall",
				wantKind: client.KindFailed,
			},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				// process/gvisor probe may run before checkWalledSettings on hosts without them,
				// so only assert exact code when wall is none/plain or when the host probes OK.
				_, err := host.New(tc.args)
				var pe *proto.Error
				if !errors.As(err, &pe) {
					t.Fatalf("host.New = %v, want *proto.Error", err)
				}
				if (wall == "process" || wall == "gvisor") && pe.Code == "wall_setup" {
					t.Skipf("%s wall not set up on this host", wall)
				}
				if pe.Code != tc.wantCode || pe.Hint == "" {
					t.Fatalf("host.New(%s) = code %q hint %q, want %q with hint", tc.name, pe.Code, pe.Hint, tc.wantCode)
				}
				if gotKind := client.HostKind(tc.wantCode); gotKind != tc.wantKind {
					t.Errorf("HostKind(%q) = %v, want %v", tc.wantCode, gotKind, tc.wantKind)
				}
			})
		}
	}
	if got := client.HostKind("docker_missing"); got != client.KindFailed {
		t.Errorf("HostKind(docker_missing) = %v, want KindFailed", got)
	}
	if got := client.HostKind("system_invalid"); got != client.KindUsage {
		t.Errorf("HostKind(system_invalid) = %v, want KindUsage", got)
	}
}

func TestShowAndStateLabelMicroVMTags(t *testing.T) {
	s := client.Session{
		ID:    "abcd2345",
		Name:  "vm-dev",
		Agent: "claude",
		Cwd:   "/work",
		State: "working",
		Wall: &api.WallInfo{
			Level:  "microvm",
			Driver: "chv",
			System: "keep",
			Sudo:   true,
			Docker: true,
			Image:  "devtools",
		},
	}
	if got := stateLabel(s); got != "working [root, docker]" {
		t.Errorf("stateLabel = %q, want %q", got, "working [root, docker]")
	}
	desc := describeSession("box", s)
	if !strings.Contains(desc, "Sandbox         microvm (root, docker, keep-system, image devtools)") {
		t.Errorf("describeSession missing sandbox line:\n%s", desc)
	}
}
