package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Generated terminal lease fixture for lower CAS/native ordering only. It
// proves no SSH/Hello, actual GrantRequest attempt or remote absence. Main's
// actual private plan/first-writer tests own that production provenance.
type unpublishedRetireFixtureProof struct {
	mu        sync.Mutex
	conn      *Conn
	original  *SandboxPolicy
	attempted bool
	aborting  bool
	held      bool
	releases  int
}

func (p *unpublishedRetireFixtureProof) holdUnpublishedSandboxRetirement(ctx context.Context, c *Conn, original *SandboxPolicy) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.mu.Lock()
	if p.attempted || p.conn != c || !sameSandboxCreation(p.original, original) || p.original.Ceiling != original.Ceiling {
		p.mu.Unlock()
		return nil, sandboxReceiptError()
	}
	p.aborting, p.held = true, true
	return sync.OnceFunc(func() { p.held = false; p.releases++; p.mu.Unlock() }), nil
}

func unpublishedRetireFixture(t *testing.T) (Host, context.Context, *Conn, *SandboxPolicy, *unpublishedRetireFixtureProof) {
	t.Helper()
	h, ctx, observation, _ := hostObservationFixture(t)
	c := observation.checkedConn
	if c == nil {
		t.Fatal("fixture lost explicit generated checked-Conn binding")
	}
	if err := commitSandboxBoundSchema(ctx, h); err != nil {
		t.Fatal(err)
	}
	prospective := SandboxPolicy{SessionID: "abcd2345", HostID: c.HostID(), Ceiling: TierOwn, CreationWall: "none", Secrets: []string{"KEY"},
		CreatedAt: time.Unix(1, 0).UTC(), request: strings.Repeat("a", 32)}
	if err := reserveSandbox(ctx, h, prospective); err != nil {
		t.Fatal(err)
	}
	// The actual reservation snapshot, not the prospective DTO whose nested
	// raw fields had not yet been encoded by the canonical writer.
	original, err := LoadSandboxPolicy(ctx, h, prospective.SessionID)
	if err != nil || original == nil {
		t.Fatalf("capture original saved reservation: %v", err)
	}
	proof := &unpublishedRetireFixtureProof{conn: c, original: cloneSandboxPolicy(original)}
	return h, ctx, c, original, proof
}

