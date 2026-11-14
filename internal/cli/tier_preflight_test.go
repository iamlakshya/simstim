package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/iamlakshya/simstim/internal/client"
)

func TestHostSetPreflightReadFailureChangesNoPair(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancelled", true: "malformed"}[malformed], func(t *testing.T) {
			withSetHosts(t)
			if err := client.SetTierContext(context.Background(), "vm", client.TierOwn, "", ""); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(client.ConfigDir(), "tiers.toml")
			valid, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var tierSetting *hostSetting
			for i := range hostSettings {
				if hostSettings[i].key == "tier" {
					tierSetting = &hostSettings[i]
				}
			}
			if tierSetting == nil || tierSetting.current == nil {
				t.Fatal("tier has no context-aware reader")
			}
			old := tierSetting.current
			t.Cleanup(func() { tierSetting.current = old })
			var fixtureErr error
			tierSetting.current = func(ctx context.Context, h client.Host) (string, error) {
				if malformed {
					fixtureErr = os.WriteFile(path, []byte("[[host\n"), 0o600)
				} else {
					cancel()
				}
				value, err := old(ctx, h)
				if malformed {
					// Recover the file before returning the read error. A later
					// save cannot hide a missing preflight error check.
					if writeErr := os.WriteFile(path, valid, 0o600); writeErr != nil {
						fixtureErr = writeErr
					}
				}
				return value, err
			}
			var out bytes.Buffer
			p := &prompter{ctx: ctx, out: &out}
			err = setHostWith(ctx, p, &out, "vm", []string{"idle=2h", "tier=shared"}, "")
			if fixtureErr != nil {
				t.Fatal(fixtureErr)
			}
			if !malformed && !errors.Is(err, context.Canceled) {
				t.Fatalf("preflight lost cancellation: %v", err)
			}
			var user *client.UserError
			if malformed && (!errors.As(err, &user) || user.Code != "bad_config") {
				t.Fatalf("preflight lost its read error: %v", err)
			}
			h, err := client.FindHost("vm")
			if err != nil || h.Machine.IdleStop != "30m" || strings.Contains(out.String(), "shared") {
				t.Fatalf("failed preflight applied a pair: host=%+v output=%q err=%v", h, out.String(), err)
			}
		})
	}
}

func TestPrepareSetPairsRefusesCancellationBeforeCallbacks(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	pairs := []pair{{s: &hostSetting{now: func(client.Host) string { called = true; return "old" }}}}
	_, before, saves, err := prepareSetPairs(ctx, client.Host{}, pairs)
	if !errors.Is(err, context.Canceled) || called || before != nil || saves {
		t.Fatalf("cancelled preflight: before=%v saves=%v called=%v err=%v", before, saves, called, err)
	}
}

func TestTierCurrentReadErrorNeverBecomesAValue(t *testing.T) {
	withSetHosts(t)
	if err := client.SetTierContext(context.Background(), "vm", client.TierOwn, "", ""); err != nil {
		t.Fatal(err)
	}
	h, err := client.FindHost("vm")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if value, err := tierNowContext(ctx, h); !errors.Is(err, context.Canceled) || value != "" {
		t.Fatalf("cancelled tier lookup returned a display value: value=%q err=%v", value, err)
	}
	if err := os.WriteFile(filepath.Join(client.ConfigDir(), "tiers.toml"), []byte("[[host\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	value, err := tierNowContext(context.Background(), h)
	var user *client.UserError
	if !errors.As(err, &user) || user.Code != "bad_config" || value != "" {
		t.Fatalf("malformed tiers returned a display value: value=%q err=%v", value, err)
	}
}

func TestOrphanTierDoesNotAnswerTheHostAddQuestion(t *testing.T) {
	_, tr := withFake(t, nil)
	h := client.Host{Name: "box", Machine: &client.Machine{Provider: "fake", ID: "fake-box"}}
	if err := client.SaveHosts(context.Background(), []client.Host{h}); err != nil {
		t.Fatal(err)
	}
	if err := client.SetTierContext(context.Background(), "box", client.TierOwn, strings.Repeat("1", 32), ""); err != nil {
		t.Fatal(err)
	}
	// A legacy writer or a hand edit removes the saved host while the
	// separate tier file keeps its old, fully answered own row.
	if err := client.SaveHosts(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	tiers, err := client.LoadTiersContext(context.Background(), nil)
	if err != nil || !tiers.Has(h.Name) || tiers.Of(h.Name) != client.TierOwn {
		t.Fatalf("fixture lost its orphan own row: has=%v tier=%s err=%v", tiers.Has(h.Name), tiers.Of(h.Name), err)
	}
	if has, err := hasSavedHostTier(context.Background(), "box"); err != nil || has {
		t.Fatalf("orphan tier counted as a saved host: has=%v err=%v", has, err)
	}
	p := &prompter{ctx: context.Background(), out: io.Discard}
	err = addHostWithTier(context.Background(), p, h, true, "", client.TierUnknown, true)
	var user *client.UserError
	if !errors.As(err, &user) || user.Code != "needs_answer" || !strings.Contains(user.Hint, "--tier") {
		t.Fatalf("host add inherited its orphan tier: %v", err)
	}
	if len(tr.Remotes()) != 0 {
		t.Fatalf("missing tier connected to the fake host: %v", tr.Remotes())
	}
}

type tierDoctorProbe struct{ calls *atomic.Int32 }

func (p tierDoctorProbe) MarshalJSON() ([]byte, error) {
	p.calls.Add(1)
	return json.Marshal(map[string]string{"distro": "test"})
}

func TestHostShowReturnsItsPostDialTierReloadError(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancelled", true: "malformed"}[malformed], func(t *testing.T) {
			_, tr := withFake(t, nil)
			tr.HostID = strings.Repeat("2", 32)
			h := client.Host{Name: "box", Machine: &client.Machine{Provider: "fake", ID: "fake-box"}}
			if err := client.SaveHosts(context.Background(), []client.Host{h}); err != nil {
				t.Fatal(err)
			}
			if err := client.SetTierContext(context.Background(), h.Name, client.TierOwn, strings.Repeat("1", 32), ""); err != nil {
				t.Fatal(err)
			}
			tiers, err := client.LoadTiersContext(context.Background(), []client.Host{h})
			if err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			tr.Answers = map[string]any{"doctor": tierDoctorProbe{calls: &calls}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var fixtureErr error
			warned := false
			ctx = client.WithWarnings(ctx, func(_, _, _ string) {
				warned = true
				if malformed {
					fixtureErr = os.WriteFile(filepath.Join(client.ConfigDir(), "tiers.toml"), []byte("[[host\n"), 0o600)
				} else {
					cancel()
				}
			})
			_, err = lookAtHost(ctx, h, []client.Host{h}, tiers)
			if fixtureErr != nil || !warned {
				t.Fatalf("post-Dial fixture was not reached: warned=%v err=%v", warned, fixtureErr)
			}
			if !malformed && !errors.Is(err, context.Canceled) {
				t.Fatalf("host show ignored reload cancellation: %v", err)
			}
			var user *client.UserError
			if malformed && (!errors.As(err, &user) || user.Code != "bad_config") {
				t.Fatalf("host show ignored malformed tiers after Dial: %v", err)
			}
			if calls.Load() != 0 {
				t.Fatalf("host show continued to Doctor after reload failure: %d calls", calls.Load())
			}
		})
	}
}
