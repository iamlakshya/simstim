package client

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/runtimecache"
	"github.com/iamlakshya/simstim/internal/supply"
	"github.com/iamlakshya/simstim/internal/vmneed"
)

// Metadata fixtures feed the actual client constructors and in-memory peer.
// They are neither signed release bytes nor native runtime evidence.
func vmDriverDeliveryFixtureV1(t *testing.T, driver string) (vmneed.NeedV1, supply.Manifest) {
	t.Helper()
	need := vmPrepareFixtureNeed()
	full := need.Runtime
	full.Items = append([]supply.Item(nil), full.Items...)
	folder := supply.ReleaseFolder(full.Release) + "/"
	fc := supply.NewItem(supply.KindVMM, "firecracker", "1.18.0", "amd64", []byte("firecracker"), folder+"firecracker-amd64")
	fc.EOL, fc.Released, fc.HostKernel = "2027-01-06", "2026-11-11", "5.10,6.1,6.18"
	efi := supply.NewItem(supply.KindKernel, "guest-efi", "6.18.63", "amd64", []byte("kernel-efi"), folder+"kernel-efi-amd64")
	efi.Licence, efi.SourceURL, efi.Released = "GPL-2.0-only", folder+"linux-6.18.63.tar.xz", "2026-11-10"
	full.Items = append(full.Items, fc, efi)
	selected, err := vmneed.SelectRuntimeForDriver(driver, full, "amd64")
	if err != nil {
		t.Fatal(err)
	}
	need.Driver, need.Runtime = driver, selected
	digest := "sha256:" + strings.Repeat("f", 64)
	need.Image.Ref, need.Image.Digest, need.Image.PlatformDigest = brand.ImageRepository+"@"+digest, digest, digest
	full.Items = append(full.Items, supply.Item{Kind: supply.KindImage, Name: "agent-base", Arch: "amd64", Digest: digest, RootHash: need.Image.RootHash, DataBlocks: need.Image.DataBlocks})
	return need, full
}
func vmDriverRuntimeItemsFixtureV1(full supply.Manifest) []supply.Item {
	items := []supply.Item{}
	for _, item := range full.Items {
		if runtimecache.IsRuntime(item.Kind) {
			items = append(items, item)
		}
	}
	return items
}
func vmDriverImageRecordFixtureV1(t *testing.T, request image.Request) *image.Record {
	t.Helper()
	size, err := image.ConvertedSizeForDataBlocksV1(request.DataBlocks)
	if err != nil {
		t.Fatal(err)
	}
	record := &image.Record{Ref: request.Ref, Digest: request.Digest, PlatformDigest: request.PlatformDigest, Arch: request.Arch, RootHash: request.RootHash, DataBlocks: request.DataBlocks, Size: size}
	if err := image.CheckRecord(request, *record); err != nil {
		t.Fatal("invalid image fixture", err)
	}
	return record
}