func TestUnpublishedSandboxRetirementRemovesOnlyOwnedRowUnderLease(t *testing.T) {
	h, ctx, c, original, proof := unpublishedRetireFixture(t)
	other := *cloneSandboxPolicy(original)
	other.SessionID, other.request = "bcde2345", strings.Repeat("b", 32)
	if err := reserveSandbox(ctx, h, other); err != nil {
		t.Fatal(err)
	}
	if err := withSandboxFile(ctx, h, true, func(file *sandboxFile) error {
		file.root["unrelated-future"] = json.RawMessage(`{ "keep": [1, 2] }`)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(removalDir(h.Name), "sandboxes.json.lock")
	beforeLock, err := os.Lstat(lock)
	if err != nil {
		t.Fatal(err)
	}
	previous := beforeGrantsStateWrite
	defer func() { beforeGrantsStateWrite = previous }()
	beforeGrantsStateWrite = func() {
		if !proof.held || !proof.aborting || proof.attempted {
			t.Fatal("actual lower writer escaped its terminal never-attempted lease")
		}
	}
	retired, err := retireUnpublishedSandboxContext(ctx, c, original, proof)
	if err != nil || !retired || proof.releases != 1 {
		t.Fatalf("owned unpublished retirement: retired=%v release=%d %v", retired, proof.releases, err)
	}
	if err := withSandboxFile(ctx, h, false, func(file *sandboxFile) error {
		if file.sessions[original.SessionID] != nil || file.sessions[other.SessionID] == nil || string(file.root["unrelated-future"]) != `{ "keep": [1, 2] }` {
			t.Fatal("row retirement changed another session or opaque root metadata")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if after, err := os.Lstat(lock); err != nil || !os.SameFile(beforeLock, after) {
		t.Fatal("row retirement removed/replaced its existing sentinel")
	}
}

func TestUnpublishedSandboxRetirementRefusesAttemptedChangedOrFutureRows(t *testing.T) {
	for _, kind := range []string{"attempted", "ceiling", "request", "secrets", "unknown", "delivery-receipt", "input-operation"} {
		t.Run(kind, func(t *testing.T) {
			h, ctx, c, original, proof := unpublishedRetireFixture(t)
			if kind == "attempted" {
				proof.attempted = true
			} else if err := withSandboxFile(ctx, h, true, func(file *sandboxFile) error {
				fields, err := sandboxObjectContext(ctx, file.sessions[original.SessionID])
				if err != nil {
					return err
				}
				switch kind {
				case "ceiling":
					fields["ceiling"] = json.RawMessage(`"shared"`)
				case "request":
					fields["request"] = json.RawMessage(`"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"`)
				case "secrets":
					fields["secrets"] = json.RawMessage(`["OTHER"]`)
				case "unknown":
					fields["future-row-owner"] = json.RawMessage(`{"version":2}`)
				case "delivery-receipt":
					fields["delivery_receipt"] = json.RawMessage(`{"version":2,"future":true}`)
				case "input-operation":
					fields["input_operation"] = json.RawMessage(`{"version":2,"future":true}`)
				}
				file.sessions[original.SessionID], err = marshalSandboxObject(fields)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(removalDir(h.Name), "sandboxes.json")
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if retired, err := retireUnpublishedSandboxContext(ctx, c, original, proof); err == nil || retired {
				t.Fatal("an attempted/changed/future reservation was erased")
			}
			if after, err := os.ReadFile(path); err != nil || !bytes.Equal(before, after) {
				t.Fatal("a refused retirement changed saved bytes")
			}
		})
	}
}

func TestUnpublishedSandboxRetirementMissingStateCreatesNothing(t *testing.T) {
	for _, kind := range []string{"data", "sentinel", "domain"} {
		t.Run(kind, func(t *testing.T) {
			h, ctx, c, original, proof := unpublishedRetireFixture(t)
			path := filepath.Join(removalDir(h.Name), "sandboxes.json")
			removed := path
			if kind == "sentinel" {
				removed += ".lock"
			}
			if kind == "domain" {
				removed = removalDir(h.Name)
				if err := os.RemoveAll(removed); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Remove(removed); err != nil {
				t.Fatal(err)
			}
			if retired, err := retireUnpublishedSandboxContext(ctx, c, original, proof); err == nil || retired {
				t.Fatal("missing local state became retirement proof")
			}
			if _, err := os.Lstat(removed); !os.IsNotExist(err) {
				t.Fatalf("retirement recreated missing %s: %v", kind, err)
			}
		})
	}
}

func TestUnpublishedSandboxRetirementRetainsOnSourceOrCallerCancellation(t *testing.T) {
	for _, kind := range []string{"source", "cancel"} {
		t.Run(kind, func(t *testing.T) {
			h, ctx, c, original, proof := unpublishedRetireFixture(t)
			if kind == "source" {
				if err := SaveHosts(t.Context(), []Host{h}); err != nil {
					t.Fatal(err)
				}
			} else {
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			}
			path := filepath.Join(removalDir(h.Name), "sandboxes.json")
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if retired, err := retireUnpublishedSandboxContext(ctx, c, original, proof); err == nil || retired {
				t.Fatal("stale Source/cancelled caller removed a reservation")
			}
			if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, before) {
				t.Fatal("incomplete cleanup did not retain its local row")
			}
		})
	}
}

func TestUnpublishedSandboxRetirementReportsCommittedCloseUncertainty(t *testing.T) {
	h, ctx, c, original, proof := unpublishedRetireFixture(t)
	marker := errors.New("real close completed, observer adds uncertainty")
	observed := false
	ctx = context.WithValue(ctx, stateOwnedCloseKey{}, func(name string) error {
		if name == "state-root" {
			observed = true
			return marker
		}
		return nil
	})
	retired, err := retireUnpublishedSandboxContext(ctx, c, original, proof)
	if !retired || err == nil || !observed || proof.releases != 1 {
		t.Fatalf("close uncertainty lost actual commit/incomplete state: retired=%v observed=%v release=%d %v", retired, observed, proof.releases, err)
	}
	// Read through a fresh caller context; no old failed-finalisation token is
	// relabelled as success or used as remote absence/completion authority.
	if row, err := LoadSandboxPolicy(WithHostStateObservation(t.Context(), ctx.Value(hostStateObservationKey{}).(*HostStateObservation)), h, original.SessionID); err != nil || row != nil {
		t.Fatalf("committed row replacement was rolled back/invented: %v", err)
	}
}
