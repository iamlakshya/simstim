package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

// This fixture uses real local metadata storage, retained settings, accepted
// generated transport provenance and the common guarded request writer. Its
// scripted peer supplies public replies; it proves no native SSH or VM fact.
type armSourceFixtureV1 struct {
	conn         *Conn
	request      *GrantRequest
	ctx          context.Context
	marker       string
	mu           sync.Mutex
	cohort       *api.SandboxCohortView
	armFrames    atomic.Int32
	snapshots    atomic.Int32
	partialArm   bool
	dropAck      bool
	onSnapshot   func()
	onArmAck     func()
	mismatchAck  bool
	policy       api.Policy
	config       string
	trustedArmed bool
}

func newArmSourceFixtureV1(t *testing.T, shared bool) *armSourceFixtureV1 {
	t.Helper()
	h := h5GrantState(t)
	tier := TierOwn
	if shared {
		tier = TierShared
		if err := SetTierContext(t.Context(), h.Name, tier, strings.Repeat("a", 32), ""); err != nil {
			t.Fatal(err)
		}
	}
	marker := filepath.Join(t.TempDir(), "original-arm-command-count")
	command := "printf ABCD0000; printf x >> " + ShellQuote(marker)
	if runtime.GOOS == "windows" {
		command = `<nul set /p "=ABCD0000" & <nul set /p "=x" >> "` + marker + `" & exit /b 0`
	}
	config := "[secrets.KEY]\nrun=" + fmt.Sprintf("%q", command) + "\nhosts=['model.example']\nheader='Authorization'\nformat='Bearer {value}'\non_hosts=['box']\n"
	if shared {
		config += "[secret_tiers]\nKEY='shared'\n"
	}
	writeRetainedImagePolicyFixture(t, config)
	c := finishGeneratedTransportFixture(t, h, strings.Repeat("a", 32), "session.id", "secrets", api.FeatureBoundSecrets, api.FeatureSandboxSnapshot, api.FeatureSandboxCohort, api.FeatureJudgeKeyV1, "policy")
	r, err := NewSecretArmGrantRequest("arm23456")
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithGrantRequest(t.Context(), r)
	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	})
	bind := api.SecretBind{Hosts: []string{"model.example:443"}, Header: "Authorization", Format: "Bearer {value}"}
	intent := api.SecretDeliveryIntent{Version: 1, Modes: map[string]api.SecretMode{"KEY": api.SecretModeBound}, Binds: map[string]api.SecretBind{"KEY": bind}}
	creation := SandboxPolicy{SessionID: "arm23456", HostID: c.HostID(), Ceiling: tier, CreationWall: "gvisor", Secrets: []string{"KEY"}, CreatedAt: time.Unix(1, 0).UTC(), request: strings.Repeat("b", 32), DeliveryIntent: &intent}
	owned, release, err := c.beginGrant(ctx, "original arm fixture creation", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := commitSandboxBoundSchema(owned, h); err != nil {
		release()
		t.Fatal(err)
	}
	if err := reserveSandbox(owned, h, creation); err != nil {
		release()
		t.Fatal(err)
	}
	release()
	state := &api.SessionSecretState{Version: 1, Generation: strings.Repeat("d", 32), Intent: api.CloneSecretDeliveryIntent(intent), Primary: map[string]api.PublicSecretState{"KEY": {Mode: api.SecretModeBound, Sentinel: "STND1111", Identity: strings.Repeat("b", 32), Shape: &api.SecretShape{Version: 1, Slots: []api.SecretShapeSlot{{Class: "ascii-upper", Count: 4, Width: 1}, {Class: "ascii-digit", Count: 4, Width: 1}}}}}}
	f := &armSourceFixtureV1{conn: c, request: r, ctx: ctx, marker: marker, config: config, cohort: &api.SandboxCohortView{Snapshot: sharedGrantSnapshot(creation.SessionID), RunGeneration: strings.Repeat("c", 32), SecretState: state}}
	c.pending = map[uint64]chan proto.Response{}
	c.w = proto.NewWriter(grantWriteFunc(f.write))
	return f
}

