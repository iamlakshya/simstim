package client

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/proto"
)

func imageSaveValuesFixture(t *testing.T, ceiling Tier, parents ...context.Context) (*imageSaveOperation, context.Context, *Session, string) {
	t.Helper()
	h := h5GrantState(t)
	c := finishGeneratedTransportFixture(t, h, strings.Repeat("a", 32), "session.id", "image.save", "secrets")
	c.pending = map[uint64]chan proto.Response{}
	r := NewImageSaveGrantRequest()
	parent := t.Context()
	if len(parents) != 0 {
		parent = parents[0]
	}
	ctx := WithGrantRequest(parent, r)
	t.Cleanup(func() { _ = r.Close() })
	p := h5Policy()
	p.Ceiling, p.CreationWall = ceiling, "microvm"
	owned, release, err := c.beginGrantSource(ctx, "image save fixture", true)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := reserveSandbox(owned, h, p); err != nil {
		t.Fatal(err)
	}
	release()
	marker := filepath.Join(t.TempDir(), "original-value-command")
	command := "printf x >>" + ShellWord(marker) + "; printf generated-image-scan-value"
	config, err := json.Marshal(command)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath(), append(append([]byte("[secrets]\nAPI_KEY = "), config...), '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	op, err := newImageSaveOperation(ctx, c, p.SessionID, "devtools", false)
	if err != nil {
		t.Fatal(err)
	}
	row := &Session{ID: p.SessionID, Secrets: []string{"API_KEY"}, Alive: true, Wall: &api.WallInfo{Level: "microvm"}}
	return op, ctx, row, marker
}

func TestImageSaveClosedProducerFetchesAndRecordsOnlyOriginalSessionValues(t *testing.T) {
	op, ctx, row, marker := imageSaveValuesFixture(t, TierOwn)
	saves := 0
	sharedGrantWire(t, op.conn, func(request proto.Request) any {
		switch request.Op {
		case "ls":
			return []Session{*row}
		case "image.save":
			saves++
			var args image.SaveArgsV1
			if json.Unmarshal(request.Args, &args) != nil || args.Version != 1 || args.Session != row.ID || args.Request.Ref != "devtools" ||
				len(args.Request.Values) != 1 || args.Request.Values["API_KEY"] != "generated-image-scan-value" {
				t.Fatal("physical save changed its original route or selected values")
			}
			// Inspect as data while the actual writer still holds its OP.
			data, err := os.ReadFile(filepath.Join(StateDir(), grantsFileName))
			if err != nil || strings.Contains(string(data), "generated-image-scan-value") {
				t.Fatal("physical save lacked value-free exposure history", err)
			}
			ledger, err := decodeGrantsFile(data)
			if err != nil || len(ledger.Grants) != 1 || ledger.Grants[0].Name != "API_KEY" || ledger.Grants[0].Count != 1 {
				t.Fatal("image-save exposure did not use the canonical per-secret ledger", err)
			}
			page, err := imageSaveReviewFixture(t, 0).PageV1(0)
			if err != nil {
				t.Fatal(err)
			}
			return page
		default:
			t.Fatal("unexpected image save operation", request.Op)
			return nil
		}
	})
	if err := op.prepare(ctx, false); err != nil {
		t.Fatal(err)
	}
	if err := op.prepare(ctx, false); err != nil {
		t.Fatal("same original selection could not retain its captured values", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("the actual selected value was not fetched", err)
	}
	first, err := op.save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	second, err := op.save(ctx)
	if err != nil || saves != 1 || first.ID != second.ID {
		t.Fatal("same request repeated an actual image-save publication", saves, err)
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "x" {
		t.Fatal("same original image save ran its selected command again", err)
	}
	if err := op.request.Close(); err != nil || len(op.values) != 0 {
		t.Fatal("request close retained private image scanner values", err)
	}
}

func TestImageSaveIncompletePublicationCannotBeRepeated(t *testing.T) {
	op, ctx, row, _ := imageSaveValuesFixture(t, TierOwn)
	saves := 0
	sharedGrantWire(t, op.conn, func(request proto.Request) any {
		if request.Op == "ls" {
			return []Session{*row}
		}
		if request.Op != "image.save" {
			t.Fatal("unexpected operation", request.Op)
		}
		saves++
		return nil // Successful transport without a complete save ACK.
	})
	if err := op.prepare(ctx, false); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := op.save(ctx); err == nil {
			t.Fatal("missing complete save ACK became a successful publication")
		}
	}
	if saves != 1 {
		t.Fatal("uncertain actual publication was attempted again", saves)
	}
}

func TestImageSaveCapturedSourceCanOnlyDropAfterCancellation(t *testing.T) {
	parent, cancel := context.WithCancel(t.Context())
	defer cancel()
	op, ctx, row, _ := imageSaveValuesFixture(t, TierOwn, parent)
	sharedGrantWire(t, op.conn, func(request proto.Request) any {
		if request.Op != "ls" {
			t.Fatal("cleanup test entered an application writer", request.Op)
		}
		return []Session{*row}
	})
	if err := op.prepare(ctx, false); err != nil {
		t.Fatal(err)
	}
	cancel()
	cleanup := context.WithoutCancel(ctx)
	if _, release, err := op.begin(cleanup, "keep image"); !errors.Is(err, context.Canceled) {
		if release != nil {
			release()
		}
		t.Fatal("cancelled original operation regained keep permission", err)
	}
	_, release, err := op.beginCleanup(cleanup)
	if err != nil {
		t.Fatal("fixed drop could not revalidate the same captured physical sources", err)
	}
	release()
	if err := op.request.Close(); err != nil || len(op.values) != 0 {
		t.Fatal("cancelled original operation retained scanner values", err)
	}
}

func TestImageSaveChecksNamesAndCeilingBeforeAnyValueCommand(t *testing.T) {
	for _, mode := range []string{"foreign name", "lower ceiling", "local review purpose"} {
		t.Run(mode, func(t *testing.T) {
			ceiling := TierOwn
			if mode == "lower ceiling" {
				ceiling = TierThrowaway
			}
			op, ctx, row, marker := imageSaveValuesFixture(t, ceiling)
			if mode == "foreign name" {
				row.Secrets = append(row.Secrets, "UNRECORDED_KEY")
			}
			sharedGrantWire(t, op.conn, func(request proto.Request) any {
				if request.Op != "ls" {
					t.Fatal("refused image save entered an application writer", request.Op)
				}
				return []Session{*row}
			})
			if mode == "local review purpose" {
				other := NewReviewGrantRequest()
				defer other.Close()
				ctx = WithGrantRequest(t.Context(), other)
			}
			if err := op.prepare(ctx, false); err == nil {
				t.Fatal("changed purpose/name/ceiling granted image scanner values")
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatal("a refused image save ran a value command", err)
			}
		})
	}
}
