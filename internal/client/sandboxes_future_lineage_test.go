package client

import (
	"bytes"
	"encoding/json"
	"os"
	"strconv"
	"testing"

	"github.com/iamlakshya/simstim/internal/proto"
)

func TestFutureLineageHasOneNonAuthoritativeReaderGrammar(t *testing.T) {
	for _, version := range []json.RawMessage{[]byte("2"), []byte("9223372036854775807")} {
		row := projectionRow()
		row["ssh_agent"] = false
		row["lineage"] = map[string]any{"version": version, "parent": "def234gh", "root": "def234gh", "allowed_names": []string{"A_KEY"}, "future": map[string]any{"meaning": "unknown"}}
		raw := projectionState(t, 3, row, nil)
		compareAgentProjection(t, raw)
		f, err := decodeSandboxFile(raw, Host{Name: "box"})
		if err != nil {
			t.Fatal(err)
		}
		p, err := decodeSandbox(projectionSession, f.sessions[projectionSession], 3)
		if err != nil || !p.FutureLineage || p.lineage != nil || p.SSHAgent || len(localSandboxNames(&p)) != 0 {
			t.Fatalf("future lineage became ordinary authority: %+v %v", p, err)
		}
		for _, tier := range []Tier{TierOwn, TierThrowaway, TierShared} {
			settings := Settings{SecretTiers: map[string]string{"A_KEY": tier.String()}}
			if err := allowResumeSecretNames(t.Context(), Host{Name: "box"}, p.HostID, tier, settings, []string{"A_KEY"}, &p); err == nil {
				t.Fatalf("future lineage granted a %s value", tier)
			}
		}
	}
}

func TestLineageVersionInvalidFormsAgreeAndNeverBecomeOrdinary(t *testing.T) {
	versions := []string{"null", `"1"`, "0", "-1", "1.0", "2e0", "9223372036854775808", "true"}
	for _, version := range versions {
		t.Run(version, func(t *testing.T) {
			row := projectionRow()
			row["ssh_agent"] = false
			row["lineage"] = json.RawMessage(`{"version":` + version + `,"parent":"def234gh","root":"def234gh","allowed_names":[]}`)
			raw := projectionState(t, 3, row, nil)
			compareAgentProjection(t, raw)
			if _, err := decodeSandboxFile(raw, Host{Name: "box"}); err == nil {
				t.Fatal("an invalid version became unrestricted metadata")
			}
		})
	}
	for _, lineage := range []json.RawMessage{
		[]byte(`{"parent":"def234gh","root":"def234gh","allowed_names":[]}`),
		[]byte(`{"version":2,"Version":2}`),
		[]byte(`{"version":2,"version":2}`),
		[]byte(`{"version":2,"future":1,"future":2}`),
		[]byte(`null`), []byte(`[]`),
	} {
		row := projectionRow()
		row["ssh_agent"], row["lineage"] = false, lineage
		raw := projectionState(t, 3, row, nil)
		compareAgentProjection(t, raw)
		if _, err := decodeSandboxFile(raw, Host{Name: "box"}); err == nil {
			t.Fatalf("ambiguous lineage became authority: %s", lineage)
		}
	}
}