func (f *armSourceFixtureV1) write(data []byte) (int, error) {
	frame, err := proto.Read(bufio.NewReader(bytes.NewReader(data)))
	if err != nil {
		return 0, err
	}
	var req proto.Request
	if json.Unmarshal(frame.Payload, &req) != nil {
		return 0, errors.New("invalid fixture request")
	}
	var result any
	switch req.Op {
	case "sandbox.snapshot":
		f.snapshots.Add(1)
		if f.onSnapshot != nil {
			f.onSnapshot()
		}
		var args api.SandboxSnapshotArgs
		if json.Unmarshal(req.Args, &args) != nil || !args.Cohort || args.ID != "arm23456" {
			return 0, errors.New("fixture lost its original cohort restriction")
		}
		f.mu.Lock()
		view := f.cohort.Clone()
		if query := args.TrustedReadiness; query != nil {
			expected, valid := f.expectedTrustedUse()
			if !valid || query.Version != 1 || query.Generation != view.SecretState.Generation || len(query.Uses) != 1 || !reflect.DeepEqual(query.Uses["KEY"], expected) {
				f.mu.Unlock()
				return 0, errors.New("fixture readiness did not restrict the actual current tuple")
			}
			unarmed := []string{}
			if !f.trustedArmed || !reflect.DeepEqual(view.SecretState.Intent.TrustedUses["KEY"], expected) {
				unarmed = []string{"KEY"}
			}
			view.TrustedReadiness = &api.TrustedSecretReadinessV1{Version: 1, Generation: query.Generation, Unarmed: unarmed}
		}
		result = view
		f.mu.Unlock()
	case "policy.ls":
		f.mu.Lock()
		result = api.ClonePolicy(f.policy)
		f.mu.Unlock()
	case "arm":
		f.armFrames.Add(1)
		if f.partialArm {
			return max(len(data)/2, 1), io.ErrClosedPipe
		}
		if f.dropAck {
			return len(data), nil
		}
		var args api.ArmSecretsArgs
		if json.Unmarshal(req.Args, &args) != nil || args.ID != "arm23456" || len(args.Secrets) > 1 || len(args.TrustedUses) > 1 || len(args.TrustedUses) != len(args.ProtectedTrustedSecretValues) {
			return 0, errors.New("fixture received an arm outside original roles")
		}
		if len(args.Secrets) != 0 && args.Secrets["KEY"] != api.Secret("ABCD0000") {
			return 0, errors.New("fixture received changed original guest values")
		}
		if len(args.TrustedUses) != 0 {
			expected, valid := f.expectedTrustedUse()
			if !valid || !reflect.DeepEqual(args.TrustedUses["KEY"], expected) || args.ProtectedTrustedSecretValues["KEY"] != api.Secret("ABCD0000") {
				return 0, errors.New("fixture received changed original trusted values")
			}
		}
		f.mu.Lock()
		if args.Generation != f.cohort.SecretState.Generation {
			f.mu.Unlock()
			return 0, errors.New("fixture received a stale arm generation")
		}
		if len(args.Secrets) != 0 {
			public := f.cohort.SecretState.Primary["KEY"]
			public.Armed = true
			f.cohort.SecretState.Primary["KEY"] = public
		}
		if len(args.TrustedUses) != 0 {
			f.trustedArmed = true
			if f.cohort.SecretState.Intent.TrustedUses == nil {
				f.cohort.SecretState.Intent.TrustedUses = map[string]api.TrustedSecretUseV1{}
			}
			f.cohort.SecretState.Intent.TrustedUses["KEY"] = args.TrustedUses["KEY"]
		}
		f.cohort.SecretState.Generation = strings.Repeat("e", 32)
		result = api.CloneSessionSecretState(f.cohort.SecretState)
		f.mu.Unlock()
		if f.onArmAck != nil {
			f.onArmAck()
		}
	default:
		return 0, errors.New("fixture received an unrelated operation")
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return 0, err
	}
	f.conn.mu.Lock()
	ch := f.conn.pending[req.ID]
	delete(f.conn.pending, req.ID)
	f.conn.mu.Unlock()
	if ch == nil {
		return 0, errors.New("fixture response lost its original request")
	}
	responseID := req.ID
	if f.mismatchAck && req.Op == "arm" {
		responseID++
	}
	ch <- proto.Response{ID: responseID, OK: true, Result: raw}
	return len(data), nil
}

