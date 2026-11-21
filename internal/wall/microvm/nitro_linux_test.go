//go:build linux

package microvm

import (
	"context"
	"errors"
	"github.com/iamlakshya/simstim/internal/cachefs"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/nitrodev"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestNitroNeverBorrowsKVMReadinessOrIdentity(t *testing.T) {
	usable, restricted := nitroBackend.deviceAccess(api.CapProbe{KVM: api.KVM{Present: true, Usable: true}})
	if usable || restricted {
		t.Fatal("KVM qualified a Nitro start")
	}
	facts := api.CapProbe{Nitro: &api.Nitro{Present: true, Usable: true}, Subuid: true, KVM: api.KVM{InGroupManager: true}}
	if usable, _ := nitroBackend.deviceAccess(facts); !usable {
		t.Fatal("Nitro needs nested KVM")
	}
	if nitroBackend.identityMode(facts) != "login" {
		t.Fatal("login-only Nitro ACL became a subuid grant")
	}
	spec := firecrackerFixture(t)
	if kind, err := backendForSpec(spec); err != nil || kind != firecrackerBackend {
		t.Fatal("legacy KVM plan changed", err)
	}
	spec.Driver = "nitro"
	if kind, err := backendForSpec(spec); err != nil || kind != nitroBackend {
		t.Fatal("explicit Nitro plan lost", err)
	}
	spec.Driver = "chv"
	if _, err := backendForSpec(spec); err == nil {
		t.Fatal("conflicting original plan accepted")
	}
}

func TestNitroOwnedDiagnosticGetsParentKernelHint(t *testing.T) {
	sandbox := &sandbox{driver: &Driver{kind: nitroBackend}}
	_, _ = sandbox.log.Write([]byte("Failed to query hypervisor-supported flags: inappropriate ioctl for device"))
	failure := sandbox.bootFailure(errors.New("owned child exited"))
	var typed *proto.Error
	if !errors.As(failure, &typed) || typed.Code != "wall_unavailable" || typed.Hint != nitrodev.ParentKernelHint {
		t.Fatalf("failure %v", failure)
	}
}

func TestNitroProbeRefusesKVMFallbackBeforeRuntimeAccess(t *testing.T) {
	spec := firecrackerFixture(t)
	options := fixtureDriver(t, spec.SessionDir).options
	options.Facts = func(context.Context) (api.CapProbe, error) {
		return api.CapProbe{OS: "linux", Arch: "amd64", Kernel: "6.18.63", KVM: api.KVM{Usable: true}, Nitro: &api.Nitro{Present: true}}, nil
	}
	options.Domain = func(context.Context) (cachefs.Domain, error) {
		t.Fatal("unsupported parent reached runtime storage")
		return cachefs.Domain{}, nil
	}
	driver, err := NewForDriver("nitro", options)
	if err != nil {
		t.Fatal(err)
	}
	probe := driver.Probe(t.Context())
	if probe.OK || probe.CanPrepare || probe.Fix != nitrodev.ParentKernelHint {
		t.Fatalf("wrong Nitro probe %+v", probe)
	}
}
