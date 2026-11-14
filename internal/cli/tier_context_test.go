package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/iamlakshya/simstim/internal/client"
)

func TestTierCallerLoadErrorsFailClosed(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancelled", true: "malformed"}[malformed], func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			h := client.Host{Name: "box", SSH: "box"}
			if err := client.SaveHosts(context.Background(), []client.Host{h}); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if malformed {
				if err := os.WriteFile(filepath.Join(client.ConfigDir(), "tiers.toml"), []byte("[[host\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				cancel()
			}
			check := func(err error) {
				t.Helper()
				if !malformed {
					if !errors.Is(err, context.Canceled) {
						t.Fatalf("caller ignored cancelled tier load: %v", err)
					}
					return
				}
				var user *client.UserError
				if !errors.As(err, &user) || user.Code != "bad_config" {
					t.Fatalf("caller ignored malformed tier load: %v", err)
				}
			}
			if has, err := hasSavedHostTier(ctx, h.Name); has || err == nil {
				t.Fatalf("tier loader error became an answer: has=%v err=%v", has, err)
			} else {
				check(err)
			}
			p := &prompter{ctx: ctx, out: io.Discard, path: "host set"}
			pairs, err := readPairs(h, []string{"tier=own"})
			if err != nil {
				t.Fatal(err)
			}
			check(checkSetConfirm(ctx, p, h, pairs, ""))
			check(addHostWithTier(ctx, p, h, false, "", client.TierUnknown, true))
			check(setHostWith(ctx, p, io.Discard, h.Name, []string{"tier=own"}, ""))
			saved, err := client.FindHost(h.Name)
			if err != nil || !client.SameEntry(saved, h) {
				t.Fatalf("refused tier operation changed the saved host: %+v %v", saved, err)
			}
		})
	}
}
