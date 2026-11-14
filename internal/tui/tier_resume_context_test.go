package tui

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/client"
)

type tierResumeObservation struct {
	ctx         context.Context
	interactive bool
}

type tierResumeLifecycle struct {
	*ownedTierLifecycle
	observed chan tierResumeObservation
}

func (l *tierResumeLifecycle) Transport(ctx context.Context, interactive bool) (client.Transport, error) {
	l.observed <- tierResumeObservation{ctx: ctx, interactive: interactive}
	return ownedTierTransport{}, nil
}

func TestPendingTransfersKeepTheLauncherTierContext(t *testing.T) {
	for _, transfer := range []string{"attach", "exec"} {
		t.Run(transfer, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			t.Setenv("XDG_CACHE_HOME", t.TempDir())
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			var warnings atomic.Int32
			raw := client.WithWarnings(context.WithValue(context.Background(), handoffContextKey{}, "kept"), func(string, string, string) { warnings.Add(1) })
			raw, cancel := context.WithCancel(raw)
			defer cancel()
			provider := "tier-resume-" + transfer
			lifecycle := &tierResumeLifecycle{ownedTierLifecycle: &ownedTierLifecycle{state: client.MachineRunning}, observed: make(chan tierResumeObservation, 4)}
			client.RegisterProvider(provider, func(client.Host) (client.Lifecycle, error) { return lifecycle, nil })
			h := client.Host{Name: "box", Machine: &client.Machine{Provider: provider, ID: "owned"}}
			if err := client.SaveHosts(context.Background(), []client.Host{h}); err != nil {
				t.Fatal(err)
			}
			// The owned pipe bridge reports a different valid identity, exercising
			// warning suppression as well as the first tier question after resume.
			pending := "migrated = 1970-01-01T00:00:01Z\n[[host]]\nname = \"box\"\ntier = \"shared\"\nneeds_answer = true\nhost_id = \"22222222222222222222222222222222\"\n"
			if err := os.WriteFile(filepath.Join(client.ConfigDir(), "tiers.toml"), []byte(pending), 0o600); err != nil {
				t.Fatal(err)
			}
			be := newFakeBackend()
			be.hosts = []client.Host{h}
			be.tiers = map[string]client.TierEntry{h.Name: {Name: h.Name, Tier: client.TierShared, NeedsAnswer: true}}
			m := New(raw, be, Options{})
			defer m.Close()
			m.pauseBg()
			if transfer == "attach" {
				m.pendingAttach = &pendingAttach{host: h.Name, id: "abcd2345", opts: client.AttachOptions{Host: h, Session: "abcd2345", State: m.attach}}
				if program, err := m.runPendingAttach(raw, nil, nil, false); err != nil || program == nil {
					t.Fatalf("attach resume: %v", err)
				}
			} else {
				m.pendingExec = &pendingExec{command: noopHandoffExec()}
				if program, err := m.runPendingExec(raw, nil, nil, false); err != nil || program == nil {
					t.Fatalf("exec resume: %v", err)
				}
			}
			done := make(chan tierAnswer, 1)
			go func() { tier, err := client.ResolveHostTier(m.backgroundContext(), h); done <- tierAnswer{tier, err} }()
			var request tierQuestionRequest
			select {
			case request = <-m.tierRequests:
			case result := <-done:
				t.Fatalf("resumed context skipped its model-owned tier question: %+v", result)
			case <-time.After(5 * time.Second):
				t.Fatal("resumed foreground use did not reach its tier question")
			}
			observation := tierBuffered(t, lifecycle.observed, "live tier verification")
			if observation.interactive || client.InputAllowed(observation.ctx) {
				t.Fatal("tier verification after resume could borrow the launcher terminal")
			}
			if observation.ctx.Value(handoffContextKey{}) != "kept" {
				t.Fatal("resumed verification lost the original context values")
			}
			if warnings.Load() != 0 {
				t.Fatal("resumed verification wrote a warning outside the launcher")
			}
			_ = m.onTierQuestion(request)
			q := m.modal.(*hostTierQuestion)
			if q.pick != -1 {
				t.Fatal("resumed tier sheet selected a default answer")
			}
			q.pick = 1 // shared, explicitly chosen on the model's own sheet
			_ = q.finish(m)
			select {
			case result := <-done:
				if result.err != nil || result.tier != client.TierShared {
					t.Fatalf("resumed tier answer: %+v", result)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("resumed model answer did not persist")
			}
			tiers, err := client.LoadTiersContext(m.backgroundContext(), []client.Host{h})
			if err != nil || tiers.NeedsAnswer(h.Name) || tiers.Of(h.Name) != client.TierShared {
				t.Fatalf("resumed explicit answer was not saved: pending=%v tier=%v err=%v", tiers.NeedsAnswer(h.Name), tiers.Of(h.Name), err)
			}
			entry, ok := tiers.Entry(h.Name)
			if !ok || entry.HostID != "11111111111111111111111111111111" || entry.IDChanged {
				t.Fatalf("resumed verification or explicit answer lost identity bookkeeping: %+v", entry)
			}
			if warnings.Load() != 0 {
				t.Fatal("a host identity warning escaped after resume")
			}
		})
	}
}
