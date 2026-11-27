package host_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/paths"
)

func TestUserDrainKeepsRunningSessionAndClearsExactMark(t *testing.T) {
	h := hosttest.New(t)
	host.ResetPlacementForTest()
	t.Cleanup(host.ResetPlacementForTest)
	if err := host.InitPlacement(); err != nil {
		t.Fatal(err)
	}
	c := dial(t, h)
	session, err := c.NewSession(t.Context(), client.NewArgs{Name: "before-drain", Argv: []string{"/bin/sh"}, Cwd: "~", Cols: 80, Rows: 24, Wall: "none", Downgrade: true})
	if err != nil {
		t.Fatal(err)
	}
	var mark api.DrainState
	if err := c.Call(t.Context(), "host.drain", api.DrainArgs{On: true, Reason: "scheduled maintenance"}, &mark); err != nil || !mark.On || mark.Reason != "scheduled maintenance" {
		t.Fatal("drain write failed", mark, err)
	}
	leaf := filepath.Join(paths.StateDir(), "drain.json")
	if info, err := os.Lstat(leaf); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("drain setting was not persisted privately", info, err)
	}
	var facts api.HostFacts
	if err := c.Call(t.Context(), "host.facts", struct{}{}, &facts); err != nil || !facts.DrainKnown || !facts.Drain {
		t.Fatal("actual facts missed the setting", facts.DrainKnown, facts.Drain, err)
	}
	_, err = c.NewSession(t.Context(), client.NewArgs{Name: "while-drained", Argv: []string{"/bin/sh"}, Cwd: "~", Wall: "none", Downgrade: true})
	var refused *client.UserError
	if err == nil || !errors.As(err, &refused) || refused.Code != "draining" {
		t.Fatal("new admitted a drained host", err)
	}
	rows, err := c.Sessions(t.Context())
	if err != nil || len(rows) != 1 || rows[0].ID != session.ID || !rows[0].Alive {
		t.Fatal("drain changed the running session", rows, err)
	}
	if err := c.Call(t.Context(), "host.drain", api.DrainArgs{}, &mark); err != nil || mark.On || mark.Reason != "" {
		t.Fatal("drain clear failed", mark, err)
	}
	if _, err := os.Lstat(leaf); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("drain clear retained the setting", err)
	}
	if _, err := c.NewSession(t.Context(), client.NewArgs{Name: "after-drain", Argv: []string{"/bin/sh"}, Cwd: "~", Wall: "none", Downgrade: true}); err != nil {
		t.Fatal("cleared drain still blocked a new session", err)
	}
}
