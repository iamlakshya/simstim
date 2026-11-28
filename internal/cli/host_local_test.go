package cli

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/client/clienttest"
	"github.com/iamlakshya/simstim/internal/provider"
	"github.com/iamlakshya/simstim/internal/provider/lima"
)

type captureLocalProvider struct {
	got provider.Request
	m   *clienttest.Machine
}

func (p *captureLocalProvider) Name() string { return "fake" }

func (p *captureLocalProvider) Create(_ context.Context, req provider.Request) (client.Host, error) {
	p.got = req
	id := provider.MachineName(req.Name)
	return client.Host{
		Name: req.Name,
		SSH:  "lima-" + id,
		Machine: &client.Machine{
			Provider:  "fake",
			ID:        id,
			IdleStop:  req.IdleStop,
			Autostart: req.Autostart,
		},
	}, nil
}

func (p *captureLocalProvider) Open(client.Host) (client.Lifecycle, error) { return p.m, nil }

func TestHostAddLocalValidation(t *testing.T) {
	newTestHost(t)

	// Missing host name without --local -> exit 2.
	if got, code := lineErr(t, "host", "add"); code != 2 || !strings.Contains(got, "needs a host name") {
		t.Fatalf("host add (0 args) = %d: %q", code, got)
	}
	// --local flags without --local -> exit 2.
	for _, flag := range []string{"--cpus=4", "--memory=6", "--disk=50", "--autostart=login", "--idle-stop=never"} {
		if got, code := lineErr(t, "host", "add", "my-vps", flag); code != 2 || !strings.Contains(got, "needs --local") {
			t.Errorf("host add my-vps %s = %d: %q", flag, code, got)
		}
	}
	// Conflicting flags with --local -> exit 2.
	for _, args := range [][]string{
		{"host", "add", "--local", "--ssh-config", "/tmp/c"},
		{"host", "add", "--local", "--host-key", "SHA256:test"},
		{"host", "add", "--local", "--provider-script", "/tmp/p"},
		{"host", "add", "--local", "--wake"},
		{"host", "add", "--local", "mac", "user@host"},
	} {
		if _, code := lineErr(t, args...); code != 2 {
			t.Errorf("%v exited %d, want 2", args, code)
		}
	}

	origCheck := checkLocalMac
	t.Cleanup(func() { checkLocalMac = origCheck })
	checkLocalMac = func(context.Context) error { return nil }
	if got, code := lineErr(t, "host", "add", "--local", "--autostart", "bogus"); code != 2 || !strings.Contains(got, "not an autostart mode") {
		t.Fatalf("bad --autostart = %d: %q", code, got)
	}
}

func TestHostAddLocalCreatesAndConnects(t *testing.T) {
	m, tr := withFake(t, nil)
	tr.Features = []string{"sandbox"}
	tr.Answers = map[string]any{
		"sandbox.probe": api.CapProbe{
			OS:           "linux",
			Arch:         "arm64",
			Kernel:       "6.12.0",
			Bwrap:        true,
			BwrapVersion: "0.11.0",
			LandlockABI:  4,
			Seccomp:      true,
			UserNS:       true,
			Walls:        []api.WallProbe{{Driver: "process", Level: "process", OK: true, Version: "0.11.0"}},
		},
	}
	cp := &captureLocalProvider{m: m}

	origCheck, origSizing, origPin, origProv := checkLocalMac, localMacSizing, ensurePinnedLima, localMacProvider
	t.Cleanup(func() {
		checkLocalMac, localMacSizing, ensurePinnedLima, localMacProvider = origCheck, origSizing, origPin, origProv
	})
	checkLocalMac = func(context.Context) error { return nil }
	localMacSizing = func(context.Context) (int, int) { return 5, 8 }
	pinnedCalled := false
	ensurePinnedLima = func(context.Context, io.Writer) (string, error) {
		pinnedCalled = true
		return "/fake/lima/2.3.0/bin/limactl", nil
	}
	localMacProvider = func() provider.Provider { return cp }

	out := okSaid(t, "host", "add", "--local", "--label", "place=desk", "--trusted", "--yes")
	if !pinnedCalled {
		t.Error("ensurePinnedLima was not called")
	}
	if cp.got.Name != lima.DefaultMacName || cp.got.CPUs != 5 || cp.got.MemoryGiB != 8 || cp.got.DiskGiB != lima.DefaultDiskGiB || cp.got.IdleStop != "never" || cp.got.Autostart != lima.AutostartBoot {
		t.Fatalf("provider.Request = %+v", cp.got)
	}
	if !strings.Contains(out, "Added this Mac as mac in ") {
		t.Errorf("stderr lacks completion line:\n%s", out)
	}
	h, err := client.FindHost("mac")
	if err != nil {
		t.Fatalf("FindHost(mac): %v", err)
	}
	if h.Machine == nil || h.Machine.Autostart != lima.AutostartBoot || h.Machine.IdleStop != "never" || h.Labels["place"] != "desk" || !h.Trusted {
		t.Errorf("saved host = %+v, machine = %+v", h, h.Machine)
	}
	if h.WallFloor != "process" {
		t.Errorf("saved host WallFloor = %q, want process", h.WallFloor)
	}
	if !strings.Contains(out, "Sandboxes on mac:") {
		t.Errorf("stderr lacks Sandboxes block:\n%s", out)
	}
}
