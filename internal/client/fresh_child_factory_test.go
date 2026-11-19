package client

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

// The fixture supplies generated protected replies over the actual common
// request writer. It proves local factory/storage/CAS behaviour, not native
// SSH/guest execution. No command, HTTP request or external transport runs.
func freshChildFactoryFixture(t *testing.T, requested ...[]string) (*Conn, *GrantRequest, context.Context, *api.SandboxCohortView, string, []byte) {
	t.Helper()
	h := h5GrantState(t)
	h.Secrets = []string{"NEW_KEY"}
	if len(requested) != 0 {
		h.Secrets = append([]string{}, requested[0]...)
	}
	if err := SaveHosts(t.Context(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	conn := finishGeneratedTransportFixture(t, h, strings.Repeat("a", 32), "session.id", "secrets", api.FeatureSpawn, api.FeatureSandboxSnapshot, api.FeatureSandboxCohort, api.FeatureBoundSecrets)
	request := NewFreshChildGrantRequest()
	ctx := WithGrantRequest(t.Context(), request)
	t.Cleanup(func() {
		// Testing cancels T.Context before cleanup. This is the fixture
		// owner's explicit bounded metadata-cleanup budget, not a new
		// production grace period or permission to contact a host.
		cleanup, stop := context.WithTimeout(context.WithoutCancel(t.Context()), 2*time.Second)
		defer stop()
		if _, err := request.Abort(cleanup); err != nil {
			t.Error(err)
		}
		if err := request.Close(); err != nil {
			t.Error(err)
		}
	})
	owned, release, err := conn.beginGrant(ctx, "fresh factory fixture", true)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := commitSandboxBoundSchema(owned, h); err != nil {
		t.Fatal(err)
	}
	root := SandboxPolicy{SessionID: "root2345", HostID: conn.HostID(), Ceiling: TierOwn, CreationWall: "gvisor", Secrets: []string{"OLD_KEY"}, Exposed: []string{"OLD_KEY"},
		CreatedAt: time.Unix(1, 0).UTC(), request: strings.Repeat("b", 32), PolicySources: json.RawMessage(`{ "original_resources" : {"duplicate":1,"duplicate":2} }`),
		DeliveryIntent: &api.SecretDeliveryIntent{Version: 1, Modes: map[string]api.SecretMode{"OLD_KEY": api.SecretModeExposed}, Binds: map[string]api.SecretBind{}}}
	if err := reserveSandbox(owned, h, root); err != nil {
		t.Fatal(err)
	}
	release()
	var before []byte
	if err := withHostStateReadContext(ctx, h, func(owned context.Context) error {
		return withSandboxFile(owned, h, false, func(file *sandboxFile) error { before = bytes.Clone(file.sessions[root.SessionID]); return nil })
	}); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "command-was-run")
	command := "printf reached > " + ShellQuote(marker)
	settings := "[secrets.NEW_KEY]\nrun = " + strconv.Quote(command) + "\nhosts = ['api.example.test']\nheader = 'Authorization'\nformat = 'Bearer {value}'\n"
	if err := os.MkdirAll(filepath.Dir(settingsPath()), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath(), []byte(settings), 0600); err != nil {
		t.Fatal(err)
	}
	cohort := &api.SandboxCohortView{Snapshot: sharedGrantSnapshot(root.SessionID), RunGeneration: strings.Repeat("c", 32),
		SecretState: &api.SessionSecretState{Version: 1, Generation: strings.Repeat("d", 32), Intent: api.CloneSecretDeliveryIntent(*root.DeliveryIntent), Primary: map[string]api.PublicSecretState{"OLD_KEY": {Mode: api.SecretModeExposed}}}}
	conn.pending = map[uint64]chan proto.Response{}
	sharedGrantWire(t, conn, func(req proto.Request) any {
		if req.Op != "sandbox.snapshot" {
			t.Fatal("pre-value constructor sent a non-snapshot operation", req.Op)
		}
		var args api.SandboxSnapshotArgs
		if json.Unmarshal(req.Args, &args) != nil || !args.Cohort || args.ID != root.SessionID {
			t.Fatal("constructor lost the original coherent cohort selector")
		}
		// The request writer must not retain a host OP across this RPC.
		_, unlock, err := LockHost(t.Context(), h.Name, "competing cohort read")
		if err != nil {
			t.Fatal("cohort RPC retained the host publication lock", err)
		}
		unlock()
		return cohort
	})
	return conn, request, ctx, cohort, marker, before
}

func TestFreshChildFactoryReservesOwnLocalDomainBeforeCommands(t *testing.T) {
	conn, request, ctx, cohort, marker, rootBefore := freshChildFactoryFixture(t)
	choice := api.JoinArgs{Parent: cohort.Snapshot.Primary, Agent: "codex", Cols: 80, Rows: 24}
	args, err := conn.PrepareFreshChild(ctx, choice)
	if err != nil {
		t.Fatal(err)
	}
	if args.ID == "" || args.SecretGeneration != cohort.SecretState.Generation || len(args.Secrets) != 0 || !sameGrantSnapshot(args.Snapshot, cohort.Snapshot) {
		t.Fatal("pre-value args lack their actual reservation/cohort or contain a value")
	}
	if !reflect.DeepEqual(args.SecretDelivery, &api.SecretDeliveryIntent{Version: 1, Modes: map[string]api.SecretMode{"NEW_KEY": api.SecretModeBound}, Binds: map[string]api.SecretBind{"NEW_KEY": {Hosts: []string{"api.example.test:443"}, Header: "Authorization", Format: "Bearer {value}"}}}) {
		t.Fatal("fresh selection borrowed the parent's names or exposure")
	}
	if err := withHostStateReadContext(ctx, conn.Host, func(owned context.Context) error {
		return withSandboxFile(owned, conn.Host, false, func(file *sandboxFile) error {
			if !bytes.Equal(file.sessions[cohort.Snapshot.Primary], rootBefore) {
				t.Fatal("factory changed the original root bytes")
			}
			row, err := decodeSandboxContext(owned, args.ID, file.sessions[args.ID], file.schema)
			if err != nil {
				return err
			}
			if file.schema != 4 || row.lineage == nil || row.lineage.Version != 2 || row.lineage.SelectedKind != "codex" ||
				!reflect.DeepEqual(localSandboxNames(&row), []string{"NEW_KEY"}) || len(row.Exposed) != 0 || row.deliveryReceipt != nil {
				t.Fatal("factory failed to retain the actual OWN v2 reservation without an ACK")
			}
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("a value command ran during construction", err)
	}
	if retired, err := request.Abort(t.Context()); err != nil || !retired {
		t.Fatal("the actual never-attempted reservation could not be retired", retired, err)
	}
}

func TestFreshChildFactoryRetainsSettingsAndOriginalCohortAcrossRetry(t *testing.T) {
	for _, changed := range []string{"settings", "cohort", "agent"} {
		t.Run(changed, func(t *testing.T) {
			conn, _, ctx, cohort, marker, _ := freshChildFactoryFixture(t)
			choice := api.JoinArgs{Parent: cohort.Snapshot.Primary, Agent: "codex"}
			args, err := conn.PrepareFreshChild(ctx, choice)
			if err != nil {
				t.Fatal(err)
			}
			switch changed {
			case "settings":
				if err := os.WriteFile(settingsPath(), []byte("[secrets]\nFOREIGN = 'printf never'\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "cohort":
				cohort.SecretState.Generation = strings.Repeat("e", 32)
			case "agent":
				args.Agent = "claude"
			}
			if _, err := conn.PrepareFreshChild(ctx, args); err == nil {
				t.Fatal("retry adopted changed local settings, cohort or agent")
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatal("refused retry ran a value command", err)
			}
		})
	}
}
