//go:build linux

package microvm

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/supply"
	"github.com/iamlakshya/simstim/internal/wall"
)

func vmFixture(t *testing.T) wall.Spec {
	t.Helper()
	boot := bootFixture()
	boot.Image.Arch = runtime.GOARCH
	for i := range boot.Runtime.Items {
		boot.Runtime.Items[i].Arch = runtime.GOARCH
	}
	folder := supply.ReleaseFolder(boot.Runtime.Release) + "/"
	boot.Runtime.Items[1].URL = folder + supply.KernelAsset(runtime.GOARCH)
	boot.Runtime.Items[2].URL = folder + supply.ToolsAsset(runtime.GOARCH)
	dir := diskTestDir(t)
	return wall.Spec{ID: "abcdef23", SessionDir: dir, MemoryMax: 1156 * mebibyte, CPUs: 2, DiskMax: diskTestBytes, VM: &wall.VMSpec{Version: 1, GuestRAM: 1024 * mebibyte, Runtime: boot.Runtime, Image: boot.Image, Agents: supply.ContentRef{Name: "agents", SHA256: boot.Runtime.Items[0].SHA256, Size: 4096}}}
}

func fixtureDriver(t *testing.T, dir string) *Driver {
	t.Helper()
	driver, err := New(Options{Facts: func(context.Context) (api.CapProbe, error) { return api.CapProbe{}, nil }, Identity: func() (string, error) { return "test-boot-helper", nil }, Domain: func(context.Context) (cachefs.Domain, error) {
		return cachefs.Domain{Scope: strings.Repeat("a", 64), HostID: strings.Repeat("b", 32)}, nil
	}, Reference: func(context.Context, wall.Spec) error { return nil }, Artefacts: dir, Images: dir, Evidence: filepath.Join(dir, "proof")})
	if err != nil {
		t.Fatal(err)
	}
	return driver
}

