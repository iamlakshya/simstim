package client

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/proto"
)

func resumeImageFixture(t *testing.T, history bool, policy string, features ...string) (*Conn, *GrantRequest, context.Context, SandboxPolicy) {
	t.Helper()
	h := h5GrantState(t)
	writeRetainedImagePolicyFixture(t, policy)
	selectedFeatures := append([]string{"session.id", "resume", api.FeatureResumeImageV1}, features...)
	c := finishGeneratedTransportFixture(t, h, strings.Repeat("a", 32), selectedFeatures...)
	creation := h5Policy()
	creation.CreationWall = "microvm"
	if history {
		request := image.Request{Ref: "ghcr.io/example/tools:stable", Digest: "sha256:" + strings.Repeat("a", 64), PlatformDigest: "sha256:" + strings.Repeat("b", 64), Arch: "amd64", RootHash: strings.Repeat("c", 64), DataBlocks: 1}
		raw, err := encodeSandboxImageSelection(&sandboxImageSelectionV1{Version: 1, Origin: imageOriginOCI, Image: request})
		if err != nil {
			t.Fatal(err)
		}
		creation.imageSelectionRaw = raw
	}
	if err := withSandboxReceiptObservation(t.Context(), c, func(owned context.Context) error { return reserveSandbox(owned, h, creation) }); err != nil {
		t.Fatal(err)
	}
	r := NewGrantRequest(false, false)
	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	})
	return c, r, WithGrantRequest(t.Context(), r), creation
}

func TestResumeImagePolicyMissingHistoryRefusesBeforeValues(t *testing.T) {
	c, _, ctx, creation := resumeImageFixture(t, false, retainedImagePolicyConfig)
	if _, _, err := captureResumeImagePolicy(ctx, c, creation.SessionID); err == nil {
		t.Fatal("active current policy accepted missing original image history")
	}
	if _, err := FetchSessionSecrets(ctx, c.Host, creation.SessionID, nil, false); err == nil {
		t.Fatal("global value helper fabricated the missing original image producer")
	}
}

func TestResumeImagePhysicalWriterUsesOriginalExpectedImageAndReleasesSource(t *testing.T) {
	c, _, ctx, creation := resumeImageFixture(t, true, "")
	ctx, operation, err := captureResumeImagePolicy(ctx, c, creation.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(operation.expected) == 0 {
		t.Fatal("original image projection missing")
	}
	if _, _, err := operation.arguments(ctx, ResumeArgs{ID: creation.SessionID, ExpectedImageV1: json.RawMessage(`{}`)}); err == nil {
		t.Fatal("caller substituted another image projection")
	}
	root := "private/laptop/root.json"
	ctx, args, err := operation.arguments(ctx, ResumeArgs{ID: creation.SessionID, Layers: []api.Layer{{Kind: api.LayerSession, LayerValues: api.LayerValues{Sandbox: api.SandboxValues{ImagePolicy: &api.ImagePolicyValues{TrustedRoot: &root}}}}}})
	if err != nil {
		t.Fatal(err)
	}
	frames := 0
	c.pending = map[uint64]chan proto.Response{}
	sharedGrantWire(t, c, func(req proto.Request) any {
		frames++
		var sent ResumeArgs
		if req.Op != "resume" || json.Unmarshal(req.Args, &sent) != nil || !bytes.Equal(sent.ExpectedImageV1, operation.expected) || bytes.Contains(req.Args, []byte(root)) {
			t.Fatal("physical frame lost original expected image")
		}
		_, unlock, err := LockHost(t.Context(), c.Host.Name, "competing resume image read")
		if err != nil {
			t.Fatal("publication Source was retained across response", err)
		}
		unlock()
		return struct{}{}
	})
	if err := c.Call(ctx, "resume", args, nil); err == nil {
		t.Fatal("private frame sent without actual Source publication ownership")
	}
	args.Layers = api.ProjectClientImagePolicy(args.Layers)
	if err := c.Call(ctx, "resume", args, nil); err == nil {
		t.Fatal("private frame sent without actual Source publication ownership")
	}
	owned, release, err := c.beginGrant(ctx, "resume image fixture", false)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := c.callWithSent(owned, "resume", args, nil, release); err != nil {
		t.Fatal(err)
	}
	if frames != 1 {
		t.Fatal("invalid physical frame reached transport", frames)
	}
}

func TestResumeImageOriginalSettingsAndSourceCannotRenewBeforeFetch(t *testing.T) {
	for _, change := range []string{"settings", "source"} {
		t.Run(change, func(t *testing.T) {
			c, _, ctx, creation := resumeImageFixture(t, true, "")
			ctx, operation, err := captureResumeImagePolicy(ctx, c, creation.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			if change == "settings" {
				writeRetainedImagePolicyFixture(t, "# original descriptor changed\n")
			} else {
				if err := SaveHosts(t.Context(), nil); err != nil {
					t.Fatal(err)
				}
				if err := SaveHosts(t.Context(), []Host{c.Host}); err != nil {
					t.Fatal(err)
				}
			}
			if err := operation.check(ctx); err == nil {
				t.Fatal("original policy source renewed")
			}
			if _, _, err := resumeImageBeforeValues(ctx, c.Host, creation.SessionID, &creation); err == nil {
				t.Fatal("value helper used stale original policy")
			}
		})
	}
}

func TestUnknownCreationActiveImagePolicyRefusesWithoutRPCOrValueFetch(t *testing.T) {
	h := h5GrantState(t)
	writeRetainedImagePolicyFixture(t, retainedImagePolicyConfig)
	c := finishGeneratedTransportFixture(t, h, strings.Repeat("a", 32), "session.id", "resume", api.FeatureResumeImageV1)
	r := NewGrantRequest(false, false)
	defer r.Close()
	ctx := WithGrantRequest(t.Context(), r)
	if _, _, err := captureResumeImagePolicy(ctx, c, "unknown234"); err == nil {
		t.Fatal("unknown creation bypassed active current image policy")
	}
	if _, _, err := resumeImageBeforeValues(ctx, h, "unknown234", nil); err == nil {
		t.Fatal("global helper bypassed active policy for unknown creation")
	}
	// The fixture has no physical writer or RPC peer: a host wall/need row was
	// neither requested nor used to classify this unknown creation.
	if c.nextID.Load() != 0 {
		t.Fatal("unknown creation caused a remote selector")
	}
}
