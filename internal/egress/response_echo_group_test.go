package egress

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func responseTestGroups(t *testing.T) *responseEchoGroupSnapshot {
	t.Helper()
	base, err := newResponseEchoGroupSnapshot([]responseEchoValueGroup{
		{Value: "shared-value", Choices: []responseEchoChoice{{Identity: "first", Sentinel: "first-value!"}, {Identity: "second", Sentinel: "other-value!"}}},
		{Value: "plain-value", Choices: []responseEchoChoice{{Identity: "unique", Sentinel: "safe--value"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return base
}

func TestResponseSparseViewsResolveOnlyActualCanonicalIdentities(t *testing.T) {
	base := responseTestGroups(t)
	for _, tc := range []struct {
		used []string
		want api.Secret
		good bool
	}{
		{nil, "", false}, {[]string{"first"}, "first-value!", true}, {[]string{"second"}, "other-value!", true}, {[]string{"first", "first"}, "first-value!", true}, {[]string{"first", "second"}, "", false}, {[]string{"unique"}, "", false},
	} {
		view, err := base.view(tc.used)
		if err != nil {
			t.Fatal(err)
		}
		if view.base != base || view.maxBytes != base.maxBytes {
			t.Fatal("response view cloned or lost its shared source base")
		}
		got, ok := view.exact("shared-value")
		if ok != tc.good || ok && got != tc.want {
			t.Fatal("ambiguous value chose an arbitrary canonical identity")
		}
		if unique, ok := view.exact("plain-value"); !ok || unique != "safe--value" {
			t.Fatal("unrelated identity ambiguity blocked an unambiguous value")
		}
		if !view.starts('s') {
			t.Fatal("unresolved values disappeared from the sparse first-byte index")
		}
		partial := view.match([]byte("shared-"), false)
		if !partial.wait || partial.length != 0 {
			t.Fatal("unresolved value prefix was dropped before it could complete")
		}
	}
	if _, err := base.view([]string{"not-a-canonical-identity"}); err == nil {
		t.Fatal("unknown trusted request identity entered a view")
	}
	if len(base.buckets) != 2 {
		t.Fatal("sparse source index allocated unrelated first-byte buckets")
	}
}

func TestResponseUnrelatedAmbiguityWaitsAndRefusesBeforeItsBytes(t *testing.T) {
	base := responseTestGroups(t)
	view, err := base.view(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		input, want string
		refuse      bool
	}{
		{"ordinary tail", "ordinary tail", false},
		{"plain-value", "safe--value", false},
		{"shared-valuX", "shared-valuX", false},
		{"before shared-value after", "", true},
	} {
		for split := 0; split <= len(tc.input); split++ {
			reader, err := newResponseCredentialReader(t.Context(), &responsePieceReader{pieces: []string{tc.input[:split], tc.input[split:]}}, func(context.Context) (*responseEchoSnapshot, error) { return view, nil }, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			got, readErr := io.ReadAll(reader)
			_ = reader.Close()
			if tc.refuse {
				if readErr == nil || strings.Contains(string(got), "shared") {
					t.Fatal("unresolved original candidate bytes escaped")
				}
			} else if readErr != nil || string(got) != tc.want {
				t.Fatal("unrelated ambiguity refused or changed ordinary data")
			}
		}
	}
}

func TestResponseUnresolvedShortValueDoesNotOverrideALongerCompleteEcho(t *testing.T) {
	base, err := newResponseEchoGroupSnapshot([]responseEchoValueGroup{
		{Value: "shared-value", Choices: []responseEchoChoice{{Identity: "first", Sentinel: "first-value!"}, {Identity: "second", Sentinel: "other-value!"}}},
		{Value: "shared-value-ext", Choices: []responseEchoChoice{{Identity: "long", Sentinel: "longer-safe-text"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	view, err := base.view(nil)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := newResponseCredentialReader(t.Context(), &responsePieceReader{pieces: []string{"shared-value", "-ext"}}, func(context.Context) (*responseEchoSnapshot, error) { return view, nil }, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	got, readErr := io.ReadAll(reader)
	if readErr != nil || string(got) != "longer-safe-text" {
		t.Fatal("unresolved shorter match cut through a longer viable exact echo")
	}
}

func TestResponseGroupAliasChoicesAreOwnedAndCoalescedWithoutPlaceholderSentinels(t *testing.T) {
	choices := []responseEchoChoice{{Identity: "alias-a", Sentinel: "first-value!"}, {Identity: "alias-b", Sentinel: "first-value!"}}
	base, err := newResponseEchoGroupSnapshot([]responseEchoValueGroup{{Value: "shared-value", Choices: choices}, {Value: "shared-value", Choices: []responseEchoChoice{{Identity: "alias-a", Sentinel: "first-value!"}}}})
	if err != nil {
		t.Fatal(err)
	}
	choices[0].Sentinel = "other-value!"
	view, err := base.view(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := view.exact("shared-value"); !ok || got != "first-value!" {
		t.Fatal("identical canonical aliases lost their one unambiguous sentinel")
	}
	if base.vectors["alias-a"] != nil || base.vectors["alias-b"] != nil {
		t.Fatal("unambiguous aliases allocated unnecessary override vectors")
	}
	for _, bad := range []responseEchoValueGroup{
		{Value: "shared-value"},
		{Value: "shared-value", Choices: []responseEchoChoice{{Identity: "", Sentinel: "first-value!"}}},
		{Value: "shared-value", Choices: []responseEchoChoice{{Identity: "id", Sentinel: "short"}}},
		{Value: "shared-value", Choices: []responseEchoChoice{{Identity: "id", Sentinel: "first-value!"}, {Identity: "id", Sentinel: "other-value!"}}},
	} {
		if _, err := newResponseEchoGroupSnapshot([]responseEchoValueGroup{bad}); err == nil {
			t.Fatal("invalid canonical source choices acquired an echo view")
		}
	}
}

func TestResponseRefreshAndCommittedPairUseTheSelectedCurrentView(t *testing.T) {
	for _, wrongCommit := range []bool{false, true} {
		var current atomic.Pointer[responseEchoSnapshot]
		current.Store(responseTestEchoes(t))
		reader, err := newResponseCredentialReader(t.Context(), io.NopCloser(strings.NewReader("tok_ABCDEF\ntok_ABCDEF:")), func(context.Context) (*responseEchoSnapshot, error) { return current.Load(), nil }, responseTestRules(t), func(_ context.Context, value api.Secret, _ string) (api.Secret, error) {
			base, err := newResponseEchoGroupSnapshot([]responseEchoValueGroup{{Value: value, Choices: []responseEchoChoice{{Identity: "first", Sentinel: "tok_ZZZZZZ"}, {Identity: "second", Sentinel: "tok_YYYYYY"}}}})
			if err != nil {
				return "", err
			}
			view, err := base.view([]string{"first"})
			if err != nil {
				return "", err
			}
			current.Store(view)
			if wrongCommit {
				return "tok_YYYYYY", nil
			}
			return "tok_ZZZZZZ", nil
		})
		if err != nil {
			t.Fatal(err)
		}
		got, readErr := io.ReadAll(reader)
		_ = reader.Close()
		if wrongCommit {
			if readErr == nil || len(got) != 0 {
				t.Fatal("unselected committed identity emitted a sentinel or original")
			}
		} else if readErr != nil || string(got) != "tok_ZZZZZZ\ntok_ZZZZZZ:" {
			t.Fatal("refresh failed to select committed identity and subsequent exact echo")
		}
	}
}

func TestResponseGroupedDerivedTypesRedactEveryVerbAndValueForm(t *testing.T) {
	const marker = "group-format-marker-123"
	choice := responseEchoChoice{Identity: "id", Sentinel: marker}
	input := responseEchoValueGroup{Value: marker, Choices: []responseEchoChoice{choice}}
	group := responseEchoGroup{Value: marker, Choices: []responseEchoChoice{choice}}
	override := responseEchoOverride{group: 0, sentinel: marker}
	vector := responseEchoOverrideVector{identity: "id", entries: []responseEchoOverride{override}}
	base := responseEchoGroupSnapshot{groups: []responseEchoGroup{group}}
	for _, value := range []any{choice, &choice, input, &input, group, &group, override, &override, vector, &vector, base, &base} {
		for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%X"} {
			if fmt.Sprintf(verb, value) != api.Redacted {
				t.Fatal("grouped derived value bypassed its explicit Formatter")
			}
		}
	}
}
