package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/runtimecache"
	"github.com/iamlakshya/simstim/internal/vmneed"
)

func TestResumeVMSizeUsesGuestRAMAndCanonicalPolicyDefaults(t *testing.T) {
	need := vmPrepareFixtureNeed()
	encoded, err := vmneed.EncodeV1(need, "amd64")
	if err != nil {
		t.Fatal(err)
	}
	before := "1G"
	prior := &api.ResumeVMSelectionV1{V: 1, Need: encoded, PolicyMemory: &before}
	for _, tc := range []struct {
		name   string
		memory int64
		policy string
		want   int64
	}{
		{"preserve", 0, "1G", 1 << 30},
		{"explicit", 3 << 30, "1G", 3 << 30},
		{"auto", api.MemoryAuto, "2G", 2 << 30},
		{"policy", 0, "2G", 2 << 30},
	} {
		t.Run(tc.name, func(t *testing.T) {
			layers := []api.Layer{{Kind: api.LayerAll, LayerValues: api.LayerValues{Sandbox: api.SandboxValues{Memory: &tc.policy}}}}
			got, err := resolveResumeVMOptionsV1(ResumeArgs{MemoryMax: tc.memory}, layers, "codex", prior)
			if err != nil || got.GuestRAM != tc.want || got.CPUs != need.CPUs || got.DiskMax != need.DiskMax || got.Driver != need.Driver {
				t.Fatal("resume lost its canonical size or original dimensions", got, err)
			}
			budget, err := vmneed.BudgetFor(got.GuestRAM)
			if err != nil || budget.Guest != tc.want || budget.Limit <= tc.want {
				t.Fatal("guest RAM paid for its host reserve", budget, err)
			}
		})
	}
	for _, agent := range agents.Table {
		want, err := api.ResolveSandboxSize(api.SandboxValues{}, nil, agent.SandboxSize())
		if err != nil {
			t.Fatal(err)
		}
		got, err := resolveResumeVMOptionsV1(ResumeArgs{MemoryMax: api.MemoryAuto}, nil, agent.Kind, prior)
		if err != nil || got.GuestRAM != want.Memory {
			t.Fatal("auto substituted a machine minimum or unit cap", agent.Kind, got, err)
		}
	}
	if _, err := resolveResumeVMOptionsV1(ResumeArgs{MemoryMax: (1 << 30) + 1}, nil, "codex", prior); err == nil {
		t.Fatal("unrepresentable guest RAM was rounded")
	}
}

func TestResumeVMRuntimeCannotBorrowUnpreparedOrForeignResume(t *testing.T) {
	r := NewGrantRequest(false, false)
	defer r.Close()
	ctx := WithGrantRequest(t.Context(), r)
	if err := requireGrantOperation(ctx, grantOpResume); err != nil {
		t.Fatal(err)
	}
	c := &Conn{}
	for _, candidate := range []context.Context{ctx, context.WithValue(ctx, resumeVMPreparationKeyV1{}, &resumeNamedSSHKeyPlan{conn: c, request: r})} {
		if _, err := c.prepareVMNeedV1(candidate, VMNeedOptionsV1{}, r); err == nil || c.nextID.Load() != 0 {
			t.Fatal("copied Resume data reached the existing VM issuer", err)
		}
		if err := requireRuntimeUpgradeContext(candidate); err == nil {
			t.Fatal("unprepared Resume borrowed runtime delivery")
		}
	}
}

