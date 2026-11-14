package client

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestEveryKnownSnapshotChildMustHaveTheOriginalLocalRootAndHostID(t *testing.T) {
	for _, mode := range []string{"standalone", "other root", "other identity"} {
		t.Run(mode, func(t *testing.T) {
			h, primary, c, r := sharedGrantFixture(t)
			foreign := h5Policy()
			foreign.SessionID, foreign.request = "uvw234xy", "foreign-root"
			foreign.Ceiling, foreign.CreationWall = TierOwn, "process"
			foreign.Secrets, foreign.Exposed = []string{"OTHER_KEY"}, []string{"OTHER_KEY"}
			if err := reserveSandbox(t.Context(), h, foreign); err != nil {
				t.Fatal(err)
			}
			badID := foreign.SessionID
			if mode != "standalone" {
				p, err := LoadSandboxPolicy(t.Context(), h, foreign.SessionID)
				if err != nil {
					t.Fatal(err)
				}
				child := sharedGrantChild(t, h, *p, "def234gh", []string{"OTHER_KEY"}, TierOwn)
				badID = child.SessionID
			}
			if mode == "other identity" {
				if err := withSandboxFile(t.Context(), h, true, func(f *sandboxFile) error {
					fields, err := sandboxObject(f.sessions[badID])
					if err != nil {
						return err
					}
					fields["host_id"] = json.RawMessage(`"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"`)
					fields["lineage"] = json.RawMessage(`{"version":1,"parent":"` + primary.SessionID + `","root":"` + primary.SessionID + `","allowed_names":["OTHER_KEY"]}`)
					f.sessions[badID], err = marshalSandboxObject(fields)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			sharedGrantWire(t, c, func(proto.Request) any { return sharedGrantSnapshot(primary.SessionID, badID) })
			ctx := WithGrantRequest(t.Context(), r)
			if err := c.CheckResumeGrants(ctx, primary.SessionID, []string{"API_KEY"}); err == nil {
				t.Fatal("a foreign member supplied grant authority")
			}
			if _, err := FetchSessionSecrets(ctx, h, primary.SessionID, []string{"API_KEY"}, false); err == nil {
				t.Fatal("a failed group precheck fell back into a legacy fetch")
			}
		})
	}
}

func TestNewGroupMissingAuthorityRefusesEveryScopedValue(t *testing.T) {
	for _, minimum := range []string{"throwaway", "shared"} {
		for _, unknown := range []string{"original", "child"} {
			t.Run(minimum+"/"+unknown, func(t *testing.T) {
				h, parent, c, request := sharedGrantFixture(t)
				config := fmt.Sprintf("[secrets]\nAPI_KEY = \"printf must-not-read\"\n[secret_tiers]\nAPI_KEY = %q\n", minimum)
				if err := os.WriteFile(settingsPath(), []byte(config), 0o600); err != nil {
					t.Fatal(err)
				}
				const missing = "uvw234xy"
				sharedGrantWire(t, c, func(req proto.Request) any {
					if req.Op != "sandbox.snapshot" {
						t.Fatal("missing local authority published values")
					}
					return sharedGrantSnapshot(parent.SessionID, missing)
				})
				ctx := WithGrantRequest(t.Context(), request)
				if unknown == "original" {
					if _, _, err := c.ResumeSecretTarget(ctx, missing, []string{"API_KEY"}); err == nil {
						t.Fatal("unknown selected member borrowed primary authority")
					}
				} else {
					if _, err := c.Resume(ctx, ResumeArgs{ID: parent.SessionID, Members: []api.MemberResumeArgs{{ID: missing, Secrets: map[string]api.Secret{"API_KEY": "must-not-send"}}}}); err == nil {
						t.Fatal("unknown child received a scoped value")
					}
				}
				if _, err := FetchSessionSecrets(ctx, h, parent.SessionID, []string{"API_KEY"}, false); err == nil {
					t.Fatal("failed group admission became a legacy scoped fetch")
				}
			})
		}
	}
}

func TestKnownWallClassifiesGroupBeforeEitherFeatureIsMissing(t *testing.T) {
	for _, features := range [][]string{{"resume", "secrets", "spawn-v1"}, {"resume", "secrets", "sandbox-snapshot-v1"}} {
		t.Run(features[2], func(t *testing.T) {
			h, primary, c, r := sharedGrantFixture(t)
			c.Features = features
			ctx := WithGrantRequest(t.Context(), r)
			if err := c.CheckResumeGrants(ctx, primary.SessionID, nil); err == nil {
				t.Fatal("one missing group feature selected legacy Resume")
			}
			if _, err := FetchSessionSecrets(ctx, h, primary.SessionID, nil, false); err == nil {
				t.Fatal("a failed group feature check became legacy authority")
			}
			if _, err := c.Resume(ctx, ResumeArgs{ID: primary.SessionID}); err == nil {
				t.Fatal("one missing feature published a legacy frame")
			}
		})
	}
}

func TestUnknownZeroValueJoinStillValidatesAllExistingState(t *testing.T) {
	h, _, c, _ := sharedGrantFixture(t)
	if err := os.WriteFile(sandboxStatePath(h), []byte(`{"schema":4,"host":"box","sessions":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.JoinSecretNames(t.Context(), "uvw234xy"); err == nil {
		t.Fatal("zero-value unknown Join bypassed an unknown state schema")
	}
}
