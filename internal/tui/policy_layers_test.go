package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

type layerPlanningConn struct {
	Conn
	trace *[]string
	gate  error
}

func (c *layerPlanningConn) PrepareNewPolicy(_ context.Context, a client.NewArgs) (client.NewArgs, error) {
	*c.trace = append(*c.trace, "prepare")
	a.Layers = []api.Layer{{Kind: api.LayerAll, LayerValues: api.LayerValues{Sandbox: api.SandboxValues{Secrets: []string{"INHERITED"}}}}, {Kind: api.LayerHost, Name: "atlas-dev"}, {Kind: api.LayerSession, LayerValues: api.LayerValues{Sandbox: api.SandboxValues{Secrets: []string{"!INHERITED", "EXPLICIT"}}}}}
	return a, nil
}
func (c *layerPlanningConn) ResolveNewWall(_ context.Context, a client.NewArgs) (client.NewArgs, error) {
	*c.trace = append(*c.trace, "wall")
	return a, nil
}
func (c *layerPlanningConn) CheckNewPolicyGrants(context.Context, []api.Layer) error {
	*c.trace = append(*c.trace, "policy gate")
	return c.gate
}

type layerPlanningBackend struct {
	*fakeBackend
	trace       *[]string
	selected    []string
	hostSecrets []string
	fetches     int
}

func (b *layerPlanningBackend) Secrets(_ context.Context, _ client.Settings, h client.Host, names []string) (map[string]api.Secret, error) {
	*b.trace = append(*b.trace, "fetch")
	b.fetches++
	b.selected = append([]string(nil), names...)
	b.hostSecrets = append([]string(nil), h.Secrets...)
	return map[string]api.Secret{"EXPLICIT": "test-value"}, nil
}

func TestLayerPlanningAndTierGatePrecedeLauncherSecretFetch(t *testing.T) {
	for _, refuse := range []bool{false, true} {
		t.Run(map[bool]string{true: "refused", false: "allowed"}[refuse], func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			for _, name := range []string{"XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_RUNTIME_DIR", "XDG_CONFIG_DIRS", "XDG_DATA_DIRS"} {
				t.Setenv(name, filepath.Join(home, name))
			}
			if err := os.MkdirAll(client.ConfigDir(), 0o700); err != nil {
				t.Fatal(err)
			}
			var trace []string
			be := &layerPlanningBackend{fakeBackend: labBackend(), trace: &trace}
			m := New(t.Context(), be, Options{})
			t.Cleanup(m.Close)
			h := m.hostByName("atlas-dev")
			h.host.Secrets = []string{"INHERITED"}
			base, err := be.Dial(t.Context(), h.host)
			if err != nil {
				t.Fatal(err)
			}
			planner := &layerPlanningConn{Conn: base, trace: &trace}
			blocked := errors.New("private network refused by H5")
			if refuse {
				planner.gate = blocked
			}
			m.conns.add(planner)
			h.conn, h.status = cleanConn{planner}, stOnline
			tier := m.startSession(h, startReq{args: client.NewArgs{Agent: "shell"}})().(hostTierResolvedMsg)
			result := m.onHostTierResolved(tier)().(startedMsg)
			if refuse {
				if !errors.Is(result.err, blocked) || be.fetches != 0 {
					t.Fatalf("refusal fetched values: %v, %d", result.err, be.fetches)
				}
				return
			}
			if result.err != nil {
				t.Fatal(result.err)
			}
			if !reflect.DeepEqual(trace, []string{"prepare", "wall", "policy gate", "fetch"}) {
				t.Fatalf("order=%v", trace)
			}
			if !reflect.DeepEqual(be.selected, []string{"EXPLICIT"}) {
				t.Fatalf("list removal was ignored: %v", be.selected)
			}
			if !reflect.DeepEqual(be.hostSecrets, []string{"INHERITED"}) {
				t.Fatal("the grant backend received an altered saved Host")
			}
		})
	}
}