func TestWarmResumeSizeWarnsWithoutPreparingFreshVMOrStopping(t *testing.T) {
	for _, freshFeature := range []bool{false, true} {
		t.Run(map[bool]string{false: "older-size-helper", true: "fresh-size-helper"}[freshFeature], func(t *testing.T) {
			features := []string{api.FeatureResumePreparationV1, api.FeatureNamedSSHKeysV1}
			if freshFeature {
				features = append(features, api.FeatureResumeVMSizeV1)
			}
			c, _, ctx, creation := resumeImageFixture(t, true, "", features...)
			c.pending = map[uint64]chan proto.Response{}
			prepared := api.ResumePrepareResultV1{V: 1, Mode: "warm", Primary: creation.SessionID, Prepared: &api.PreparedResumeV1{V: 1, Token: strings.Repeat("b", 64)}}
			calls := []string{}
			sharedGrantWire(t, c, func(request proto.Request) any {
				calls = append(calls, request.Op)
				switch request.Op {
				case "resume.prepare-v1":
					var a api.ResumePrepareArgsV1
					if json.Unmarshal(request.Args, &a) != nil || a.Start.MemoryMax != 2<<30 || a.PrepareVM != freshFeature || len(a.Start.VMNeedV1) != 0 {
						t.Fatal("warm metadata changed its original size request")
					}
					return prepared
				case "resume.prepared.check-v1":
					return prepared
				case "resume.prepared.cancel-v1":
					return struct{}{}
				default:
					t.Fatal("warm resume dispatched or prepared VM inputs", request.Op)
					return nil
				}
			})
			mode, err := c.PrepareNativeResume(ctx, ResumeArgs{ID: creation.SessionID, MemoryMax: 2 << 30})
			if err != nil || mode != "warm" {
				t.Fatal(mode, err)
			}
			n := grantRequestOf(ctx).namedResume
			if n.vm != nil || !n.memoryWarned || !reflect.DeepEqual(calls, []string{"resume.prepare-v1", "resume.prepared.check-v1"}) {
				t.Fatal("warm size changed execution selection or lost warning", calls)
			}
		})
	}
}

