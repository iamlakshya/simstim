package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func sharedGrantChild(t *testing.T, h Host, parent SandboxPolicy, id string, names []string, ceiling Tier) SandboxPolicy {
	t.Helper()
	p := cloneSandboxPolicy(&parent)
	p.SessionID, p.Ceiling, p.SSHAgent, p.request = id, ceiling, false, "child-"+id
	p.Secrets = slices.Clone(names)
	p.lineage = &sandboxLineage{Version: 1, Parent: parent.SessionID, Root: localSandboxRoot(&parent), Allowed: localSandboxNames(&parent)}
	if p.lineage.Allowed == nil {
		p.lineage.Allowed = []string{}
	}
	if err := reserveSandbox(t.Context(), h, *p); err != nil {
		t.Fatal(err)
	}
	return *p
}

func TestCompleteResumePrechecksLaterChildBeforeFirstValue(t *testing.T) {
	h, parent, c, request := sharedGrantFixture(t)
	parent.Secrets, parent.Exposed = []string{"API_KEY", "OTHER_KEY"}, []string{"API_KEY", "OTHER_KEY"}
	// This is an independently created fixture, not a retry that enlarges
	// the original wall. Preserve its exact raw creation bytes.
	parent.SessionID, parent.request, parent.wallIntent = "bcd234ef", "two-name-parent", nil
	if err := reserveSandbox(t.Context(), h, parent); err != nil {
		t.Fatal(err)
	}
	p, err := LoadSandboxPolicy(t.Context(), h, parent.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	parent = *p
	child := sharedGrantChild(t, h, parent, "def234gh", []string{"OTHER_KEY"}, TierThrowaway)
	marker := filepath.Join(t.TempDir(), "first-value-must-not-run")
	config := "[secrets]\nAPI_KEY = " + quotedCommand("touch "+ShellWord(marker)+"; printf forbidden") + "\nOTHER_KEY = \"printf later\"\n"
	if err := os.WriteFile(settingsPath(), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	sharedGrantWire(t, c, func(proto.Request) any { return sharedGrantSnapshot(parent.SessionID, child.SessionID) })
	ctx := WithGrantRequest(t.Context(), request)
	if err := c.CheckResumeGrants(ctx, parent.SessionID, []string{"API_KEY"}); err == nil {
		t.Fatal("a later child's lower ceiling did not stop the complete precheck")
	}
	if _, err := FetchSessionSecrets(ctx, h, parent.SessionID, []string{"API_KEY"}, false); err == nil {
		t.Fatal("the generic primary fetch bypassed a prepared member refusal")
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a primary command ran before every child's grant passed")
	}
}

func quotedCommand(command string) string {
	raw, _ := json.Marshal(command)
	return string(raw)
}

func TestChildResumeUsesLocalRootAndOneCompleteBundle(t *testing.T) {
	h, parent, c, request := sharedGrantFixture(t)
	child := sharedGrantChild(t, h, parent, "def234gh", []string{"API_KEY"}, TierOwn)
	const unknown = "uvw234xy"
	var resumes int
	sharedGrantWire(t, c, func(req proto.Request) any {
		if req.Op == "sandbox.snapshot" {
			var a api.SandboxSnapshotArgs
			_ = json.Unmarshal(req.Args, &a)
			if a.ID != child.SessionID {
				t.Fatal("the structural read lost the original typed child")
			}
			return sharedGrantSnapshot(parent.SessionID, child.SessionID, unknown)
		}
		resumes++
		var a ResumeArgs
		if req.Op != "resume" || json.Unmarshal(req.Args, &a) != nil || a.ID != child.SessionID ||
			a.Snapshot == nil || a.Snapshot.Primary != parent.SessionID || len(a.Members) != 2 ||
			a.Members[0].ID != child.SessionID || len(a.Members[0].Secrets) != 1 ||
			a.Members[1].ID != unknown || len(a.Members[1].Secrets) != 0 || len(a.Secrets) != 1 {
			t.Fatalf("incomplete or retargeted resume: %+v", a)
		}
		return ResumeResult{Session: Session{ID: child.SessionID}}
	})
	ctx := WithGrantRequest(t.Context(), request)
	id, names, err := c.ResumeSecretTarget(ctx, child.SessionID, []string{"API_KEY"})
	if err != nil || id != parent.SessionID || !slices.Equal(names, []string{"API_KEY"}) {
		t.Fatalf("local child selector=%q %v %v", id, names, err)
	}
	values, err := FetchSessionSecrets(ctx, h, id, names, false)
	if err != nil {
		t.Fatal(err)
	}
	members, err := c.ResumeMemberSecrets(ctx, child.SessionID, false)
	if err != nil {
		t.Fatal(err)
	}
	result, err := c.Resume(ctx, ResumeArgs{ID: child.SessionID, Secrets: values, Members: members})
	if err != nil || result.Session.ID != child.SessionID || resumes != 1 {
		t.Fatalf("complete child resume: %+v, %v", result, err)
	}
	rows, err := SandboxPolicies(t.Context(), h)
	if err != nil || len(rows) != 2 {
		t.Fatalf("an unknown member was adopted locally: %d, %v", len(rows), err)
	}
}

func TestResumeRejectsMissingDuplicateForeignAndChangedSnapshotBeforeFrame(t *testing.T) {
	h, parent, c, request := sharedGrantFixture(t)
	child := sharedGrantChild(t, h, parent, "def234gh", nil, TierOwn)
	sharedGrantWire(t, c, func(proto.Request) any { return sharedGrantSnapshot(parent.SessionID, child.SessionID) })
	ctx := WithGrantRequest(t.Context(), request)
	if _, _, err := c.ResumeSecretTarget(ctx, parent.SessionID, nil); err != nil {
		t.Fatal(err)
	}
	valid := ResumeArgs{ID: parent.SessionID, Snapshot: sharedGrantSnapshot(parent.SessionID, child.SessionID), Members: []api.MemberResumeArgs{{ID: child.SessionID}}}
	cases := []struct {
		name string
		edit func(*ResumeArgs)
	}{
		{"missing", func(a *ResumeArgs) { a.Members = nil }},
		{"duplicate", func(a *ResumeArgs) { a.Members = append(a.Members, a.Members[0]) }},
		{"primary among children", func(a *ResumeArgs) { a.Members[0].ID = parent.SessionID }},
		{"foreign", func(a *ResumeArgs) { a.Members[0].ID = "uvw234xy" }},
		{"generation", func(a *ResumeArgs) { a.Snapshot.Generation = "b" + a.Snapshot.Generation[1:] }},
		{"null child set", func(a *ResumeArgs) { a.Snapshot.Members = nil }},
		{"unapproved value", func(a *ResumeArgs) { a.Members[0].Secrets = map[string]api.Secret{"OTHER_KEY": "forbidden"} }},
	}
	c.w = proto.NewWriter(grantWriteFunc(func([]byte) (int, error) { t.Fatal("invalid complete resume published a frame"); return 0, nil }))
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := cloneResumeValues(valid)
			tc.edit(&a)
			if _, err := c.Resume(ctx, a); err == nil {
				t.Fatal("an incomplete or changed bundle was accepted")
			}
		})
	}
}

