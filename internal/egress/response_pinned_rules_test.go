package egress

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestResponsePinnedFamiliesKeepTheirCapturesAcrossEverySplit(t *testing.T) {
	rules, err := pinnedResponseRuleSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id, prefix     string
		left, right    int
		suffix, marker string
	}{
		{id: "anthropic-api-key", prefix: "sk-ant-api03-", left: 93, suffix: "AA"},
		{id: "anthropic-admin-api-key", prefix: "sk-ant-admin01-", left: 93, suffix: "AA"},
		{id: "openai-api-key", prefix: "sk-", left: 20, right: 20, marker: "T3BlbkFJ"},
		{id: "openai-api-key", prefix: "sk-svcacct-", left: 74, right: 74, marker: "T3BlbkFJ"},
	} {
		value := tc.prefix + strings.Repeat("A", tc.left) + tc.marker + strings.Repeat("B", tc.right) + tc.suffix
		sentinel := tc.prefix + strings.Repeat("C", tc.left) + tc.marker + strings.Repeat("D", tc.right) + tc.suffix
		input := "\"" + value + "\";" + value + ":"
		want := "\"" + sentinel + "\";" + sentinel + ":"
		for split := 0; split <= len(input); split++ {
			var current atomic.Pointer[responseEchoSnapshot]
			current.Store(responseTestEchoes(t))
			calls := 0
			reader, err := newResponseCredentialReader(t.Context(), &responsePieceReader{pieces: []string{input[:split], input[split:]}}, func(context.Context) (*responseEchoSnapshot, error) { return current.Load(), nil }, rules, func(_ context.Context, candidate api.Secret, id string) (api.Secret, error) {
				calls++
				if candidate != api.Secret(value) || id != tc.id {
					return "", errors.New("pinned candidate identity changed")
				}
				snapshot, err := newResponseEchoSnapshot([]responseEchoPair{{Value: candidate, Sentinel: api.Secret(sentinel)}})
				if err != nil {
					return "", err
				}
				current.Store(snapshot)
				return api.Secret(sentinel), nil
			})
			if err != nil {
				t.Fatal(err)
			}
			got, readErr := io.ReadAll(reader)
			_ = reader.Close()
			if readErr != nil || string(got) != want || calls != 1 {
				t.Fatalf("pinned%s split%d lost capture/shape/refresh: callbacks=%d error=%v", tc.id, split, calls, readErr)
			}
		}
	}
}