func TestOriginalArmSourcePublishesOnceAndFinalRerunReadsActualOwner(t *testing.T) {
	f := newArmSourceFixtureV1(t, false)
	state, err := f.conn.ArmSecrets(f.ctx, "arm23456", false)
	if err != nil || state == nil || !state.Primary["KEY"].Armed || f.armFrames.Load() != 1 {
		t.Fatal("actual original arm facade did not publish and acknowledge once", err)
	}
	observed := f.snapshots.Load()
	if _, err := f.conn.ArmSecrets(f.ctx, "arm23456", false); err != nil || f.armFrames.Load() != 1 || f.snapshots.Load() <= observed {
		t.Fatal("final direct rerun skipped actual-owner recheck or replayed a value frame", err)
	}
	count, err := os.ReadFile(f.marker)
	if err != nil || string(count) != "x" {
		t.Fatal("final rerun fetched another value", err)
	}
}

func TestOriginalArmSkippedRunnerCannotDonateSharedPermission(t *testing.T) {
	f := newArmSourceFixtureV1(t, true)
	for range 2 {
		_, err := f.conn.ArmSecrets(f.ctx, "arm23456", false)
		var question *scopedGrantConfirmation
		if !errors.As(err, &question) || question.scope.kind != OutboundSecret || f.armFrames.Load() != 0 {
			t.Fatal("a skipped or nil question runner approved the actual arm", err)
		}
	}
	if _, err := os.Stat(f.marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unanswered Shared question ran a value command", err)
	}
}

func TestOriginalArmPartialWriteAndUnknownACKCannotReplay(t *testing.T) {
	for _, mode := range []string{"partial", "unknown_ack"} {
		t.Run(mode, func(t *testing.T) {
			f := newArmSourceFixtureV1(t, false)
			f.partialArm, f.dropAck = mode == "partial", mode == "unknown_ack"
			ctx, cancel := context.WithTimeout(f.ctx, 200*time.Millisecond)
			defer cancel()
			if _, err := f.conn.ArmSecrets(ctx, "arm23456", false); err == nil {
				t.Fatal("uncertain publication became a successful arm")
			}
			if _, err := f.conn.ArmSecrets(f.ctx, "arm23456", false); err == nil || f.armFrames.Load() != 1 {
				t.Fatal("ignored fatal publication error replayed a value frame", err)
			}
			count, err := os.ReadFile(f.marker)
			if err != nil || string(count) != "x" {
				t.Fatal("uncertain publication refetched its credential", err)
			}
		})
	}
}

func TestOriginalArmSourceChangeBeforeValuesRefuses(t *testing.T) {
	f := newArmSourceFixtureV1(t, false)
	f.onSnapshot = func() { writeRetainedImagePolicyFixture(t, "# original source changed before values\n") }
	if _, err := f.conn.ArmSecrets(f.ctx, "arm23456", false); err == nil || f.armFrames.Load() != 0 {
		t.Fatal("changed original settings reached an arm frame", err)
	}
	if _, err := os.Stat(f.marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("changed original settings ran a value command", err)
	}
}

func (f *armSourceFixtureV1) expectedTrustedUse() (api.TrustedSecretUseV1, bool) {
	values := f.policy.Classifier
	if values.Model == nil || values.Endpoint == nil || values.Key == nil || *values.Key != "KEY" {
		return api.TrustedSecretUseV1{}, false
	}
	return api.TrustedSecretUseV1{Version: 1, Use: api.TrustedJudgeUseV1, Mode: api.SecretModeBound, Bind: f.cohort.SecretState.Intent.Binds["KEY"], Model: *values.Model, Endpoint: *values.Endpoint}, true
}