func vmDriverProbeFixtureV1(driver string) api.CapProbe {
	return api.CapProbe{OS: "linux", Arch: "amd64", Kernel: "6.18.0", KVM: api.KVM{Present: true, Usable: true}, Nitro: &api.Nitro{Present: true, Usable: true}, Walls: []api.WallProbe{{Driver: driver, Level: "microvm", OK: true}}}
}
func assertDriverRuntimeFixtureV1(t *testing.T, need vmneed.NeedV1, full supply.Manifest, driver string) {
	t.Helper()
	if need.Driver != driver || len(need.Runtime.Items) != 3 {
		t.Fatal("actual builder lost the selected driver", need.Driver, len(need.Runtime.Items))
	}
	roles, err := vmneed.RolesForDriver(driver)
	if err != nil {
		t.Fatal(err)
	}
	for _, role := range roles {
		original, found := full.Find(role.Kind, role.Name, "amd64")
		actual, selected := need.Runtime.Find(role.Kind, role.Name, "amd64")
		if !found || !selected || !reflect.DeepEqual(original, actual) {
			t.Fatal("actual builder replaced an original signed role", driver, role)
		}
	}
	if err := need.Validate("amd64"); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareNewVMActualPathRetainsSelectedDriverAndFullDelivery(t *testing.T) {
	for _, driver := range []string{"chv", "firecracker", "nitro"} {
		t.Run(driver, func(t *testing.T) {
			h := h5GrantState(t)
			writeRetainedImagePolicyFixture(t, "")
			old, full := vmDriverDeliveryFixtureV1(t, driver)
			signedBefore, err := json.Marshal(full)
			if err != nil {
				t.Fatal(err)
			}
			useRuntimeDeliveryManifest(t, full)
			c := finishGeneratedTransportFixture(t, h, strings.Repeat("a", 32), "session.id", "sandbox", "images", api.FeatureVMNeedV1, api.FeatureAgentsDiskV1, api.FeatureRuntimeOriginV1)
			c.pending = map[uint64]chan proto.Response{}
			r := NewGrantRequest(false, false)
			t.Cleanup(func() {
				if err := r.Close(); err != nil {
					t.Error(err)
				}
			})
			ctx := WithGrantRequest(t.Context(), r)
			if err := r.BindNewWall(ctx, "microvm", nil); err != nil {
				t.Fatal(err)
			}
			runtimeCalls, agentCalls, imageCalls := 0, 0, 0
			sharedGrantWire(t, c, func(request proto.Request) any {
				switch request.Op {
				case "sandbox.probe":
					return vmDriverProbeFixtureV1(driver)
				case "runtime.status":
					runtimeCalls++
					var req runtimecache.RuntimeRequest
					if json.Unmarshal(request.Args, &req) != nil || !reflect.DeepEqual(req.Manifest.Items, vmDriverRuntimeItemsFixtureV1(full)) || r.runtimeUpgrade == nil || r.namedResume != nil {
						t.Error("New did not retain its original full signed delivery")
					}
					return runtimeDeliveryStatus(req.Manifest, true)
				case "image.status":
					imageCalls++
					var req image.StatusRequest
					if json.Unmarshal(request.Args, &req) != nil || req.Request == nil || !reflect.DeepEqual(*req.Request, old.Image) {
						t.Error("New selected image outside its original client projection")
					}
					return image.CacheStatus{Present: true, Image: vmDriverImageRecordFixtureV1(t, old.Image)}
				case "agents.disk.ensure":
					agentCalls++
					return image.AgentDiskV1{Version: 1, Arch: "amd64", SourceDigest: strings.Repeat("e", 64), Content: old.Agents, Agents: []string{"codex"}}
				default:
					t.Error("unexpected actual New VM preparation", request.Op)
					return struct{}{}
				}
			})
			memory, disk, cpus := "2G", "16M", 2
			a := NewArgs{Wall: "microvm", Agent: "codex", Layers: []api.Layer{{Kind: api.LayerSession, LayerValues: api.LayerValues{Sandbox: api.SandboxValues{Memory: &memory, DiskMax: &disk, CPUs: &cpus}}}}}
			plan, err := c.PrepareNewVM(ctx, a, "microvm")
			if err != nil {
				t.Fatal(err)
			}
			need := plan.Need()
			assertDriverRuntimeFixtureV1(t, need, full, driver)
			if need.GuestRAM != 2<<30 || need.CPUs != cpus || need.DiskMax != 16<<20 || r.preparedVM != plan || r.newSecrets != nil || runtimeCalls < 2 || imageCalls != 1 || agentCalls != 1 {
				t.Fatal("actual New preparation skipped an original producer or changed dimensions", need.GuestRAM, runtimeCalls, imageCalls, agentCalls)
			}
			origin, err := plan.runtimeOriginForV1(ctx, c)
			if err != nil || func() bool {
				value, e := api.DecodeRuntimeOriginV1(origin)
				return e != nil || !reflect.DeepEqual(value.Manifest.Items, vmDriverRuntimeItemsFixtureV1(full))
			}() {
				t.Fatal("actual New lost its original full runtime carrier", err)
			}
			foreign := NewGrantRequest(false, false)
			if _, err := plan.runtimeOriginForV1(WithGrantRequest(t.Context(), foreign), c); err == nil {
				t.Fatal("another GrantRequest borrowed original runtime bytes")
			}
			if err := foreign.Close(); err != nil {
				t.Fatal(err)
			}
			other := finishGeneratedTransportFixture(t, h, c.HostID(), c.Features...)
			if _, err := plan.runtimeOriginForV1(ctx, other); err == nil {
				t.Fatal("another connection borrowed original runtime bytes")
			}
			stamp := c.hostTransport
			c.hostTransport = other.hostTransport
			_, stampErr := plan.runtimeOriginForV1(ctx, c)
			c.hostTransport = stamp
			if stampErr == nil {
				t.Fatal("a replacement transport stamp borrowed original runtime bytes")
			}
			hostID := c.hostID
			c.hostID = strings.Repeat("b", 32)
			_, identityErr := plan.runtimeOriginForV1(ctx, c)
			c.hostID = hostID
			if identityErr == nil {
				t.Fatal("a changed HostID borrowed original runtime bytes")
			}
			raw, err := plan.EncodedFor(c)
			if err != nil {
				t.Fatal(err)
			}
			roundtrip, err := vmneed.DecodeV1(raw, "amd64")
			if err != nil || roundtrip.Driver != driver {
				t.Fatal("actual outgoing New Need lost its chosen driver", err)
			}
			signedAfter, err := json.Marshal(full)
			if err != nil || !bytes.Equal(signedBefore, signedAfter) || !reflect.DeepEqual(r.runtimeUpgrade.runtime.Manifest.Items, vmDriverRuntimeItemsFixtureV1(full)) {
				t.Fatal("driver projection rewrote the original signed delivery collection", err)
			}
		})
	}
}
