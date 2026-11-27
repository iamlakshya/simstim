package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
)

func TestEmptyPullReadsNoReviewValuesOrQuestions(t *testing.T) {
	fixture := makeNativePull(t, "source.txt", "work", false)
	peer := &pullPeerFixture{supported: true}
	called := 0
	result, err := pullSandbox(t.Context(), peer, "fixture-host", "fixture2", PullOptions{Repository: fixture.local, ReadSecrets: func(context.Context) ([][]byte, error) {
		called++
		return nil, errors.New("must never ask for an empty pull")
	}})
	if err != nil || result == nil || !result.Empty || called != 0 {
		t.Fatal("empty export read secret values or asked a question", result, err, called)
	}
}

func TestUnavailableReviewAuthorityKeepsVerifiedQuarantineBeforeBranchMove(t *testing.T) {
	fixture := makeNativePull(t, "source.txt", "work", false)
	peer := nativePullPeer(fixture)
	called := 0
	result, err := pullSandbox(t.Context(), peer, "fixture-host", "fixture2", PullOptions{Repository: fixture.local, Into: "review-target", AllowFlagged: true, ReadSecrets: func(context.Context) ([][]byte, error) {
		called++
		if !strings.Contains(localRefs(t, fixture.local), "refs/"+brand.Name+"/sessions/") {
			t.Fatal("review question ran before any local refs existed")
		}
		return nil, &UserError{Msg: "fixture missing original local authority", Hint: "Restore the original local record.", Code: "review_authority", Kind: KindRefused}
	}})
	var refused *PullReviewError
	if !errors.As(err, &refused) || called != 1 || result == nil || result.ReviewComplete || result.Moved || len(result.Quarantine) == 0 {
		t.Fatal("incomplete review moved a branch or lost quarantine", result, err, called)
	}
	if strings.Contains(localRefs(t, fixture.local), "refs/heads/review-target") {
		t.Fatal("allow-flagged bypassed missing review authority")
	}
	for _, ack := range peer.acks {
		if len(ack.Commits) != 0 {
			t.Fatal("incomplete review acknowledged work")
		}
	}
}

func TestLocalReviewValuesAreReadOnceClearedAndNeverSent(t *testing.T) {
	value := []byte("local-review-sensitive-fixture")
	fixture := makeNativePull(t, "source.txt", string(value)+"\n", false)
	peer := nativePullPeer(fixture)
	called := 0
	result, err := pullSandbox(t.Context(), peer, "fixture-host", "fixture2", PullOptions{Repository: fixture.local, Into: "review-target", ReadSecrets: func(context.Context) ([][]byte, error) {
		called++
		return [][]byte{value}, nil
	}})
	if err == nil || called != 1 || result == nil || !result.ReviewComplete || result.Moved {
		t.Fatal("secret review did not stop on its one local read", result, err, called)
	}
	if !bytes.Equal(value, make([]byte, len(value))) {
		t.Fatal("transferred local secret bytes were retained")
	}
	wire, err := json.Marshal([]any{peer.begins, peer.reads, peer.acks, result})
	if err != nil || bytes.Contains(wire, []byte("local-review-sensitive-fixture")) {
		t.Fatal("secret value appeared in an outgoing request or summary")
	}
}
