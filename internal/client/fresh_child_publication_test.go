package client

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestFreshChildActualWriterStoresOnlyMatchingCompleteACK(t *testing.T) {
	for _, outcome := range []string{"complete", "missing state", "wrong run", "wrong child"} {
		t.Run(outcome, func(t *testing.T) {
			// A genuinely empty current LOCAL selection needs no command.
			// Generated replies exercise the actual pending writer/receipt path;
			// they are not proof of native guest or network execution.
			conn, request, ctx, cohort, marker, _ := freshChildFactoryFixture(t, []string{})
			var spawned string
			spawns := 0
			sharedGrantWire(t, conn, func(req proto.Request) any {
				if req.Op == "sandbox.snapshot" {
					return cohort
				}
				if req.Op != "spawn" {
					t.Fatal("unexpected member operation", req.Op)
				}
				spawns++
				var args api.JoinArgs
				if json.Unmarshal(req.Args, &args) != nil || args.Parent != cohort.Snapshot.Primary || args.Snapshot == nil ||
					args.SecretGeneration != strings.Repeat("d", 32) || args.SecretDelivery == nil || len(args.SecretDelivery.Modes) != 0 || len(args.Secrets) != 0 {
					t.Fatal("actual frame lost its sealed parent/cohort/OWN empty delivery")
				}
				// Out-of-band test oracle: the real publisher still owns its
				// native OP, so do not reacquire it from a raw test context.
				raw, err := os.ReadFile(sandboxStatePath(conn.Host))
				if err != nil {
					t.Fatal(err)
				}
				file, err := decodeSandboxFile(raw, conn.Host)
				if err != nil {
					t.Fatal(err)
				}
				row, err := decodeSandboxContext(t.Context(), args.ID, file.sessions[args.ID], file.schema)
				if err != nil || row.lineage == nil || row.lineage.Version != 2 || row.deliveryReceipt != nil {
					t.Fatal("physical spawn preceded its actual pending local reservation", err)
				}
				spawned = args.ID
				// The generated protected post-publication view must include
				// the actual child; it cannot manufacture a missing ACK.
				cohort.Snapshot = sharedGrantSnapshot(cohort.Snapshot.Primary, args.ID)
				child := Session{ID: args.ID, Agent: "codex", SandboxOf: cohort.Snapshot.Primary, RunGeneration: cohort.RunGeneration,
					Secrets: []string{}, MissingSecrets: []string{}, SecretState: &api.SessionSecretState{Version: 1, Generation: cohort.SecretState.Generation,
						Intent: api.SecretDeliveryIntent{Version: 1, Modes: map[string]api.SecretMode{}, Binds: map[string]api.SecretBind{}}, Primary: map[string]api.PublicSecretState{}}}
				switch outcome {
				case "missing state":
					child.SecretState = nil
				case "wrong run":
					child.RunGeneration = strings.Repeat("e", 32)
				case "wrong child":
					child.ID = "wrong234"
				}
				return child
			})
			child, err := conn.StartFreshChild(ctx, api.JoinArgs{Parent: cohort.Snapshot.Primary, Agent: "codex"}, false)
			if (err == nil) != (outcome == "complete") || spawns != 1 || spawned == "" {
				t.Fatalf("publication outcome=%s child=%v err=%v spawns=%d", outcome, child, err, spawns)
			}
			row, readErr := LoadSandboxPolicy(t.Context(), conn.Host, spawned)
			if readErr != nil || row == nil {
				t.Fatal("uncertain publication lost its local reservation", readErr)
			}
			if outcome == "complete" {
				if row.deliveryReceipt == nil || row.deliveryReceipt.Operation != "spawn" || row.deliveryReceipt.Scope.RunGeneration != cohort.RunGeneration ||
					row.deliveryReceipt.Scope.RootID != cohort.Snapshot.Primary || len(row.deliveryReceipt.Allowed) != 0 {
					t.Fatal("matching full ACK did not produce its OWN actual spawn receipt")
				}
			} else if row.deliveryReceipt != nil {
				t.Fatal("an incomplete/foreign ACK was replaced by public cohort metadata")
			}
			if retired, err := request.Abort(t.Context()); err != nil || retired {
				t.Fatal("an actual writer attempt became never-attempted cleanup", retired, err)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatal("known-empty selection ran a value command", err)
			}
		})
	}
}