func TestPrepareAndOpenRequireFreshAuthority(t *testing.T) {
	spec := vmFixture(t)
	driver := fixtureDriver(t, spec.SessionDir)
	prepared, err := driver.Prepare(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	prepared.(*sandbox).releaseLease()
	if _, err := driver.Open(context.Background(), spec.SessionDir); err == nil {
		t.Fatal("saved host pins became execution authority")
	}
	// The saved file contains layout only. A different freshly validated
	// trusted Need supplies the runtime authority on this stopped open.
	savedPath := filepath.Join(spec.SessionDir, vmSpecName)
	data, err := os.ReadFile(savedPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "runtime") || strings.Contains(string(data), "root_hash") {
		t.Fatal("execution pins were duplicated into the VM layout")
	}
	fresh := spec
	copyVM := *spec.VM
	fresh.VM = &copyVM
	fresh.VM.Runtime.Runtime = "new-trusted-need"
	expected := wall.WithExpectedSpec(context.Background(), fresh)
	opened, err := driver.Open(expected, spec.SessionDir)
	if err != nil {
		t.Fatal(err)
	}
	if opened.(*sandbox).spec.VM.Runtime.Runtime != fresh.VM.Runtime.Runtime {
		t.Fatal("Open selected runtime authority from saved host metadata")
	}
	opened.(*sandbox).releaseLease()
	changed := spec
	changed.DiskMax++
	if _, err := driver.Open(wall.WithExpectedSpec(context.Background(), changed), spec.SessionDir); err == nil {
		t.Fatal("Open silently resized persistent disk")
	}
}

func TestBuildUsesTrustedExpectedPlanOverHostArgument(t *testing.T) {
	spec := vmFixture(t)
	driver := fixtureDriver(t, spec.SessionDir)
	argument := spec
	copyVM := *spec.VM
	argument.VM = &copyVM
	argument.VM.Runtime.Runtime = "host-argument"
	resource, err := driver.Build(wall.WithExpectedSpec(context.Background(), spec), argument)
	if err != nil {
		t.Fatal(err)
	}
	defer resource.(*sandbox).releaseLease()
	if resource.(*sandbox).spec.VM.Runtime.Runtime != spec.VM.Runtime.Runtime {
		t.Fatal("a host argument overrode the trusted expected Need")
	}
}

func TestLayoutRejectsUnknownSecurityFields(t *testing.T) {
	spec := vmFixture(t)
	driver := fixtureDriver(t, spec.SessionDir)
	resource, err := driver.Prepare(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	resource.(*sandbox).releaseLease()
	path := filepath.Join(spec.SessionDir, vmSpecName)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	record["unknown_security"] = true
	data, err = json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := driver.Open(wall.WithExpectedSpec(context.Background(), spec), spec.SessionDir); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("layout did not reject the unknown security field: %v", err)
	}
}

func TestPrepareRefusesWrongRoleArchVerityAndUnknownVersion(t *testing.T) {
	for name, change := range map[string]func(*wall.Spec){
		"version":      func(s *wall.Spec) { s.VM.Version++ },
		"image root":   func(s *wall.Spec) { s.VM.Image.RootHash = "" },
		"two images":   func(s *wall.Spec) { s.Image = &wall.ImageRef{} },
		"agents role":  func(s *wall.Spec) { s.VM.Agents.Name = "vmm" },
		"runtime role": func(s *wall.Spec) { s.VM.Runtime.Items[0].Kind = "agents" },
		"ambiguous VMM": func(s *wall.Spec) {
			other := s.VM.Runtime.Items[0]
			other.Name = "other"
			s.VM.Runtime.Items = append(s.VM.Runtime.Items, other)
		},
		"guest RAM as unit cap": func(s *wall.Spec) { s.MemoryMax = s.VM.GuestRAM },
		"unaligned agents":      func(s *wall.Spec) { s.VM.Agents.Size++ },
		"architecture":          func(s *wall.Spec) { s.VM.Image.Arch = "other" },
	} {
		t.Run(name, func(t *testing.T) {
			spec := vmFixture(t)
			driver := fixtureDriver(t, spec.SessionDir)
			change(&spec)
			if _, err := driver.Prepare(context.Background(), spec); err == nil {
				t.Fatal("invalid authority accepted")
			}
			if _, err := os.Stat(filepath.Join(spec.SessionDir, vmSpecName)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("invalid authority changed host layout")
			}
		})
	}
}

func TestOpenRefusesPersistentIdentityModeChange(t *testing.T) {
	spec := vmFixture(t)
	driver := fixtureDriver(t, spec.SessionDir)
	prepared, err := driver.Prepare(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	prepared.(*sandbox).releaseLease()
	driver.options.Facts = func(context.Context) (api.CapProbe, error) {
		return api.CapProbe{Subuid: true, KVM: api.KVM{InGroupManager: true}}, nil
	}
	if _, err := driver.Open(wall.WithExpectedSpec(context.Background(), spec), spec.SessionDir); err == nil {
		t.Fatal("persistent disk was silently moved to another identity mode")
	}
}

func TestStartRefusesMissingBoundaryBeforeDiskOrRuntime(t *testing.T) {
	spec := vmFixture(t)
	driver := fixtureDriver(t, spec.SessionDir)
	want := errors.New("no owning unit")
	driver.options.Boundary = func(context.Context, MemoryBudget) error { return want }
	s := newSandbox(driver, spec, nil, "login", context.Background())
	if _, err := s.Start(context.Background()); !errors.Is(err, want) {
		t.Fatalf("Start error = %v", err)
	}
	if got := s.Wait(); !errors.Is(got.Err, want) {
		t.Fatalf("owned lifecycle lost startup failure: %+v", got)
	}
	if _, err := os.Stat(vmDataDir(spec)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("VM data created before boundary admission")
	}
	if err := s.Kill(); err != nil {
		t.Fatal(err)
	}
}

func TestUIDModeRequiresManagerKVMGroupForSubuid(t *testing.T) {
	facts := api.CapProbe{Subuid: true, KVM: api.KVM{InGroup: true, InGroupNow: true}}
	if uidMode(facts) != "login" {
		t.Fatal("login-shell group membership stood in for manager membership")
	}
	facts.KVM.InGroupManager = true
	if uidMode(facts) != "subuid" {
		t.Fatal("complete canonical subuid/group facts were ignored")
	}
	facts.Subuid = false
	if uidMode(facts) != "login" {
		t.Fatal("subuid chosen without mappings")
	}
}
