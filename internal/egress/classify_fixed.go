package egress

import (
	_ "embed"
	"errors"
	"math"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"
)

//go:embed rules/classify.toml
var fixedClassifierData string

type classifierHostDeny struct {
	Host   string `toml:"host"`
	Source string `toml:"source"`
	rule   compiledRule
}

type fixedClassifierRules struct {
	Denies   []classifierHostDeny `toml:"denies"`
	Barriers []serviceBarrier     `toml:"barriers"`
}

var loadFixedClassifier = sync.OnceValues(func() (fixedClassifierRules, error) {
	var data fixedClassifierRules
	metadata, err := toml.Decode(fixedClassifierData, &data)
	if err != nil || len(metadata.Undecoded()) != 0 || len(data.Denies) == 0 || len(data.Barriers) == 0 {
		return data, errors.New("invalid fixed classifier data. Try: update matching helpers")
	}
	seen := make(map[string]bool, len(data.Denies))
	for i := range data.Denies {
		deny := &data.Denies[i]
		deny.rule, err = compileServiceHost(deny.Host)
		if err != nil || !strings.HasPrefix(deny.Source, "https://") || seen[deny.Host] {
			return data, errors.New("invalid fixed classifier host or source. Try: update matching helpers")
		}
		seen[deny.Host] = true
	}
	return data, nil
})

// This fixed stage has no authority to call a judge, resolve a name, change a
// rule or consume a permit. The caller has already exhausted actual policy.
func fixedClassifierDecision(host string, numeric bool) Decision {
	if numeric {
		return Decision{Rule: "classify:ip"}
	}
	data, err := loadFixedClassifier()
	if err != nil {
		return Decision{Rule: "classify:unavailable"}
	}
	for _, deny := range data.Denies {
		if matchesServiceHost(deny.rule, host) || deny.rule.wildcard && deny.rule.host == host {
			return Decision{Rule: "classify:fixed"}
		}
	}
	for _, label := range strings.Split(host, ".") {
		if randomClassifierLabel(label) {
			return Decision{Rule: "classify:random"}
		}
	}
	// The actual hold/judge caller must handle unsure. This stage supplies no
	// substitute allow or completed ask implementation.
	return Decision{Rule: "classify:unsure", unmatched: true}
}

// These are starting values from spec7.8, pending the P2.12 evaluation set.
// Entropy is measured over the complete canonical label, never a sample.
func randomClassifierLabel(label string) bool {
	if len(label) < 40 {
		return false
	}
	var counts [256]int
	for i := range len(label) {
		counts[label[i]]++
	}
	entropy := 0.0
	for _, count := range counts {
		if count != 0 {
			fraction := float64(count) / float64(len(label))
			entropy -= fraction * math.Log2(fraction)
		}
	}
	return entropy >= 3.5
}
