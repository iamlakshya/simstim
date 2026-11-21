//go:build linux && amd64

package host

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/supply"
	"github.com/iamlakshya/simstim/internal/vmneed"
)

// This exercises a real original OP/OFD and protected file comparison only.
// Metadata fixture bytes are not executable artifacts or a VM/native grant.
func TestResumeImageUsesSameProtectedNeedAndIgnoresAdvisoryRow(t *testing.T) {
	secretHome(t)
	need := resumeImageMetadataFixtureV1()
	encoded, err := vmneed.EncodeV1(need, "amd64")
	if err != nil {
		t.Fatal(err)
	}
	expected, err := vmneed.EncodeImageV1(need.Image)
	if err != nil {
		t.Fatal(err)
	}
	budget, err := vmneed.BudgetFor(need.GuestRAM)
	if err != nil {
		t.Fatal(err)
	}
	id := "image234"
	saved := Manifest{Schema: 2, ID: id, Name: "image", Wall: &WallRecord{Level: "microvm", Driver: "chv"}, VMNeedV1: encoded, RuntimeManifest: &need.Runtime, Limits: Limits{MemoryMax: budget.Limit, Declared: budget.Limit}}
	if err := paths.EnsurePrivateDir(paths.SessionDir(id)); err != nil {
		t.Fatal(err)
	}
	write := func() {
		t.Helper()
		raw, err := saved.MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(paths.SessionDir(id), "manifest.json"), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write()
	original, err := BeginSessionOperation(context.Background(), id, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer original.Close()
	if err := checkResumeExpectedImageV1(context.Background(), original, expected); err != nil {
		t.Fatal(err)
	}
	// A changed host display projection cannot choose a new protected image.
	saved.Run.Image = "sha256:" + strings.Repeat("9", 64)
	write()
	if err := checkResumeExpectedImageV1(context.Background(), original, expected); err != nil {
		t.Fatal(err)
	}
	replacement := need.Image.Clone()
	replacement.Digest = "sha256:" + strings.Repeat("8", 64)
	other, err := vmneed.EncodeImageV1(replacement)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkResumeExpectedImageV1(context.Background(), original, other); err == nil {
		t.Fatal("advisory replacement changed original protected image")
	}
	// Offline restore receives no new laptop policy. No expected-image field
	// may manufacture a policy update or change the retained original Need.
	if err := checkResumeExpectedImageV1(context.Background(), original, nil); err != nil {
		t.Fatal(err)
	}
	current, err := original.LoadContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(current.VMNeedV1, encoded) {
		t.Fatal("image comparison rewrote original Need")
	}
	if err := checkResumeExpectedImageV1(context.Background(), &SessionOperation{id: id}, expected); err == nil {
		t.Fatal("data-only operation supplied protected source")
	}
}
func resumeImageMetadataFixtureV1() vmneed.NeedV1 {
	folder := supply.ReleaseFolder("1.0.0") + "/"
	vmm := supply.NewItem(supply.KindVMM, "cloud-hypervisor", "54.1", "amd64", []byte("vmm"), folder+"cloud-hypervisor-static")
	vmm.EOL, vmm.Released, vmm.HostKernel = "2027-01-06", "2026-11-11", ">=6.8"
	kernel := supply.NewItem(supply.KindKernel, "guest", "6.18.63", "amd64", []byte("kernel"), folder+"vmlinux-amd64")
	kernel.Licence, kernel.SourceURL, kernel.Released = "GPL-2.0-only", folder+"linux-6.18.63.tar.xz", "2026-11-10"
	tools := supply.NewItem(supply.KindTools, "tools", "1.0.0", "amd64", []byte("tools"), folder+"tools-amd64.erofs")
	tools.Size, tools.DataBlocks, tools.RootHash, tools.Salt = 202*4096, 200, strings.Repeat("d", 64), strings.Repeat("0", 64)
	return vmneed.NeedV1{BootV1: vmneed.BootV1{Version: 1, GuestRAM: 1024 * vmneed.Mebibyte, Runtime: supply.Manifest{Release: "1.0.0", Runtime: "fixture", Items: []supply.Item{vmm, kernel, tools}}, Image: image.Request{Ref: "registry.example/team/dev:1", Digest: "sha256:" + strings.Repeat("a", 64), PlatformDigest: "sha256:" + strings.Repeat("b", 64), Arch: "amd64", RootHash: strings.Repeat("c", 64), DataBlocks: 300}, Agents: supply.ContentRef{Name: "agents", SHA256: strings.Repeat("e", 64), Size: 4096}}, Driver: "chv", CPUs: 2, DiskMax: 16 << 20}
}
