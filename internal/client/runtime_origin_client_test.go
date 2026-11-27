package client

import (
	"errors"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestRuntimeOriginFeatureMissingNeverDowngradesEligibleFixedVMV1(t *testing.T) {
	target, _ := prepareSavedFixture(t, Host{Name: "box", SSH: "box.example"}, TierOwn, strings.Repeat("a", 32), false)
	c := finishGeneratedTransportFixture(t, target.Host(), target.HostID(), "sandbox", api.FeatureVMNeedV1, api.FeatureAgentsDiskV1)
	probe := api.CapProbe{OS: "linux", Arch: "amd64", Walls: []api.WallProbe{{Driver: "chv", Level: "microvm", CanPrepare: true}, {Driver: "gvisor", Level: "gvisor", OK: true}}}
	for _, requested := range []string{"", "microvm"} {
		level, _, err := selectSandboxPrepareLevelV1(target, c, probe, requested)
		var user *UserError
		if !errors.As(err, &user) || user.Code != "helper_old" || level != "" || c.nextID.Load() != 0 {
			t.Fatal("old helper silently lowered eligible VM preparation", requested, level, err)
		}
	}
}
func TestNewRuntimeOriginFeatureMissingRefusesBeforeAnyProducerRPCV1(t *testing.T) {
	h := h5GrantState(t)
	writeRetainedImagePolicyFixture(t, "")
	c := finishGeneratedTransportFixture(t, h, strings.Repeat("a", 32), "sandbox", "images", api.FeatureVMNeedV1, api.FeatureAgentsDiskV1)
	r := NewGrantRequest(false, false)
	defer r.Close()
	ctx := WithGrantRequest(t.Context(), r)
	if err := r.BindNewWall(ctx, "microvm", nil); err != nil {
		t.Fatal(err)
	}
	_, err := c.PrepareNewVM(ctx, NewArgs{Wall: "microvm", Agent: "codex"}, "microvm")
	var user *UserError
	if !errors.As(err, &user) || user.Code != "helper_old" || c.nextID.Load() != 0 {
		t.Fatal("New emitted a producer request without origin negotiation", err)
	}
}
func TestColdRuntimeOriginFeatureMissingPreservesWarmCompatibilityV1(t *testing.T) {
	c, _, ctx, creation := resumeImageFixture(t, true, "", api.FeatureResumePreparationV1, api.FeatureNamedSSHKeysV1, api.FeatureResumeVMSizeV1)
	c.pending = map[uint64]chan proto.Response{}
	calls := 0
	sharedGrantWire(t, c, func(request proto.Request) any {
		switch request.Op {
		case "resume.prepare-v1":
			calls++
			return api.ResumePrepareResultV1{V: 1, Mode: "cold", Primary: creation.SessionID, Prepared: &api.PreparedResumeV1{V: 1, Token: strings.Repeat("b", 64)}}
		case "resume.prepared.cancel-v1":
			return struct{}{}
		default:
			t.Error("cold origin feature refusal emitted VM/value request", request.Op)
			return struct{}{}
		}
	})
	_, err := c.PrepareNativeResume(ctx, ResumeArgs{ID: creation.SessionID})
	var user *UserError
	if !errors.As(err, &user) || user.Code != "helper_old" || calls != 1 {
		t.Fatal("cold VM continued without origin negotiation", err, calls)
	}
	// The genuine warm compatibility cases remain in the unchanged actual
	// TestWarmResumeSizeWarnsWithoutPreparingFreshVMOrStopping producer test.
}
