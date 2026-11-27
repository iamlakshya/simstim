package supervisor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

func scanOwnedHostValues(t *testing.T, values ...string) {
	t.Helper()
	for _, root := range []string{paths.DataDir(), paths.StateDir(), paths.RuntimeDir()} {
		if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, value := range values {
				if len(value) == 0 {
					t.Fatal("persistence oracle requires nonempty actual values")
				}
				if bytes.Contains(data, []byte(value)) {
					t.Fatalf("a full actual value reached an owned host regular file: %s", path)
				}
			}
			return nil
		}); err != nil {
			t.Fatalf("could not scan every owned host regular file: %v", err)
		}
	}
}

func ownedGuestCapabilityFixture(t *testing.T, s *Supervisor) {
	t.Helper()
	a, b := net.Pipe()
	local := link.New(a, link.Supervisor, link.Options{})
	peer := link.New(b, link.Guest, link.Options{})
	s.link = local
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := local.Open(ctx, link.KindCtl, nil)
	if err != nil {
		t.Fatal(err)
	}
	remote, err := peer.Accept(ctx)
	if err != nil {
		t.Fatal(err)
	}
	s.ctl = newCtl(s, stream)
	done := make(chan struct{})
	go func() {
		defer close(done)
		reader, writer := bufio.NewReader(remote), proto.NewWriter(remote)
		for {
			frame, err := proto.Read(reader)
			if err != nil {
				return
			}
			var request proto.Request
			if frame.Type != proto.TRequest || json.Unmarshal(frame.Payload, &request) != nil {
				return
			}
			response := proto.Response{ID: request.ID}
			if request.Op == proto.CtlGuestCapabilities {
				response.OK = true
				response.Result, _ = json.Marshal(proto.CtlGuestFeatures{Version: 1, Features: []string{api.FeatureBoundSecrets}})
			} else {
				response.Error = &proto.Error{Code: "test_guest", Message: "fixture allows only the existing capability request", Hint: "Do not send an agent or Store command in this persistence oracle."}
			}
			if writer.WriteJSON(proto.TResponse, response) != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		_ = local.Close()
		_ = peer.Close()
		waitEventSignal(t, done, "owned guest capability peer")
		waitEventSignal(t, s.ctl.done, "owned guest control reader")
	})
}

func TestModernInitialMemberArmKeepAllActualValuesOutOfHostFiles(t *testing.T) {
	caBoundaryFolders(t) // isolates HOME, USERPROFILE, every XDG and git/GPG
	for _, root := range []string{paths.DataDir(), paths.StateDir(), paths.RuntimeDir()} {
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	guestRoot := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{paths.DataDir(), paths.StateDir(), paths.RuntimeDir()} {
		for _, outside := range []string{guestRoot, executable} {
			relative, err := filepath.Rel(root, outside)
			if err != nil || relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				t.Fatal("guest fixtures or executable are inside scanned host state")
			}
		}
	}
	oldEnvironment := testEgressEnv
	testEgressEnv = caBoundaryEnvironment() // real fd3 egress, no DNS/origin
	t.Cleanup(func() { testEgressEnv = oldEnvironment })
	logger := slog.New(slog.DiscardHandler)
	intent := boundStartupIntent("CREDENTIAL")
	m := &host.Manifest{Schema: 1, ID: "prv22222", Name: "privacy", CreatedAt: time.Now().UTC(), Secrets: []string{"CREDENTIAL"}, Agent: host.AgentInfo{Kind: "custom-command"},
		Wall: &host.WallRecord{Level: "process", Driver: driverPlain}, Policy: &api.Policy{Preset: api.PresetLocked},
		SecretState: &api.SessionSecretState{Version: api.SessionSecretStateV1, Intent: api.CloneSecretDeliveryIntent(*intent)}}
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}
	s := &Supervisor{id: m.ID, m: m, log: logger, firstSaved: true, lifetime: context.Background()}
	s.audit = openAudit(paths.AuditLog(m.ID), logger)
	t.Cleanup(func() { s.stopEgress(); s.wg.Wait(); s.audit.close() })
	first := strings.Join([]string{"REAL", "1234"}, "")
	memberValue := strings.Join([]string{"MEMB", "5678"}, "")
	exposed := strings.Join([]string{"explicit", "guest", "only", "value"}, "-")
	replacement := strings.Join([]string{"NEXT", "9012"}, "")
	message := startupSecretEnvelope{Env: map[string]api.Secret{"CREDENTIAL": api.Secret(first)}, SecretDelivery: intent}
	plan, err := prepareStartupSecretInput(context.Background(), *m, nil, message)
	if err != nil {
		t.Fatal(err)
	}
	s.startupSecrets = plan
	primaryValues := map[string]string{"CREDENTIAL": first}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	environment, err := s.startEgress(ctx, primaryValues)
	if err != nil {
		t.Fatal(err)
	}
	if environment.Modes["CREDENTIAL"] != api.SecretModeBound || environment.Env["CREDENTIAL"] == first || len(primaryValues) != 0 || len(plan.Values) != 0 {
		t.Fatal("modern init exposed or retained a bound input")
	}
	scanOwnedHostValues(t, first, memberValue, exposed, replacement)
	s.mu.Lock()
	s.rec.phase, s.m.Armed = host.LifeRunning, true
	s.mu.Unlock()
	ownedGuestCapabilityFixture(t, s)
	memberIntent := boundStartupIntent("MEMBER_KEY")
	memberIntent.Modes["EXPOSED_KEY"] = api.SecretModeExposed
	child := &host.Manifest{ID: "child222", SandboxOf: m.ID, Secrets: []string{"MEMBER_KEY", "EXPOSED_KEY"}, Agent: host.AgentInfo{Kind: "custom-command"}}
	memberEnv, err := s.installMemberPolicy(ctx, child, map[string]api.Secret{"MEMBER_KEY": api.Secret(memberValue), "EXPOSED_KEY": api.Secret(exposed)}, memberIntent, s.m.SecretState.Generation)
	if err != nil {
		t.Fatal(err)
	}
	if memberEnv.Env["MEMBER_KEY"] == memberValue || memberEnv.Env["EXPOSED_KEY"] != exposed {
		t.Fatal("member safe environment changed exposure or leaked bound input")
	}
	// This deliberate explicit-exposure file is in the separate owned GUEST
	// tree. No file is excluded from the three host-root scans.
	if err := os.WriteFile(filepath.Join(guestRoot, "explicit-exposure"), []byte(memberEnv.Env["EXPOSED_KEY"]), 0o600); err != nil {
		t.Fatal(err)
	}
	scanOwnedHostValues(t, first, memberValue, exposed, replacement)
	s.mu.Lock()
	generation := s.m.SecretState.Generation
	s.mu.Unlock()
	state, err := s.armSecrets(ctx, api.ArmSecretsArgs{ID: m.ID, Generation: generation, Secrets: map[string]api.Secret{"CREDENTIAL": api.Secret(replacement)}})
	if err != nil || state == nil {
		t.Fatalf("actual modern arm did not complete: %v", err)
	}
	if state.Primary["CREDENTIAL"].Sentinel != api.Secret(environment.Env["CREDENTIAL"]) {
		t.Fatal("arm changed a live stand-in or reinjected an environment")
	}
	scanOwnedHostValues(t, first, memberValue, exposed, replacement)
}
