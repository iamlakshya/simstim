package client

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/term"
)

// The sole production dial factory accepts real framed hellos over the
// fixture's owned pipes. Native state/OP/source files are the test's own.
func restrictedShellParent(t *testing.T) (Host, *Conn, *atomic.Int32) {
	t.Helper()
	h := h5GrantState(t)
	transport := newPrepareBridgeTransport(t, strings.Repeat("a", 32), []string{"session.id"}, func(request proto.Request, _ *proto.Writer) (any, *proto.Error, bool) {
		return map[string]string{"op": request.Op}, nil, false
	})
	parent, err := dial(t.Context(), h, route{t: transport})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(parent.Close)
	return h, parent, &transport.starts
}

func TestRestrictedShellDialRejectsSelectionMutationBeforeTransport(t *testing.T) {
	for _, mutation := range []string{"wrong-host", "identical-remove-readd", "host-config", "settings-config", "tier", "parent-end", "cancel"} {
		t.Run(mutation, func(t *testing.T) {
			h, parent, starts := restrictedShellParent(t)
			owner, cancel := context.WithCancel(t.Context())
			defer cancel()
			bound, closeScope, err := parent.RestrictSavedHostSourceV1(owner)
			if err != nil {
				if closeScope != nil {
					_ = closeScope()
				}
				t.Fatal(err)
			}
			defer closeScope()
			selected := h
			switch mutation {
			case "wrong-host":
				selected.Name = "other"
			case "identical-remove-readd":
				if err := SaveHosts(t.Context(), nil); err != nil {
					t.Fatal(err)
				}
				if err := SaveHosts(t.Context(), []Host{h}); err != nil {
					t.Fatal(err)
				}
			case "host-config":
				next := h
				next.Labels = map[string]string{"changed": "after selection"}
				if err := SaveHosts(t.Context(), []Host{next}); err != nil {
					t.Fatal(err)
				}
			case "settings-config":
				if err := os.WriteFile(settingsPath(), []byte("[network]\nunknown = 'deny'\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "tier":
				if err := SetTierContext(t.Context(), h.Name, TierShared, parent.HostID(), ""); err != nil {
					t.Fatal(err)
				}
			case "parent-end":
				parent.Close()
			case "cancel":
				cancel()
			}
			child, err := Dial(bound, selected, false)
			if child != nil {
				child.Close()
				t.Fatal("a changed selection opened a new transport")
			}
			if err == nil || starts.Load() != 1 {
				t.Fatal("changed original Source/tier/config reached transport startup", mutation, err, starts.Load())
			}
			if mutation == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatal("cancellation lost its original cause", err)
			}
		})
	}
}

func TestRestrictedShellRejectsMetadataChangeBeforeGrant(t *testing.T) {
	for _, mutation := range []string{"config", "tier"} {
		t.Run(mutation, func(t *testing.T) {
			h, parent, starts := restrictedShellParent(t)
			bound, closeScope, err := parent.RestrictSavedHostSourceV1(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer closeScope()
			child, err := Dial(bound, h, false)
			if err != nil {
				t.Fatal(err)
			}
			defer child.Close()
			if mutation == "config" {
				if err := os.WriteFile(settingsPath(), []byte("[network]\nunknown = 'deny'\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			} else if err := SetTierContext(t.Context(), h.Name, TierShared, parent.HostID(), ""); err != nil {
				t.Fatal(err)
			}
			_, release, err := child.beginGrantSource(bound, "changed shell metadata", true)
			if release != nil {
				release()
			}
			if err == nil || starts.Load() != 2 {
				t.Fatal("a metadata mutation refreshed the original grant restriction", err)
			}
		})
	}
}

func TestRestrictedShellContinuationPreservesFramedOutputBytes(t *testing.T) {
	h := h5GrantState(t)
	hostID := strings.Repeat("a", 32)
	parentProc, parentOutput := continuationHelloProc(t, hostID)
	childProc, childOutput := continuationHelloProc(t, hostID)
	var starts atomic.Int32
	transport := &lifecycleTransport{start: func(context.Context) (Proc, error) {
		if starts.Add(1) == 1 {
			return parentProc, nil
		}
		return childProc, nil
	}}
	parent, err := dial(t.Context(), h, route{t: transport})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = parentOutput.Close(); parent.Close() }()
	bound, closeScope, err := parent.RestrictSavedHostSourceV1(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer closeScope()
	prepared := false
	child, err := dialWithPreparation(bound, h, false, func(_ context.Context, actual Transport) error {
		prepared = actual == transport
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = childOutput.Close(); child.Close() }()
	if !prepared || !sameHostTransportSource(parent.hostTransport.source, child.hostTransport.source) || child.route.t != parent.route.t {
		t.Fatal("the restricted continuation refreshed its actual original route or Source")
	}
	data := append(bytes.Repeat([]byte("\x00\x01\xff\xc3\xa9\r\n"), 128), []byte("\x1b[38;2;1;2;3mexact\x1b[0m")...)
	written := make(chan error, 1)
	go func() { written <- proto.NewWriter(childOutput).Write(proto.TOutput, proto.EncodeOutput(0, data)) }()
	var frame proto.Frame
	select {
	case frame = <-child.Stream():
	case <-time.After(3 * time.Second):
		t.Fatal("the original framed continuation produced no output")
	}
	if err := <-written; err != nil {
		t.Fatal(err)
	}
	out, err := os.OpenFile(filepath.Join(t.TempDir(), "terminal-bytes"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	a := &attacher{o: AttachOptions{Out: out}, tracker: term.NewTracker(), guard: newPasteGuard(clipMemoryAt(filepath.Join(t.TempDir(), "guard")))}
	if _, err := a.frame(bound, frame); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(out.Name())
	if err != nil || !bytes.Equal(got, data) {
		t.Fatal("the actual output pump changed continuation bytes", err)
	}
	if err := parent.CheckSavedSource(t.Context()); err != nil {
		t.Fatal("the shell continuation retired the original parent", err)
	}
}

func TestRestrictedShellRefusesUnstampedAndClosedScopes(t *testing.T) {
	if _, closeScope, err := (&Conn{}).RestrictSavedHostSourceV1(t.Context()); err == nil || closeScope != nil {
		t.Fatal("a raw connection issued a saved-host restriction")
	}
	h, parent, starts := restrictedShellParent(t)
	bound, closeScope, err := parent.RestrictSavedHostSourceV1(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := closeScope(); err != nil {
		t.Fatal(err)
	}
	if child, err := Dial(bound, h, false); err == nil || child != nil || starts.Load() != 1 {
		t.Fatal("a retired restriction revived a transport", err)
	}
}