func TestPreparedResumeLocalMutationStopsReadAndFinalPublication(t *testing.T) {
	h, parent, c, request := sharedGrantFixture(t)
	child := sharedGrantChild(t, h, parent, "def234gh", []string{"API_KEY"}, TierOwn)
	sharedGrantWire(t, c, func(proto.Request) any { return sharedGrantSnapshot(parent.SessionID, child.SessionID) })
	ctx := WithGrantRequest(t.Context(), request)
	if _, _, err := c.ResumeSecretTarget(ctx, parent.SessionID, []string{"API_KEY"}); err != nil {
		t.Fatal(err)
	}
	if err := SetTierContext(t.Context(), h.Name, TierThrowaway, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := FetchSessionSecrets(ctx, h, parent.SessionID, []string{"API_KEY"}, false); err == nil {
		t.Fatal("a tier change after precheck allowed a value read")
	}
	c.w = proto.NewWriter(grantWriteFunc(func([]byte) (int, error) { t.Fatal("lowered-tier resume published"); return 0, nil }))
	if _, err := c.Resume(ctx, ResumeArgs{ID: parent.SessionID, Secrets: map[string]api.Secret{"API_KEY": "must-not-send"}, Members: []api.MemberResumeArgs{{ID: child.SessionID, Secrets: map[string]api.Secret{"API_KEY": "must-not-send"}}}}); err == nil {
		t.Fatal("a final group gate ignored the current tier")
	}
}

func TestUnknownSharedResumeIsValueFreeAndNeverAdoptsKnownRoot(t *testing.T) {
	_, parent, c, request := sharedGrantFixture(t)
	const unknown = "uvw234xy"
	sharedGrantWire(t, c, func(req proto.Request) any {
		if req.Op == "sandbox.snapshot" {
			return sharedGrantSnapshot(parent.SessionID, unknown)
		}
		return ResumeResult{Session: Session{ID: unknown}}
	})
	ctx := WithGrantRequest(t.Context(), request)
	id, names, err := c.ResumeSecretTarget(ctx, unknown, nil)
	if err != nil || id != parent.SessionID || len(names) != 0 {
		t.Fatalf("unknown no-value mapping: %q, %v, %v", id, names, err)
	}
	if _, err := FetchSessionSecrets(ctx, c.Host, id, []string{"API_KEY"}, false); err == nil {
		t.Fatal("an unknown selected child borrowed the known root's authority")
	}
	members, err := c.ResumeMemberSecrets(ctx, unknown, false)
	if err != nil || len(members) != 1 || len(members[0].Secrets) != 0 {
		t.Fatalf("unknown value-free children=%+v err=%v", members, err)
	}
	if _, err := c.Resume(ctx, ResumeArgs{ID: unknown, Members: members}); err != nil {
		t.Fatal(err)
	}
}

func TestOrdinaryUnwalledResumeKeepsLegacyPath(t *testing.T) {
	h := h5GrantState(t)
	c := &Conn{Host: h, hostID: h5Policy().HostID, Features: []string{"resume"}, pending: map[uint64]chan proto.Response{}, done: make(chan struct{})}
	sharedGrantWire(t, c, func(req proto.Request) any {
		if req.Op != "resume" || bytes.Contains(req.Args, []byte("snapshot")) || bytes.Contains(req.Args, []byte("members")) {
			t.Fatal("ordinary resume changed its legacy protocol")
		}
		return ResumeResult{Session: Session{ID: "abc23def"}}
	})
	if _, err := c.Resume(context.Background(), ResumeArgs{ID: "abc23def"}); err != nil {
		t.Fatal(err)
	}
}

func TestDirectCompleteResumePinsEmptyValuesWithoutReadsOrLaterGrowth(t *testing.T) {
	h, parent, c, request := sharedGrantFixture(t)
	child := sharedGrantChild(t, h, parent, "def234gh", []string{"API_KEY"}, TierThrowaway)
	marker := filepath.Join(t.TempDir(), "no-value-wake-must-not-read")
	config := "[secrets]\nAPI_KEY = " + quotedCommand("touch "+ShellWord(marker)+"; printf forbidden") + "\n"
	if err := os.WriteFile(settingsPath(), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	var resumes int
	sharedGrantWire(t, c, func(req proto.Request) any {
		if req.Op == "sandbox.snapshot" {
			return sharedGrantSnapshot(parent.SessionID, child.SessionID)
		}
		resumes++
		var a ResumeArgs
		if json.Unmarshal(req.Args, &a) != nil || req.Op != "resume" || len(a.Secrets) != 0 ||
			len(a.Members) != 1 || len(a.Members[0].Secrets) != 0 {
			t.Fatal("value-free wake grew a value bundle")
		}
		return ResumeResult{Session: Session{ID: parent.SessionID, MissingSecrets: []string{"API_KEY"}}}
	})
	ctx := WithGrantRequest(t.Context(), request)
	a := ResumeArgs{ID: parent.SessionID, Members: []api.MemberResumeArgs{{ID: child.SessionID}}}
	result, err := c.Resume(ctx, a)
	if err != nil || result == nil || !slices.Equal(result.Session.MissingSecrets, []string{"API_KEY"}) || resumes != 1 {
		t.Fatalf("honest value-free wake=%+v err=%v frames=%d", result, err, resumes)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a value-free wake ran a secret command")
	}
	a.Secrets = map[string]api.Secret{"API_KEY": "forbidden-growth"}
	a.Members[0].Secrets = map[string]api.Secret{"API_KEY": "forbidden-growth"}
	if _, err := c.Resume(ctx, a); err == nil || resumes != 1 {
		t.Fatal("an empty plan grew values on the same logical request")
	}
	if _, err := FetchSessionSecrets(ctx, h, parent.SessionID, []string{"API_KEY"}, false); err == nil {
		t.Fatal("an empty plan inferred permission to fetch a saved name")
	}
	if rows, err := GrantsForHostContext(t.Context(), h, parent.HostID); err != nil || len(rows) != 0 {
		t.Fatalf("absent values were recorded as sent: %+v %v", rows, err)
	}
}

func TestDerivedFetchNeedsPreparedCompleteCapabilityGate(t *testing.T) {
	h, parent, _, _ := sharedGrantFixture(t)
	child := sharedGrantChild(t, h, parent, "def234gh", []string{"API_KEY"}, TierOwn)
	if _, err := FetchSessionSecrets(t.Context(), h, child.SessionID, []string{"API_KEY"}, false); err == nil {
		t.Fatal("a derived member read values without its feature/snapshot gate")
	}
}

func TestDirectCompleteResumeKeepsActuallySuppliedScopedValues(t *testing.T) {
	h, parent, c, request := sharedGrantFixture(t)
	child := sharedGrantChild(t, h, parent, "def234gh", []string{"API_KEY"}, TierThrowaway)
	if err := os.WriteFile(settingsPath(), []byte("[secrets]\nAPI_KEY = \"printf must-not-fetch\"\n[secret_tiers]\nAPI_KEY = \"throwaway\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var resumes int
	sharedGrantWire(t, c, func(req proto.Request) any {
		if req.Op == "sandbox.snapshot" {
			return sharedGrantSnapshot(parent.SessionID, child.SessionID)
		}
		resumes++
		var a ResumeArgs
		if req.Op != "resume" || json.Unmarshal(req.Args, &a) != nil || len(a.Members) != 1 ||
			string(a.Members[0].Secrets["API_KEY"]) != "scoped-child-value" {
			t.Fatal("direct scoped values were changed or a different operation was used")
		}
		return ResumeResult{Session: Session{ID: parent.SessionID}}
	})
	_, err := c.Resume(WithGrantRequest(t.Context(), request), ResumeArgs{ID: parent.SessionID,
		Members: []api.MemberResumeArgs{{ID: child.SessionID, Secrets: map[string]api.Secret{"API_KEY": "scoped-child-value"}}}})
	if err != nil || resumes != 1 {
		t.Fatalf("direct scoped bundle frames=%d err=%v", resumes, err)
	}
	if rows, err := GrantsForHostContext(t.Context(), h, parent.HostID); err != nil || len(rows) != 1 || rows[0].Name != "API_KEY" || rows[0].Count != 1 {
		t.Fatalf("a child-only exposure was absent from history: %+v %v", rows, err)
	}
}

func TestChildResumeCannotReturnThePrimaryForAttach(t *testing.T) {
	h, parent, c, request := sharedGrantFixture(t)
	child := sharedGrantChild(t, h, parent, "def234gh", nil, TierOwn)
	sharedGrantWire(t, c, func(req proto.Request) any {
		if req.Op == "sandbox.snapshot" {
			return sharedGrantSnapshot(parent.SessionID, child.SessionID)
		}
		return ResumeResult{Session: Session{ID: parent.SessionID}}
	})
	_, err := c.Resume(WithGrantRequest(t.Context(), request), ResumeArgs{ID: child.SessionID,
		Members: []api.MemberResumeArgs{{ID: child.SessionID}}})
	var refused *UserError
	if !errors.As(err, &refused) || refused.Code != "protocol" {
		t.Fatalf("a host reply retargeted the child attach: %v", err)
	}
}
