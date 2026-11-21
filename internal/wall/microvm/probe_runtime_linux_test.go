//go:build linux

package microvm

import (
	"bytes"
	"context"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/Microsoft/hcsshim/ext4/dmverity"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/runtimecache"
	"github.com/iamlakshya/simstim/internal/supply"
	"github.com/iamlakshya/simstim/internal/wall"
)

func installProbeRuntimeV1(t *testing.T, driver *Driver, multiVMM ...bool) supply.Manifest {
	t.Helper()
	manifest := bootFixture().Runtime
	data := bytes.Repeat([]byte("x"), 8192)
	tree, err := dmverity.MerkleTree(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	bodies := [][]byte{[]byte("vmm"), []byte("kernel"), slices.Concat(data, tree)}
	folder := supply.ReleaseFolder(manifest.Release) + "/"
	for i := range manifest.Items {
		item := &manifest.Items[i]
		item.Arch = runtime.GOARCH
		if i == 1 {
			item.URL = folder + supply.KernelAsset(runtime.GOARCH)
		}
		if i == 2 {
			*item = supply.NewItem(supply.KindTools, "tools", manifest.Release, runtime.GOARCH, bodies[i], folder+supply.ToolsAsset(runtime.GOARCH))
			item.RootHash = hex.EncodeToString(dmverity.RootHash(tree))
			item.Salt, item.DataBlocks = strings.Repeat("0", 64), 2
		}
	}
	if len(multiVMM) == 1 && multiVMM[0] {
		// Store/metadata fixtures only. These are not executable boot inputs.
		body := []byte("alternative VMM fixture")
		item := supply.NewItem(supply.KindVMM, "firecracker", "1.18.0", runtime.GOARCH, body, folder+"firecracker-"+runtime.GOARCH)
		item.EOL, item.HostKernel, item.Released = "2027-11-01", "5.10,6.1,6.18", "2026-11-01"
		manifest.Items = append(manifest.Items, item)
		bodies = append(bodies, body)
	}
	domain, err := driver.options.Domain(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	store, err := runtimecache.OpenHost(t.Context(), driver.options.Artefacts, domain, func(_ context.Context, publish func() error) error { return publish() })
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for i, item := range manifest.Items {
		part, err := store.BeginContext(t.Context(), item)
		if err != nil {
			t.Fatal(err)
		}
		if err := part.WriteAt(bodies[i], 0); err != nil {
			_ = part.Close()
			t.Fatal(err)
		}
		if err := part.Finish(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	defaults := []supply.Item{}
	if err := store.CommitWithImageDefaults(t.Context(), manifest, &defaults, runtime.GOARCH); err != nil {
		t.Fatal(err)
	}
	return manifest
}

func TestKVMAccessBlockerStillChecksOtherHostPrerequisites(t *testing.T) {
	spec := vmFixture(t)
	driver := fixtureDriver(t, spec.SessionDir)
	facts := api.CapProbe{OS: "linux", Arch: runtime.GOARCH, Kernel: "6.18.63", KVM: api.KVM{Present: true, AccessRestricted: true}}
	driver.options.Facts = func(context.Context) (api.CapProbe, error) { return facts, nil }
	driver.options.Domain = func(context.Context) (cachefs.Domain, error) {
		t.Fatal("incomplete physical prerequisites reached runtime storage")
		return cachefs.Domain{}, nil
	}
	for _, next := range []func(){
		func() {},
		func() { facts.UserNS = true },
		func() { facts.UserManager, facts.CgroupV2 = true, true },
	} {
		next()
		probe := driver.Probe(t.Context())
		if probe.OK || probe.CanPrepare || probe.Facts["root_blocker"] != "" {
			t.Fatal("a KVM access warning hid another host prerequisite", probe)
		}
	}
}

func TestKVMAccessBlockerRequiresVerifiedRuntimeAndGrantsNoReadiness(t *testing.T) {
	spec := vmFixture(t)
	driver := fixtureDriver(t, spec.SessionDir)
	driver.options.Artefacts = filepath.Join(spec.SessionDir, "runtime")
	manifest := installProbeRuntimeV1(t, driver)
	facts := api.CapProbe{OS: "linux", Arch: runtime.GOARCH, Kernel: "6.18.63", KVM: api.KVM{Present: true, AccessRestricted: true}}
	probe := func() wall.Probe {
		return driver.probeRuntimeV1(t.Context(), facts, wall.Probe{Driver: "chv", Level: wall.MicroVM, Facts: map[string]string{}})
	}
	blocked := probe()
	if blocked.OK || blocked.CanPrepare || blocked.Facts["root_blocker"] != "kvm_access" || blocked.Facts["runtime_verified"] != "true" {
		t.Fatal("the checked runtime did not retain its distinct access blocker", blocked)
	}
	facts.KVM.Usable, facts.KVM.AccessRestricted = true, false
	first := probe()
	if first.OK || !first.CanPrepare || first.Facts["root_blocker"] != "" {
		t.Fatal("a first boot needed old boot evidence or acquired compatibility readiness", first)
	}
	facts.KVM.Usable, facts.KVM.AccessRestricted = false, true
	path := filepath.Join(driver.options.Artefacts, "sha256", manifest.Items[0].SHA256)
	mode := runtimecache.Mode(manifest.Items[0])
	if err := os.Chmod(path, mode|0o200); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("bad"), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	corrupt := probe()
	if corrupt.OK || corrupt.CanPrepare || corrupt.Facts["root_blocker"] != "" {
		t.Fatal("corrupt runtime bytes acquired a root-repair promise", corrupt)
	}
	if _, err := os.Stat(driver.options.Evidence); !os.IsNotExist(err) {
		t.Fatal("a diagnostic probe created boot evidence", err)
	}
}
