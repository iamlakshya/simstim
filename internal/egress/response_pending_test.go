package egress

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

func responsePendingView(t *testing.T, pending bool) *responseEchoSnapshot {
	t.Helper()
	base, err := newResponseEchoGroupSnapshot([]responseEchoValueGroup{
		{Value: "pending-value", Choices: []responseEchoChoice{{Identity: "owner", Sentinel: "pending-safe!", Pending: pending}}},
		{Value: "current-value", Choices: []responseEchoChoice{{Identity: "owner", Sentinel: "current-safe!"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	view, err := base.view([]string{"owner"})
	if err != nil {
		t.Fatal(err)
	}
	return view
}

func TestResponsePendingValuesStaySparseFullAndPrefixCandidates(t *testing.T) {
	view := responsePendingView(t, true)
	if !view.starts('p') || view.maxBytes < len("pending-value") {
		t.Fatal("pending real disappeared from first-byte or carry metadata")
	}
	partial := view.match([]byte("pending-"), false)
	if !partial.wait || partial.length != 0 {
		t.Fatal("pending real prefix was released as unrelated data")
	}
	full := view.match([]byte("pending-value:"), false)
	if full.length != len("pending-value") || !errors.Is(full.err, errResponseEchoPending) {
		t.Fatal("pending value did not fail at its actual exact candidate")
	}
	if _, ok := view.exact("pending-value"); ok {
		t.Fatal("pending sentinel was called a committed pair")
	}
	if ready, ok := view.exact("current-value"); !ok || ready != "current-safe!" {
		t.Fatal("unrelated ready value was blocked by pending state")
	}
	const input = "before pending-value:"
	for split := 0; split <= len(input)+1; split++ {
		pieces := []string{}
		if split > len(input) {
			for i := range len(input) {
				pieces = append(pieces, input[i:i+1])
			}
		} else {
			pieces = []string{input[:split], input[split:]}
		}
		reader, err := newResponseCredentialReader(t.Context(), &responsePieceReader{pieces: pieces}, func(context.Context) (*responseEchoSnapshot, error) { return view, nil }, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		got, readErr := io.ReadAll(reader)
		_ = reader.Close()
		if !errors.Is(readErr, errResponseEchoPending) || strings.Contains(string(got), "pending") {
			t.Fatal("pending colon echo released its real or future sentinel bytes")
		}
	}
}

func TestResponsePendingChoiceCannotUseAContextFreeReadyFallback(t *testing.T) {
	for _, sameSentinel := range []bool{false, true} {
		pendingSentinel := api.Secret("other-value!")
		if sameSentinel {
			pendingSentinel = "first-value!"
		}
		base, err := newResponseEchoGroupSnapshot([]responseEchoValueGroup{{Value: "shared-value", Choices: []responseEchoChoice{{Identity: "ready", Sentinel: "first-value!"}, {Identity: "pending", Sentinel: pendingSentinel, Pending: true}}}})
		if err != nil {
			t.Fatal(err)
		}
		for _, used := range [][]string{nil, {"pending"}, {"ready", "pending"}} {
			view, err := base.view(used)
			if err != nil {
				t.Fatal(err)
			}
			match := view.match([]byte("shared-value:"), false)
			if !errors.Is(match.err, errResponseEchoPending) {
				t.Fatal("ready aliases/ambiguity bypassed selected or context-free pending state")
			}
		}
		view, err := base.view([]string{"ready"})
		if err != nil {
			t.Fatal(err)
		}
		if got, ok := view.exact("shared-value"); !ok || got != "first-value!" {
			t.Fatal("legitimately selected distinct ready identity lost its authority")
		}
	}
	if _, err := newResponseEchoGroupSnapshot([]responseEchoValueGroup{{Value: "shared-value", Choices: []responseEchoChoice{{Identity: "same", Sentinel: "first-value!"}, {Identity: "same", Sentinel: "first-value!", Pending: true}}}}); err == nil {
		t.Fatal("one identity published contradictory readiness")
	}
}

func TestResponseReadyPublicationRefreshesTheSameGenerationView(t *testing.T) {
	var current atomic.Pointer[responseEchoSnapshot]
	current.Store(responsePendingView(t, true))
	reader, err := newResponseCredentialReader(t.Context(), &responsePieceReader{pieces: []string{"!pending-value:"}}, func(context.Context) (*responseEchoSnapshot, error) { return current.Load(), nil }, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	first := make([]byte, 1)
	if n, err := reader.Read(first); err != nil || n != 1 || first[0] != '!' {
		t.Fatal("independent prefix did not stream while reservation was pending")
	}
	current.Store(responsePendingView(t, false))
	got, err := io.ReadAll(reader)
	if err != nil || string(got) != "pending-safe!:" {
		t.Fatal("durable-ready snapshot was not refreshed before later candidate emission")
	}
}

func TestResponseDurableRegistrationWaitScopesPendingFailureToItsOwnResponse(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(map[bool]string{false: "ready-ack", true: "cancel-before-ack"}[cancelled], func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var current atomic.Pointer[responseEchoSnapshot]
			current.Store(responseTestEchoes(t))
			reserved, ack := make(chan struct{}), make(chan struct{})
			reader, err := newResponseCredentialReader(ctx, io.NopCloser(strings.NewReader("tok_ABCDEF\n")), func(context.Context) (*responseEchoSnapshot, error) { return current.Load(), nil }, responseTestRules(t), func(ctx context.Context, value api.Secret, _ string) (api.Secret, error) {
				publish := func(pending bool) error {
					base, err := newResponseEchoGroupSnapshot([]responseEchoValueGroup{{Value: value, Choices: []responseEchoChoice{{Identity: "owner", Sentinel: "tok_ZZZZZZ", Pending: pending}}}})
					if err != nil {
						return err
					}
					view, err := base.view([]string{"owner"})
					if err != nil {
						return err
					}
					current.Store(view)
					return nil
				}
				if err := publish(true); err != nil {
					return "", err
				}
				close(reserved)
				select {
				case <-ack:
				case <-ctx.Done():
					return "", ctx.Err()
				}
				if err := publish(false); err != nil {
					return "", err
				}
				return "tok_ZZZZZZ", nil
			})
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			result := make(chan struct {
				data []byte
				err  error
			}, 1)
			go func() {
				data, err := io.ReadAll(reader)
				result <- struct {
					data []byte
					err  error
				}{data, err}
			}()
			select {
			case <-reserved:
			case <-time.After(5 * time.Second):
				t.Fatal("mint reservation did not publish pending real")
			}
			other, err := newResponseCredentialReader(t.Context(), io.NopCloser(strings.NewReader("tok_ABCDEF:")), func(context.Context) (*responseEchoSnapshot, error) { return current.Load(), nil }, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			otherData, otherErr := io.ReadAll(other)
			_ = other.Close()
			if len(otherData) != 0 || !errors.Is(otherErr, errResponseEchoPending) {
				t.Fatal("concurrent colon echo bypassed durable pending state")
			}
			if ctx.Err() != nil {
				t.Fatal("one pending response failure cancelled another owner")
			}
			select {
			case <-result:
				t.Fatal("registrar emitted before durable ready ACK")
			default:
			}
			if cancelled {
				cancel()
			} else {
				close(ack)
			}
			select {
			case got := <-result:
				if cancelled {
					if len(got.data) != 0 || !errors.Is(got.err, context.Canceled) {
						t.Fatal("cancel-before-ACK emitted a real or pending sentinel")
					}
				} else if got.err != nil || string(got.data) != "tok_ZZZZZZ\n" {
					t.Fatal("ready ACK did not precede registered sentinel emission")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("owned durable registration wait did not finish")
			}
		})
	}
}
