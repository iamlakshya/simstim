package client

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func writeRetainedImagePolicyFixture(t *testing.T, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(settingsPath()), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath(), []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}

const retainedImagePolicyConfig = `[sandbox.image_policy]
identity = "broader"
issuer = "issuer"
trusted_root = "roots/root.json"
[profiles.work.sandbox.image_policy]
identity = "selected"
`

func TestNewImagePolicyKeepsEffectiveProfileAndOriginalDescriptor(t *testing.T) {
	h := h5GrantState(t)
	writeRetainedImagePolicyFixture(t, retainedImagePolicyConfig)
	c := finishGeneratedTransportFixture(t, h, strings.Repeat("a", 32), "session.id")
	r := NewGrantRequest(false, false)
	ctx := WithGrantRequest(t.Context(), r)
	defer r.Close()
	args := NewArgs{Layers: []api.Layer{{Kind: api.LayerAll}, {Kind: api.LayerHost, Name: h.Name}, {Kind: api.LayerProfile, Name: "work"}, {Kind: api.LayerSession}}}
	ctx, err := captureNewImagePolicy(ctx, c, args)
	if err != nil {
		t.Fatal(err)
	}
	values, close, err := retainedImagePolicy(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	wantRoot := filepath.Join(filepath.Dir(settingsPath()), "roots/root.json")
	if values == nil || *values.Identity != "selected" || *values.Issuer != "issuer" || *values.TrustedRoot != wantRoot {
		t.Fatal("effective original policy was lost", values)
	}
	*values.Identity = "caller mutation"
	if err := close(); err != nil {
		t.Fatal(err)
	}
	again, close, err := retainedImagePolicy(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if *again.Identity != "selected" {
		t.Fatal("borrowed copy changed original policy")
	}
	if err := close(); err != nil {
		t.Fatal(err)
	}
	changed := "changed"
	args.Layers[len(args.Layers)-1].Sandbox.ImagePolicy = &api.ImagePolicyValues{Identity: &changed}
	if _, err := captureNewImagePolicy(ctx, c, args); err == nil {
		t.Fatal("retry replaced effective policy")
	}
	writeRetainedImagePolicyFixture(t, retainedImagePolicyConfig+"\n# descriptor changed\n")
	if err := revalidateRetainedImagePolicy(ctx); err == nil {
		t.Fatal("original descriptor was refreshed")
	}
}

func TestNewImagePolicyMissingCaptureCannotFallBackToBroaderLayers(t *testing.T) {
	h := h5GrantState(t)
	c := finishGeneratedTransportFixture(t, h, strings.Repeat("a", 32), "session.id")
	r := NewGrantRequest(false, false)
	ctx := WithGrantRequest(t.Context(), r)
	defer r.Close()
	if err := requireGrantOperation(ctx, grantOpNew); err != nil {
		t.Fatal(err)
	}
	if _, _, close, err := withRetainedImagePolicy(ctx, c); err == nil {
		close()
		t.Fatal("New without its original capture opened a substitute")
	}
	// The actual constructor still enforces the configured broader policy even
	// for a direct SDK caller whose NewArgs omitted its layer list.
	writeRetainedImagePolicyFixture(t, retainedImagePolicyConfig)
	ctx, err := captureNewImagePolicy(ctx, c, NewArgs{})
	if err != nil {
		t.Fatal(err)
	}
	values, close, err := retainedImagePolicy(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if values == nil || *values.Identity != "broader" {
		t.Fatal("omitting layers omitted configured policy")
	}
	if err := close(); err != nil {
		t.Fatal(err)
	}
}

func TestImagePolicyNativeNamespaceConflictRefusesBeforeUse(t *testing.T) {
	h := h5GrantState(t)
	writeRetainedImagePolicyFixture(t, "[secrets.XDG_CACHE_HOME]\nrun = 'printf unused'\n")
	c := finishGeneratedTransportFixture(t, h, strings.Repeat("a", 32), "session.id")
	if _, _, close, err := withRetainedImagePolicy(t.Context(), c); err == nil {
		close()
		t.Fatal("verifier cache lookup accepted a configured secret name")
	}
}

func TestRetainedImagePolicyRejectsSourceABAAndOriginalCancellation(t *testing.T) {
	for _, mode := range []string{"source", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			h := h5GrantState(t)
			c := finishGeneratedTransportFixture(t, h, strings.Repeat("a", 32), "session.id")
			parent, cancel := context.WithCancel(t.Context())
			defer cancel()
			r := NewGrantRequest(false, false)
			defer r.Close()
			ctx, err := captureNewImagePolicy(WithGrantRequest(parent, r), c, NewArgs{})
			if err != nil {
				t.Fatal(err)
			}
			if mode == "source" {
				if err := SaveHosts(t.Context(), nil); err != nil {
					t.Fatal(err)
				}
				if err := SaveHosts(t.Context(), []Host{h}); err != nil {
					t.Fatal(err)
				}
				if err := revalidateRetainedImagePolicy(ctx); err == nil {
					t.Fatal("identical re-add renewed accepted Source")
				}
			} else {
				cancel()
				if err := revalidateRetainedImagePolicy(context.WithoutCancel(ctx)); !errors.Is(err, context.Canceled) {
					t.Fatal("original lifetime was renewed", err)
				}
			}
		})
	}
}