func futureSharedFixture(t *testing.T) (Host, SandboxPolicy, *Conn, json.RawMessage) {
	t.Helper()
	h, p, c, _ := sharedGrantFixture(t)
	const future = `{ "version":2, "parent":"def234gh", "root":"def234gh", "allowed_names":["API_KEY"], "future":{"x":1,"x":2} }`
	if err := withSandboxFile(t.Context(), h, true, func(f *sandboxFile) error {
		fields, err := sandboxObject(f.sessions[p.SessionID])
		if err != nil {
			return err
		}
		fields["lineage"] = json.RawMessage(future)
		f.schema, f.root["schema"] = 3, json.RawMessage("3")
		f.sessions[p.SessionID], err = marshalSandboxObject(fields)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return h, p, c, json.RawMessage(future)
}

func TestFutureLineageRefusesDerivationAndAgentButKeepsZeroValueWake(t *testing.T) {
	h, p, c, _ := futureSharedFixture(t)
	if _, err := c.JoinSecretNames(WithGrantRequest(t.Context(), NewGrantRequest(false, false)), p.SessionID); err == nil {
		t.Fatal("future lineage derived a child")
	}
	if err := c.checkAgentPolicy(t.Context(), p.SessionID, agentModeForTest(t).inspector); err == nil {
		t.Fatal("future lineage admitted an agent capability")
	}
	var resumes int
	sharedGrantWire(t, c, func(req proto.Request) any {
		if req.Op == "sandbox.snapshot" {
			return sharedGrantSnapshot(p.SessionID)
		}
		resumes++
		if bytes.Contains(req.Args, []byte("secrets")) {
			t.Fatal("future structural wake carried values")
		}
		return ResumeResult{Session: Session{ID: p.SessionID}}
	})
	ctx := WithGrantRequest(t.Context(), NewGrantRequest(false, false))
	if _, err := c.Resume(ctx, ResumeArgs{ID: p.SessionID}); err != nil || resumes != 1 {
		t.Fatalf("future zero-value structural wake=%v frames=%d", err, resumes)
	}
	if _, err := FetchSessionSecrets(ctx, h, p.SessionID, []string{"API_KEY"}, false); err == nil {
		t.Fatal("a zero-value future wake inferred a value grant")
	}
}

func TestFutureLineageBytesSurviveOrdinaryWritesAndSchemaNeverDowngrades(t *testing.T) {
	h, p, _, future := futureSharedFixture(t)
	ordinary := h5Policy()
	ordinary.SessionID, ordinary.request = "uvw234xy", "ordinary-after-future"
	if err := reserveSandbox(t.Context(), h, ordinary); err != nil {
		t.Fatal(err)
	}
	if err := withSandboxFile(t.Context(), h, false, func(f *sandboxFile) error {
		fields, err := sandboxObject(f.sessions[p.SessionID])
		if err != nil || f.schema != 3 || !bytes.Equal(fields["lineage"], future) {
			t.Fatalf("future bytes or schema changed: schema=%d lineage=%s err=%v", f.schema, fields["lineage"], err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, schema := range []int{2, 3} {
		f := &sandboxFile{schema: schema, root: map[string]json.RawMessage{"schema": []byte(strconv.Itoa(schema))}}
		if err := upgradeSandboxIntent(f); err != nil || f.schema != schema {
			t.Fatalf("upgrade changed schema%d: %v", schema, err)
		}
	}
	if f := (&sandboxFile{schema: 4}); upgradeSandboxIntent(f) == nil || f.schema != 4 {
		t.Fatal("an unknown schema was converted into an older writable file")
	}
	if raw, err := os.ReadFile(sandboxStatePath(h)); err != nil || !bytes.Contains(raw, []byte(`"schema":3`)) {
		t.Fatalf("ordinary write downgraded schema: %v", err)
	}
}

func TestKnownScopedLineageNamesNeedNoBlanketExposureSubset(t *testing.T) {
	isolateH5GrantHome(t)
	row := projectionRow()
	row["ssh_agent"], row["secrets"] = false, []string{"SCOPED_KEY"}
	row["wall_intent"] = map[string]any{"level": "process", "exposed": []string{"A_KEY"}}
	row["lineage"] = map[string]any{"version": 1, "parent": "def234gh", "root": "def234gh", "allowed_names": []string{"SCOPED_KEY"}}
	raw := projectionState(t, 3, row, nil)
	compareAgentProjection(t, raw)
	f, err := decodeSandboxFile(raw, Host{Name: "box"})
	if err != nil {
		t.Fatal(err)
	}
	p, err := decodeSandbox(projectionSession, f.sessions[projectionSession], 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := allowResumeSecretNames(t.Context(), Host{Name: "box"}, p.HostID, TierThrowaway,
		Settings{SecretTiers: map[string]string{"SCOPED_KEY": "throwaway"}}, []string{"SCOPED_KEY"}, &p); err != nil {
		t.Fatalf("a scoped value acquired an invented exposure restriction: %v", err)
	}
}

func TestOnlyDerivedPublicationAdvancesTwoToThreeAndOrdinaryKeepsThree(t *testing.T) {
	h, parent, _, _ := sharedGrantFixture(t)
	readSchema := func(want int) {
		t.Helper()
		if err := withSandboxFile(t.Context(), h, false, func(f *sandboxFile) error {
			if f.schema != want {
				t.Fatalf("schema=%d want%d", f.schema, want)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	readSchema(2)
	ordinary := h5Policy()
	ordinary.SessionID, ordinary.request = "uvw234xy", "ordinary-before-derived"
	if err := reserveSandbox(t.Context(), h, ordinary); err != nil {
		t.Fatal(err)
	}
	readSchema(2)
	sharedGrantChild(t, h, parent, "def234gh", nil, TierOwn)
	readSchema(3)
	ordinary.SessionID, ordinary.request = "stu234vw", "ordinary-after-derived"
	if err := reserveSandbox(t.Context(), h, ordinary); err != nil {
		t.Fatal(err)
	}
	readSchema(3)
}
