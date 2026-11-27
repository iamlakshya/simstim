//go:build linux

package host

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/supply"
	"github.com/iamlakshya/simstim/internal/vmneed"
	"github.com/iamlakshya/simstim/internal/wall/microvm"
)

type runtimeOriginFixtureHTTPV1 func(*http.Request) (*http.Response, error)

func (f runtimeOriginFixtureHTTPV1) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Actual local store/converter bytes only. The converter jail and VM boot
// are not exercised. The HTTP transport serves this exact in-memory document.
func runtimeOriginStoredRootFixtureV1(t *testing.T, ctx context.Context) image.Request {
	t.Helper()
	document := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{},"layers":[]}`)
	sum := sha256.Sum256(document)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	registry := &image.Registry{Client: &http.Client{Transport: runtimeOriginFixtureHTTPV1(func(r *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(r.URL.Path, "/"+digest) {
			return nil, fmt.Errorf("unexpected fixture registry request")
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(document))}, nil
	})}}
	request := image.Request{Ref: brand.ImageRepository + "@" + digest, Digest: digest, PlatformDigest: digest, Arch: runtime.GOARCH}
	publication, err := beginRuntimePublication(ctx)
	if err != nil {
		t.Fatal(err)
	}
	store, err := image.OpenHost(ctx, paths.ImagesDir(), publication.owner.domain(), publication.gate)
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Prepare(ctx, request, registry, func(owned context.Context, layers []image.Layer, files []*os.File, out *os.File) error {
		_, err := image.Convert(owned, layers, files, out)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := publication.close(); err != nil {
		t.Fatal(err)
	}
	request.RootHash, request.DataBlocks = record.RootHash, record.DataBlocks
	return request
}

// The recorded prior-boot manifest is fixture DATA. Cold authority comes
// only from the actual OP/OFD, authenticated manager, native unit/cgroup
// absence and real saved artifact descriptors captured by InspectResume.
func runtimeOriginPriorRecordFixtureV1(t *testing.T, b *bridge, need vmneed.NeedV1, full supply.Manifest) string {
	t.Helper()
	id := NewID()
	rawNeed, err := vmneed.EncodeV1(need, runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	args := NewArgs{ID: id, Wall: "microvm", VMNeedV1: rawNeed, RuntimeOrigin: runtimeOriginRawFixtureV1(t, full)}
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := b.newRequestContext(proto.Request{ID: 7, Op: "new", Args: raw}, args)
	if err != nil {
		t.Fatal(err)
	}
	ctx, _, err = captureNewRequest(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := reserveSessionID(id)
	if err != nil {
		t.Fatal(err)
	}
	operation, err := beginReservedSessionOperation(ctx, reservation)
	if err != nil {
		t.Fatal(err)
	}
	policy, layers, err := egress.MergeLayers(nil)
	if err != nil {
		t.Fatal(err)
	}
	budget, err := vmneed.BudgetFor(need.GuestRAM)
	if err != nil {
		t.Fatal(err)
	}
	previous := "00000000-0000-4000-8000-000000000001"
	if previous == BootID() {
		previous = "00000000-0000-4000-8000-000000000002"
	}
	m := &Manifest{Schema: 2, ID: id, Name: "runtime-origin", CreatedAt: time.Now().UTC(), Agent: AgentInfo{Kind: "custom", Argv: []string{"true"}, Command: []string{"true"}}, Cwd: "/work/scratch", Wall: &WallRecord{Level: "microvm", Driver: need.Driver}, Policy: &policy, Layers: layers, Files: &FilePlan{Seed: Seed{Scratch: true, Repo: "scratch"}}, Limits: Limits{MemoryMax: budget.Limit, Declared: budget.Limit}, Run: Runtime{BootID: previous, Image: need.Image.PlatformDigest}, Status: Status{State: StateStopped}, Restart: Restart{Policy: "never"}}
	if err := admitReservedRuntime(ctx, operation, m, args); err != nil {
		t.Fatal(err)
	}
	if err := operation.saveNewManifest(m); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(paths.SessionDir(id), "vm"), 0o700); err != nil {
		t.Fatal(err)
	}
	disk, err := os.OpenFile(filepath.Join(paths.SessionDir(id), "vm", microvm.SessionDiskName), os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := disk.Truncate(need.DiskMax); err != nil {
		t.Fatal(err)
	}
	if err := disk.Close(); err != nil {
		t.Fatal(err)
	}
	if err := operation.CloseWithError(); err != nil {
		t.Fatal(err)
	}
	reservation.close()
	return id
}

func TestResumeRuntimeProjectionConsumerUsesActualColdAndStatusOwnersV1(t *testing.T) {
	// This test performs no StartUnit, stop, VM execution or credential read.
	// A platform without the authentic private user manager cannot qualify it.
	b, full, original, call := runtimeOriginProducerFixtureV1(t)
	probe, err := openNativeUserManagerV1(t.Context())
	if err != nil {
		t.Skip("actual private user manager is unavailable: " + err.Error())
	}
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}
	agents := runtimeOriginAgentsPinFixtureV1(t, b)
	root := runtimeOriginStoredRootFixtureV1(t, b.ctx)
	for _, driver := range []string{"chv", "firecracker", "nitro"} {
		t.Run(driver, func(t *testing.T) {
			selected, err := vmneed.SelectRuntimeForDriver(driver, full, runtime.GOARCH)
			if err != nil {
				t.Fatal(err)
			}
			need := vmneed.NeedV1{BootV1: vmneed.BootV1{Version: 1, GuestRAM: 512 << 20, Runtime: selected, Image: root, Agents: agents.Content}, Driver: driver, CPUs: 2, DiskMax: 16 << 20}
			id := runtimeOriginPriorRecordFixtureV1(t, b, need, full)
			need.GuestRAM = 768 << 20
			rawNeed, err := vmneed.EncodeV1(need, runtime.GOARCH)
			if err != nil {
				t.Fatal(err)
			}
			expectedImage, err := vmneed.EncodeImageV1(need.Image)
			if err != nil {
				t.Fatal(err)
			}
			start := api.ResumeArgs{ID: id, MemoryMax: need.GuestRAM, ExpectedImageV1: expectedImage}
			phase1 := call("resume.prepare-v1", api.ResumePrepareArgsV1{V: 1, PrepareVM: true, Start: start})
			if !phase1.OK {
				t.Fatal("actual bridge cold source refused", phase1.Error)
			}
			var prepared api.ResumePrepareResultV1
			if json.Unmarshal(phase1.Result, &prepared) != nil || !prepared.Valid() || prepared.Mode != "cold" || prepared.VM == nil {
				t.Fatal("actual phase1 did not retain original cold source")
			}
			start.VMNeedV1 = rawNeed
			second := api.ResumeVMPrepareArgsV1{V: 1, Prepared: prepared.Prepared, Start: start, RuntimeOrigin: runtimeOriginRawFixtureV1(t, full)}
			phase2 := call("resume.vm.prepare-v1", second)
			if !phase2.OK {
				t.Fatal("actual bridge Resume projection refused", phase2.Error)
			}
			if retry := call("resume.vm.prepare-v1", second); !retry.OK {
				t.Fatal("identical original phase2 was not idempotent", retry.Error)
			}
			altered := second
			changed := cloneSelectedRuntime(full)
			changed.Runtime = "changed-origin"
			altered.RuntimeOrigin = runtimeOriginRawFixtureV1(t, changed)
			if retry := call("resume.vm.prepare-v1", altered); retry.OK {
				t.Fatal("phase2 retry altered original origin")
			}
			if unsolicited := call("resume", map[string]any{"id": id, "prepared": prepared.Prepared, "runtime_origin_v1": second.RuntimeOrigin}); unsolicited.OK {
				t.Fatal("final Resume accepted phase2-only carrier")
			}
			service := b.resumeServiceV1()
			p := service.plan.Load()
			if p == nil || p.vm == nil || p.cold == nil || p.accounting == nil || p.vm.producers.runtime != original || p.vm.producers.runtimeUse.original != original {
				t.Fatal("actual Resume replaced original status/cold/accounting owners")
			}
			// This consumes the SAME genuine Cold/OP binding without entering
			// startAgain. No native unit or VM is created by this regression.
			if err := p.cold.bindResumeOperationV1(p.lifetime, p.operation); err != nil {
				t.Fatal(err)
			}
			next, err := p.operation.LoadContext(p.lifetime)
			if err != nil {
				t.Fatal(err)
			}
			if err := applyAdmittedRestartMemoryV1(p.operation, next); err != nil {
				t.Fatal(err)
			}
			if err := p.accounting.TransferToStarting(p.lifetime, p.operation, p.cold, next); err != nil {
				t.Fatal(err)
			}
			if err := saveRestartManifestV1(p.operation, next); err != nil {
				t.Fatal("actual restart writer rejected original projection", err)
			}
			stored, err := p.operation.LoadContext(p.lifetime)
			if err != nil {
				t.Fatal(err)
			}
			if !reflectRuntimeManifestEqualV1(*stored.RuntimeManifest, selected) || len(stored.RuntimeManifest.Items) != 3 {
				t.Fatal("restart persisted full collection instead of selected roles")
			}
			data, err := os.ReadFile(filepath.Join(paths.SessionDir(id), "manifest.json"))
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(data, []byte("runtime_origin_v1")) {
				t.Fatal("origin data was persisted as authority")
			}
			current, err := b.runtime.selectedRuntime(b.ctx, full)
			if err != nil || current != original || len(original.files) != len(full.Items) {
				t.Fatal("restart replaced original full producer", err)
			}
			if canceled := call("resume.prepared.cancel-v1", prepared.Prepared); !canceled.OK {
				t.Fatal("original native-file resume cleanup refused", canceled.Error)
			}

		})
	}
}
