package egress

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"golang.org/x/net/idna"
)

func classifierTestALabel(t *testing.T, unicodeHost string) string {
	t.Helper()
	host, err := idna.Lookup.ToASCII(unicodeHost)
	if err != nil {
		t.Fatal("authored IDNA fixture is invalid", err)
	}
	return host
}

func TestClassifierConfusableCompleteDataAndCanonicalSkeletonVersion(t *testing.T) {
	prototypes, err := loadClassifierConfusables()
	if err != nil {
		t.Fatal(err)
	}
	keys := make([]rune, 0, len(prototypes))
	for r := range prototypes {
		keys = append(keys, r)
	}
	slices.Sort(keys)
	digest := sha256.New()
	for _, r := range keys {
		_, _ = fmt.Fprintf(digest, "%06X;%s\n", r, prototypes[r])
	}
	if len(keys) != 6311 || hex.EncodeToString(digest.Sum(nil)) != classifierConfusableDigest {
		t.Fatal("complete pinned Unicode15 source data identity changed")
	}
	raw, err := decodeClassifierConfusables(classifierConfusableCompressed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseClassifierConfusables(raw + "\n"); err == nil {
		t.Fatal("edited original prototype bytes retained their pinned identity")
	}
	for _, pair := range [][2]string{
		{"paypal.com", "раураl.com"},
		{"google.com", "goog1e.com"},
		{"modern.example", "rnodern.example"},
	} {
		left, err := classifierSkeleton(classifierTestALabel(t, pair[0]))
		if err != nil {
			t.Fatal(err)
		}
		right, err := classifierSkeleton(classifierTestALabel(t, pair[1]))
		if err != nil || left != right {
			t.Fatal("genuine pinned skeleton lost a multi-scalar or non-ASCII prototype", err)
		}
	}
	first, err := classifierSkeleton(classifierTestALabel(t, "café.example"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := classifierSkeleton(classifierTestALabel(t, "cafe\u0301.example"))
	if err != nil || first != second {
		t.Fatal("canonical-equivalent names lost NFD/IDNA equivalence", err)
	}
	if _, err := classifierSkeleton("xn--a.example"); err == nil {
		t.Fatal("invalid A-label reached skeleton comparison")
	}
}

func TestClassifierConfusableNamesRecheckActualAuthorityWithoutUsingLeases(t *testing.T) {
	now := time.Now()
	spoof := classifierTestALabel(t, "раураl.com")
	for _, state := range []string{"current", "expired", "deny", "wrong-method", "spent"} {
		t.Run(state, func(t *testing.T) {
			rule := api.Rule{Layer: api.LayerSession, Target: "paypal.com", Allow: true, Once: true, GrantID: "authored-comparison"}
			policy := api.Policy{Preset: api.PresetLocked, Unknown: api.UnknownClassify, Rules: []api.Rule{rule}}
			switch state {
			case "expired":
				policy.Rules[0].Until = now
			case "deny":
				policy.Rules = append(policy.Rules, api.Rule{Layer: api.LayerSession, Target: "paypal.com"})
			case "wrong-method":
				policy.Rules[0].Methods = new([]string{"GET"})
			}
			engine, err := NewEngine("shell", policy)
			if err != nil {
				t.Fatal(err)
			}
			engine.now = func() time.Time { return now }
			if state == "spent" {
				permit := engine.NewPermit()
				if !engine.DecideWithPermit(Request{Host: "paypal.com", Port: 443}, permit).Allow {
					t.Fatal("owned comparison lease fixture was not available")
				}
				permit.Commit()
				permit.Close()
			}
			before := snapshotDNSLedger(engine)
			got := engine.Decide(Request{Host: spoof, Port: 443, Method: "POST", Path: "/authored"})
			want := "classify:unsure"
			if state == "current" {
				want = "classify:confusable"
			}
			if got.Allow || got.Rule != want || !reflect.DeepEqual(before, snapshotDNSLedger(engine)) {
				t.Fatal("confusable comparison inferred authority or changed the actual lease ledger")
			}
		})
	}
}

func TestClassifierConfusableWildcardAndLivePolicyIndexUseTheSameMatcher(t *testing.T) {
	spoof := "authored." + classifierTestALabel(t, "раураl.com")
	policy := api.Policy{Preset: api.PresetLocked, Unknown: api.UnknownClassify, Rules: []api.Rule{{Layer: api.LayerSession, Target: "*.paypal.com", Allow: true}}}
	engine, err := NewEngine("shell", policy)
	if err != nil {
		t.Fatal(err)
	}
	if got := engine.Decide(Request{Host: spoof, Port: 443}); got.Allow || got.Rule != "classify:confusable" {
		t.Fatal("wildcard comparison missed its actual authorised replacement suffix")
	}
	for _, host := range []string{"authored.paypal.com", "other.example"} {
		got := engine.Decide(Request{Host: host, Port: 443})
		if strings.HasPrefix(got.Rule, "classify:confusable") || host == "authored.paypal.com" && !got.Allow {
			t.Fatal("same canonical host or unrelated domain acquired a confusable refusal")
		}
	}
	replaced, err := NewEngine("shell", api.Policy{Preset: api.PresetLocked, Unknown: api.UnknownClassify})
	if err != nil {
		t.Fatal(err)
	}
	engine.ledger.syncPolicy(replaced)
	if got := replaced.Decide(Request{Host: spoof, Port: 443}); got.Rule != "classify:unsure" {
		t.Fatal("a previous engine's comparison index survived actual policy replacement")
	}
}