// This exercises the real client issuer and ordered physical writer through
// an in-memory RPC peer. It proves client provenance/order, not native VM boot.
func TestColdResumeFreshVMProducerPrecedesKeysAndBindsFinalFrame(t *testing.T) {
	for _, driver := range []string{"chv", "firecracker", "nitro"} {
		for _, missing := range []bool{false, true} {
			t.Run(driver+"/"+map[bool]string{false: "cached-image", true: "missing-image-consent"}[missing], func(t *testing.T) { exerciseColdResumeFreshVMV1(t, driver, missing) })
		}
	}
}
func exerciseColdResumeFreshVMV1(t *testing.T, driver string, missing bool) {
	h := h5GrantState(t)
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	marker := filepath.Join(t.TempDir(), "key-command-count")
	command := "printf synthetic-key; printf x >> " + ShellQuote(marker)
	writeRetainedImagePolicyFixture(t, "[sandbox]\nssh_keys=['KEY']\n[secrets.KEY]\nkind='ssh-key'\non_hosts=['box']\nrun="+strconv.Quote(command)+"\n")
	need, manifest := vmDriverDeliveryFixtureV1(t, driver)
	useRuntimeDeliveryManifest(t, manifest)
	history, err := encodeSandboxImageSelection(&sandboxImageSelectionV1{Version: 1, Origin: imageOriginRelease, Release: manifest.Release, Image: need.Image.Clone()})
	if err != nil {
		t.Fatal(err)
	}
	creation := h5Policy()
	creation.CreationWall, creation.Ceiling, creation.Secrets, creation.imageSelectionRaw = "microvm", TierOwn, nil, history
	c := finishGeneratedTransportFixture(t, h, creation.HostID, "session.id", "resume", "sandbox", "images", api.FeatureResumeImageV1, api.FeatureResumePreparationV1, api.FeatureResumeVMSizeV1, api.FeatureNamedSSHKeysV1, api.FeatureVMNeedV1, api.FeatureAgentsDiskV1, api.FeatureRuntimeOriginV1, proto.FeatureImageTransferV1)
	if err := withSandboxReceiptObservation(t.Context(), c, func(owned context.Context) error { return reserveSandbox(owned, h, creation) }); err != nil {
		t.Fatal(err)
	}
	c.pending = map[uint64]chan proto.Response{}
	r := NewGrantRequest(false, false)
	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	})
	ctx := WithGrantRequest(t.Context(), r)
	priorRaw, err := vmneed.EncodeV1(need, "amd64")
	if err != nil {
		t.Fatal(err)
	}
	prepared := api.ResumePrepareResultV1{V: 1, Mode: "cold", Primary: creation.SessionID, Prepared: &api.PreparedResumeV1{V: 1, Token: strings.Repeat("b", 64)}, VM: &api.ResumeVMSelectionV1{V: 1, Need: priorRaw}}
	selected := false
	pulled := false
	fetches := 0
	finalFrames := 0
	var selectedRaw []byte
	sharedGrantWire(t, c, func(request proto.Request) any {
		switch request.Op {
		case "resume.prepare-v1":
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Error("key ran before native classification", err)
			}
			return prepared
		case "sandbox.probe":
			return vmDriverProbeFixtureV1(driver)
		case "runtime.status":
			if r.runtimeUpgrade == nil || r.namedResume == nil || r.newSecrets != nil {
				t.Error("fresh runtime borrowed a different request owner")
			}
			var runtimeRequest runtimecache.RuntimeRequest
			if err := json.Unmarshal(request.Args, &runtimeRequest); err != nil {
				t.Error(err)
			}
			if !reflect.DeepEqual(runtimeRequest.Manifest.Items, vmDriverRuntimeItemsFixtureV1(manifest)) {
				t.Error("driver projection replaced the original full signed runtime delivery")
			}
			return runtimeDeliveryStatus(runtimeRequest.Manifest, true)
		case "image.status":
			if missing && !pulled {
				return image.CacheStatus{}
			}
			var status image.StatusRequest
			if err := json.Unmarshal(request.Args, &status); err != nil {
				t.Error(err)
			}
			if status.Request == nil || !reflect.DeepEqual(*status.Request, need.Image) {
				t.Error("fresh image lost original client projection")
			}
			return image.CacheStatus{Present: true, Image: vmDriverImageRecordFixtureV1(t, need.Image)}
		case "image.pull":
			if !missing || r.imageFetch == nil || !r.confirmed[r.imageFetch.scope] || r.imageFetch.use.family != grantOpResume {
				t.Error("missing image escaped its original Resume grant")
			}
			fetches++
			pulled = true
			return *vmDriverImageRecordFixtureV1(t, need.Image)
		case "image.install":
			var install image.Install
			if json.Unmarshal(request.Args, &install) != nil || install.Action != "abort" {
				t.Error("unexpected Resume image mutation")
			}
			return struct{}{}
		case "agents.disk.ensure":
			return image.AgentDiskV1{Version: 1, Arch: "amd64", SourceDigest: strings.Repeat("e", 64), Content: need.Agents, Agents: []string{"codex"}}
		case "resume.vm.prepare-v1":
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Error("key ran before fresh VM admission", err)
			}
			var a api.ResumeVMPrepareArgsV1
			if err := json.Unmarshal(request.Args, &a); err != nil {
				t.Error(err)
			}
			fresh, err := vmneed.DecodeV1(a.Start.VMNeedV1, "amd64")
			if err != nil {
				t.Error("fresh driver Need did not decode", err)
				return struct{}{}
			}
			origin, originErr := api.DecodeRuntimeOriginV1(a.RuntimeOrigin)
			if err != nil || originErr != nil || fresh.Driver != driver || fresh.GuestRAM != 2<<30 || fresh.CPUs != need.CPUs || !reflect.DeepEqual(fresh.Image, need.Image) || !reflect.DeepEqual(origin.Manifest.Items, vmDriverRuntimeItemsFixtureV1(manifest)) || a.Start.NamedSSHKeys != nil || a.Start.Secrets != nil || !reflect.DeepEqual(a.Prepared, prepared.Prepared) {
				t.Error("fresh VM continuation changed original selection", err)
			}
			assertDriverRuntimeFixtureV1(t, *fresh, manifest, driver)
			selectedRaw = append([]byte(nil), a.Start.VMNeedV1...)
			selected = true
			prepared.VM = nil
			return prepared
		case "resume.prepared.check-v1":
			return prepared
		case "resume":
			finalFrames++
			if bytes.Contains(request.Args, []byte(`"runtime_origin_v1"`)) {
				t.Error("phase2-only carrier leaked into final Resume")
			}
			var a ResumeArgs
			if err := json.Unmarshal(request.Args, &a); err != nil {
				t.Error(err)
			}
			if !selected || !bytes.Equal(a.VMNeedV1, selectedRaw) || a.NamedSSHKeys == nil || a.NamedSSHKeys.Values["KEY"] != "synthetic-key" || len(a.Secrets) != 0 {
				t.Error("physical Resume escaped its original selected Need and key carrier")
			}
			return struct{}{}
		case "resume.prepared.cancel-v1":
			return struct{}{}
		default:
			t.Error("unexpected fresh VM operation", request.Op)
			return struct{}{}
		}
	})
	a := ResumeArgs{ID: creation.SessionID, MemoryMax: 2 << 30}
	if missing {
		_, question := c.PrepareNativeResume(ctx, a)
		var confirmation *scopedGrantConfirmation
		if !errors.As(question, &confirmation) || confirmation.scope.kind != OutboundImageFetchV1 || fetches != 0 || selected {
			t.Fatal("missing image did not stop before its original scoped consent", question)
		}
		if r.imageFetch == nil || r.imageFetch.work.Err() != nil {
			t.Fatal("the joined preparation cancelled the retained image question")
		}
		var err error
		ctx, err = ConfirmGrant(ctx, question)
		if err != nil {
			t.Fatal(err)
		}
	}
	if mode, err := c.PrepareNativeResume(ctx, a); err != nil || mode != "cold" {
		t.Fatal(mode, err)
	}
	if missing && fetches != 1 {
		t.Fatal("approved missing image did not fetch exactly once", fetches)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("native preparation fetched a key", err)
	}
	ctx, imagePlan, err := captureResumeImagePolicy(ctx, c, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	ctx, a, err = imagePlan.arguments(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	ctx, a, err = r.namedResume.arguments(ctx, c, a)
	if err != nil || ctx.Err() != nil {
		t.Fatal("the final publication inherited a cancelled preparation call", err, ctx.Err())
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "x" {
		t.Fatal("cold key did not fetch exactly once after admission", string(data), err)
	}
	if _, _, err := r.namedResume.arguments(ctx, c, a); err != nil {
		t.Fatal(err)
	}
	owned, release, err := c.beginGrant(ctx, "resume fixture", false)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := c.callWithSent(owned, "resume", a, nil, release); err != nil {
		t.Fatal(err)
	}
	if finalFrames != 1 {
		t.Fatal("the original physical Resume was not sent once", finalFrames)
	}
}

func TestColdResumeCannotIgnoreMissingSizingPreparation(t *testing.T) {
	for _, feature := range []bool{false, true} {
		t.Run(strconv.FormatBool(feature), func(t *testing.T) {
			features := []string{api.FeatureResumePreparationV1, api.FeatureNamedSSHKeysV1}
			if feature {
				features = append(features, api.FeatureResumeVMSizeV1, api.FeatureRuntimeOriginV1)
			}
			c, _, ctx, creation := resumeImageFixture(t, true, "", features...)
			c.pending = map[uint64]chan proto.Response{}
			frames := 0
			sharedGrantWire(t, c, func(request proto.Request) any {
				switch request.Op {
				case "resume.prepare-v1":
					frames++
					return api.ResumePrepareResultV1{V: 1, Mode: "cold", Primary: creation.SessionID, Prepared: &api.PreparedResumeV1{V: 1, Token: strings.Repeat("b", 64)}}
				case "resume.prepared.cancel-v1":
					return struct{}{}
				default:
					t.Error("missing cold preparation reached values or VM transport", request.Op)
					return struct{}{}
				}
			})
			if _, err := c.PrepareNativeResume(ctx, ResumeArgs{ID: creation.SessionID, MemoryMax: 2 << 30}); err == nil || frames != 1 {
				t.Fatal("cold size silently continued without original fresh VM preparation", err, frames)
			}
		})
	}
}
