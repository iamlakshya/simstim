package egress

import (
	"reflect"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestBoundRestorePreservesGenericClassesWhenRandomPunctuationSamplesSeparators(t *testing.T) {
	for _, row := range []struct {
		name, original, sampled string
		policy                  sentinelPunctuationPolicy
	}{
		{"bearer dash", "A~0", "A-0", sentinelPunctuationToken68},
		{"bearer underscore", "A~0", "A_0", sentinelPunctuationToken68},
		{"bearer repeated", "A~~0", "A/+0", sentinelPunctuationToken68},
		{"raw separator", "A!0", "A:0", sentinelPunctuationPrintable},
		{"raw repeated", "A!!0", "A=/0", sentinelPunctuationPrintable},
		{"Unicode fixed width", "Ж!５", "Я-８", sentinelPunctuationPrintable},
	} {
		t.Run(row.name, func(t *testing.T) {
			original, err := compileSentinelFormat(t.Context(), "KEY", api.Secret(row.original), "", "", row.policy)
			if err != nil {
				t.Fatal(err)
			}
			public, err := publicSecretShape(t.Context(), "KEY", original, "")
			if err != nil {
				t.Fatal(err)
			}
			before := api.ClonePublicSecretStates(map[string]api.PublicSecretState{"KEY": {Mode: api.SecretModeBound, Shape: public, Sentinel: api.Secret(row.sampled)}})["KEY"]
			saved := api.ClonePublicSecretStates(map[string]api.PublicSecretState{"KEY": before})["KEY"]
			restored, err := restoreSecretShape(t.Context(), "KEY", saved, "", row.policy)
			if err != nil || !sameSecretShape(original, restored) {
				t.Fatal("valid public random sample changed the saved source class on restore")
			}
			if !reflect.DeepEqual(saved, before) {
				t.Fatal("restore mutated the persisted public shape")
			}
			if err := checkSentinelValue(t.Context(), "KEY", api.Secret(row.original), restored); err != nil {
				t.Fatal("restored shape no longer accepts its source format")
			}
		})
	}
}

func TestBoundRestoreRefusesForgedObservedClassesAndNoncanonicalMetadata(t *testing.T) {
	shape, err := compileSentinelFormat(t.Context(), "KEY", "A~0", "", "", sentinelPunctuationToken68)
	if err != nil {
		t.Fatal(err)
	}
	public, err := publicSecretShape(t.Context(), "KEY", shape, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*api.SecretShape){
		func(p *api.SecretShape) { p.Slots[1].Class, p.Slots[1].ASCII = "ascii", "~" },
		func(p *api.SecretShape) { p.Slots[0].Class, p.Slots[0].ASCII = "ascii", "AB" },
		func(p *api.SecretShape) { p.Slots[1].Class = "ascii-printable-punct" },
		func(p *api.SecretShape) { p.Slots[0].ASCII = "A" },
		func(p *api.SecretShape) { p.Slots[0].Width = 2 },
		func(p *api.SecretShape) { p.Slots[0].Count = 0 },
		func(p *api.SecretShape) { p.Rule = "unknown-grammar" },
		func(p *api.SecretShape) { p.Prefix = "caller-prefix" },
	} {
		saved := api.ClonePublicSecretStates(map[string]api.PublicSecretState{"KEY": {Mode: api.SecretModeBound, Shape: public, Sentinel: "A~0"}})["KEY"]
		mutate(saved.Shape)
		if _, err := restoreSecretShape(t.Context(), "KEY", saved, "", sentinelPunctuationToken68); err == nil {
			t.Fatal("forged generic source class was accepted")
		}
	}
}

func TestBoundRestoreValidatesPinnedGrammarWithoutWideningSuppliedSlots(t *testing.T) {
	value := api.Secret("sk-" + strings.Repeat("A", 20) + "T3BlbkFJ" + strings.Repeat("B", 20))
	shape, rule, err := compileBoundShape(t.Context(), "KEY", value, "", sentinelPunctuationToken68)
	if err != nil || rule == "" {
		t.Fatal("pinned test format did not compile")
	}
	public, err := publicSecretShape(t.Context(), "KEY", shape, rule)
	if err != nil {
		t.Fatal(err)
	}
	saved := api.PublicSecretState{Mode: api.SecretModeBound, Sentinel: value, Shape: public}
	restored, err := restoreSecretShape(t.Context(), "KEY", saved, "", sentinelPunctuationToken68)
	if err != nil || !sameSecretShape(shape, restored) {
		t.Fatal("valid pinned grammar did not round trip")
	}
	for i, slot := range saved.Shape.Slots {
		if slot.Class == "ascii" && len(slot.ASCII) > 1 {
			saved.Shape.Slots[i].ASCII += "!"
			if _, err := restoreSecretShape(t.Context(), "KEY", saved, "", sentinelPunctuationToken68); err == nil {
				t.Fatal("persisted alphabet widened beyond the pinned grammar")
			}
			return
		}
	}
	t.Fatal("fixture did not exercise a finite pinned alphabet")
}

func TestBoundRestoreKeepsEitherLegalPinnedPathWhenSampleContainsTwoMarkers(t *testing.T) {
	const prefix, marker = "sk-proj-", "T3BlbkFJ"
	sampled := api.Secret(prefix + strings.Repeat("A", 58) + marker + strings.Repeat("A", 8) + marker + strings.Repeat("B", 58))
	var shapes []sentinelShape
	for _, widths := range [][2]int{{58, 74}, {74, 58}} {
		original := api.Secret(prefix + strings.Repeat("A", widths[0]) + marker + strings.Repeat("B", widths[1]))
		shape, rule, err := compileBoundShape(t.Context(), "KEY", original, "", sentinelPunctuationToken68)
		if err != nil || rule != "openai-api-key" {
			t.Fatal("finite alternative fixture did not compile")
		}
		shapes = append(shapes, shape)
		public, err := publicSecretShape(t.Context(), "KEY", shape, rule)
		if err != nil {
			t.Fatal(err)
		}
		if err := checkSentinelValue(t.Context(), "KEY", sampled, shape); err != nil {
			t.Fatal("double-marker random sample did not match its supplied grammar path")
		}
		saved := api.PublicSecretState{Mode: api.SecretModeBound, Sentinel: sampled, Shape: public}
		restored, err := restoreSecretShape(t.Context(), "KEY", saved, "", sentinelPunctuationToken68)
		if err != nil || !sameSecretShape(shape, restored) {
			t.Fatal("restore chose a different legal path from the sampled public bytes")
		}
	}
	if sameSecretShape(shapes[0], shapes[1]) {
		t.Fatal("fixture did not preserve distinct source marker positions")
	}
}
