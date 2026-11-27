package tui

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/client/clienttest"
	"github.com/iamlakshya/simstim/internal/inputtransfer"
)

func heldRepositoryLocalGitV1(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "app")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		command := exec.CommandContext(t.Context(), "git", append([]string{"-C", dir}, args...)...)
		for _, entry := range os.Environ() {
			if !strings.HasPrefix(entry, "GIT_") {
				command.Env = append(command.Env, entry)
			}
		}
		command.Env = append(command.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("prepare original local repository: %v: %s", err, output)
		}
	}
	git("init", "--template=", "--initial-branch=main")
	git("config", "user.name", "Fixture")
	git("config", "user.email", "fixture@example.invalid")
	git("remote", "add", "origin", "https://forge.example/team/app.git")
	if err := os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("original local primary\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "tracked.txt")
	git("commit", "-m", "initial")
	return dir
}

// This crosses actual original local preparation, protected creation storage,
// TUI dispatch, native config publication and normal policy propagation. Its
// framed peer runs no guest: remote runtime/SSH acceptance is separate evidence.
func TestLocalRepositoryCreationSelectionFeedsActualTUIRepositoryPropagation(t *testing.T) {
	for _, organisation := range []bool{false, true} {
		name := map[bool]string{false: "no-section", true: "organisation-only"}[organisation]
		t.Run(name, func(t *testing.T) {
			for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_RUNTIME_DIR", "XDG_CONFIG_DIRS", "XDG_DATA_DIRS"} {
				t.Setenv(key, t.TempDir())
			}
			t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
			t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
			hs := sessions(t, 110, 38)
			h, _ := hs.m.selectedPair()
			if h == nil {
				t.Fatal("the launcher has no original host selection")
			}
			features := []string{"session.id", "sandbox", "wall.process", "policy", client.PolicyLayerFeature, inputtransfer.Feature,
				inputtransfer.FinishFeature, inputtransfer.PrimaryPolicyFeature, api.FeatureNetworkHolds, api.FeatureNetworkPublicOnly,
				api.FeaturePolicyLiveV1, api.FeatureHTTPMethods}
			peer := newHeldRepositoryPeerV1(t, features)
			machine := clienttest.NewMachine(client.MachineRunning, peer)
			provider := "held-repository-" + name
			client.RegisterProvider(provider, func(client.Host) (client.Lifecycle, error) { return machine, nil })
			t.Cleanup(func() { client.RegisterProvider(provider, nil) })
			host := client.Host{Name: h.name(), Machine: &client.Machine{Provider: provider, ID: "fixture"}, WallFloor: "process"}
			if err := client.SaveHosts(t.Context(), []client.Host{host}); err != nil {
				t.Fatal(err)
			}
			if err := client.SetTierContext(t.Context(), host.Name, client.TierOwn, peer.hostID, ""); err != nil {
				t.Fatal(err)
			}
			settings := "[sandbox]\nprofile='review'\n[profiles.review.network]\ndeny=['blocked-profile.example']\n"
			if organisation {
				settings += "[repos.\"forge.example/team/*\".network]\ndeny=['blocked-org.example']\n"
			}
			if err := os.WriteFile(filepath.Join(client.ConfigDir(), "config.toml"), []byte(settings), 0600); err != nil {
				t.Fatal(err)
			}
			request := client.NewGrantRequest(false, false)
			ctx := client.WithGrantRequest(client.WithoutPolicyReplay(t.Context()), request)
			t.Cleanup(func() { _ = request.Close() })
			connection, err := client.Dial(ctx, host, false)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(connection.Close)
			primary := heldRepositoryLocalGitV1(t)
			args, err := connection.PrepareNewInputPolicy(ctx, client.NewArgs{Agent: "shell"}, client.NewInputOptions{OriginalCWD: primary, Seeds: []string{primary}})
			if err == nil {
				args, err = connection.PrepareNewPolicy(ctx, args)
			}
			if err == nil {
				args, err = connection.ResolveNewWall(ctx, args)
			}
			if err == nil {
				err = request.BindNewWall(ctx, args.Wall, nil)
			}
			if err == nil {
				args, err = connection.PrepareNewInputs(ctx, args)
			}
			if err == nil {
				args, err = connection.PrepareNewSecrets(ctx, args)
			}
			if err != nil || !api.ValidSessionID(args.ID) {
				t.Fatal("actual original creation preparation failed", err)
			}
			const origin = "forge.example/team/app"
			exact := slices.IndexFunc(args.Layers, func(layer api.Layer) bool { return layer.Kind == api.LayerRepo && layer.Name == origin })
			profile := slices.IndexFunc(args.Layers, func(layer api.Layer) bool { return layer.Kind == api.LayerProfile })
			org := slices.IndexFunc(args.Layers, func(layer api.Layer) bool { return layer.Kind == api.LayerRepo && layer.Name == "forge.example/team/*" })
			if exact < 0 || profile <= exact || organisation && (org < 0 || org >= exact) ||
				len(args.Layers[exact].Rules) != 0 || args.Layers[exact].Lock != nil || !reflect.DeepEqual(args.Layers[exact].LayerValues, api.LayerValues{}) {
				t.Fatal("original exact reference was absent, out of order or granted something")
			}
			rows, err := client.SandboxPolicies(ctx, host)
			if err != nil || len(rows) != 1 {
				t.Fatal("actual original creation record was not retained", err)
			}
			pin, err := api.DecodePolicySourcePin(rows[0].PolicySources)
			if err != nil || pin == nil || !slices.ContainsFunc(pin.Layers, func(layer api.LayerRef) bool { return layer.Kind == api.LayerRepo && layer.Name == origin }) {
				t.Fatal("original creation pin lost its exact local repository reference", err)
			}
			if err := peer.setCreation(args); err != nil {
				t.Fatal(err)
			}
			if err := request.Close(); err != nil {
				t.Fatal(err)
			}
			previous := h.conn
			h.gen++
			h.host, h.conn, h.status, h.sessions = host, cleanConn{connection}, stOnline, []client.Session{peer.session}
			h.loaded, h.changes = true, h.changes+1
			previous.Close()
			hs.m.cur, hs.m.sel, hs.m.selIdx, hs.m.autoSel = host.Name, sessKey{host.Name, args.ID}, 0, false
			hs.key("p")
			d, ok := hs.m.modal.(*policySheet)
			if !ok || d.err != nil || d.holdCount() != 1 {
				t.Fatal("actual policy sheet did not read the original held request")
			}
			hs.key("r")
			requests, sent, applied := peer.observed()
			if d.err != nil || !applied || d.holdCount() != 0 {
				t.Fatal("actual repository answer did not reach native publication and normal apply", d.err)
			}
			var resolves, replaces int
			for _, request := range requests {
				switch request.Op {
				case api.OpPolicyResolve:
					resolves++
				case "policy.replace":
					replaces++
				case "policy.add", api.OpPolicyAnswer:
					t.Fatal("repository answer bypassed its native file/normal apply path", request.Op)
				}
			}
			var granted bool
			for _, layer := range sent {
				if layer.Kind == api.LayerRepo && layer.Name == origin {
					for _, rule := range layer.Rules {
						granted = granted || rule.Allow && rule.Target == "api.example.com:443"
					}
				}
			}
			if resolves != 1 || replaces != 1 || !granted {
				t.Fatal("the original empty exact section did not receive and propagate its rule", resolves, replaces)
			}
		})
	}
}
