package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestOriginalSchema1NonePublicSetStillRunsFreshLiteralReview(t *testing.T) {
	h := h5GrantState(t)
	p := h5Policy()
	entry, err := json.Marshal(savedSandbox{SessionID: p.SessionID, HostID: p.HostID, Ceiling: p.Ceiling.String(),
		Secrets: p.Secrets, CreatedAt: p.CreatedAt, Request: p.request})
	if err != nil {
		t.Fatal(err)
	}
	raw := fmt.Appendf(nil, `{"schema":1,"host":%q,"sessions":{%q:%s}}`, h.Name, p.SessionID, entry)
	writeSandboxFixture(t, h, raw)
	c := finishGeneratedTransportFixture(t, h, p.HostID)
	reads := 0
	values, err := combinedPullReviewValues(t.Context(), func(ctx context.Context) ([]api.Secret, error) {
		return c.ReviewPublicStandIns(ctx, p.SessionID)
	}, func(context.Context) ([][]byte, error) { reads++; return [][]byte{[]byte("short-local-fixture")}, nil })
	if err != nil || reads != 1 || len(values) != 1 || !bytes.Equal(values[0], []byte("short-local-fixture")) {
		t.Fatal("known-empty public set bypassed or prevented fresh local literal review", reads, err)
	}
	stored, err := os.ReadFile(sandboxStatePath(h))
	if err != nil || !bytes.Equal(stored, raw) {
		t.Fatal("legacy public read rewrote its original record", err)
	}
}

func TestMissingModernOrUnknownReceiptNeverBecomesLegacyEmptyPublicSet(t *testing.T) {
	h := h5GrantState(t)
	p := h5Policy()
	entry, err := json.Marshal(savedSandbox{SessionID: p.SessionID, HostID: p.HostID, Ceiling: p.Ceiling.String(),
		Secrets: p.Secrets, CreatedAt: p.CreatedAt, Request: p.request})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name         string
		schema       int
		suffix, root string
		missing      bool
	}{
		{name: "schema2-modern-or-migrated", schema: 2, suffix: `,"wall_intent":{"level":"none"}`},
		{name: "schema3-modern-missing", schema: 3, suffix: `,"wall_intent":{"level":"none"}`},
		{name: "schema4-modern-missing", schema: 4, suffix: `,"wall_intent":{"level":"none"}`},
		{name: "schema1-future-row", schema: 1, suffix: `,"future_session":{"keep":1.00}`},
		{name: "schema1-future-root", schema: 1, root: `,"future_root":{"keep":1.00}`},
		{name: "schema1-modern-input", schema: 1, suffix: `,"input_operation":null`},
		{name: "schema1-modern-delivery", schema: 1, suffix: `,"delivery_intent":null`},
		{name: "missing-original", schema: 1, missing: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := append(bytes.Clone(entry[:len(entry)-1]), test.suffix...)
			body = append(body, '}')
			sessions := fmt.Sprintf(`%q:%s`, p.SessionID, body)
			if test.missing {
				sessions = ""
			}
			raw := fmt.Appendf(nil, `{"schema":%d,"host":%q%s,"sessions":{%s}}`, test.schema, h.Name, test.root, sessions)
			writeSandboxFixture(t, h, raw)
			c := finishGeneratedTransportFixture(t, h, p.HostID)
			reads := 0
			_, err := combinedPullReviewValues(t.Context(), func(ctx context.Context) ([]api.Secret, error) {
				return c.ReviewPublicStandIns(ctx, p.SessionID)
			}, func(context.Context) ([][]byte, error) { reads++; return nil, nil })
			if err == nil || reads != 0 {
				t.Fatal("missing/unknown modern metadata acquired legacy-empty permission", reads, err)
			}
		})
	}
}
