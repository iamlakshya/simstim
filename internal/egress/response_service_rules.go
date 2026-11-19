package egress

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"regexp/syntax"
)

// This finite bound is PyPI's pinned1020-byte maximum plus one byte of left
// context. The original model/custom constructor retains its170-byte bound.
const responseServicePatternWindow = 1021

//go:embed rules/minted-services.json
var responseServiceMintedData string

const responseServiceMintedDataSHA256 = "b669ad6eca92b9d19f6ef59557471f2f0c75e479d8ba81b81f223e2a0066711d"

// One immutable snapshot and scanner handle all families. The original model
// data is independently checked with its exact three-rule/170-byte contract.
func appendPinnedServiceResponseRules(model *responseRuleSnapshot, raw string) (*responseRuleSnapshot, error) {
	digest := sha256.Sum256([]byte(raw))
	if model == nil || model.window != responsePatternWindow || len(model.rules) != 3 || hex.EncodeToString(digest[:]) != responseServiceMintedDataSHA256 {
		return nil, errResponseRules
	}
	var data struct {
		Commit  string `json:"source_commit"`
		Source  string `json:"source_sha256"`
		Maximum int    `json:"maximum_scan_window_bytes"`
		Rules   []struct {
			ID             string `json:"id"`
			Expression     string `json:"regex"`
			Capture        int    `json:"capture_group"`
			SecretMaximum  int    `json:"max_secret_bytes"`
			MatchMaximum   int    `json:"max_match_bytes"`
			WindowMaximum  int    `json:"max_scan_window_bytes"`
			NetworkEntropy string `json:"network_entropy_policy"`
		} `json:"rules"`
	}
	if err := json.Unmarshal([]byte(raw), &data); err != nil || data.Commit != "83d9cd684c87d95d656c1458ef04895a7f1cbd8e" || data.Source != "e163e53b9e7e8a8511e77271e2b323ed057759542a6d988258afe3a1fa329caf" || data.Maximum != responseServicePatternWindow || len(data.Rules) != 21 {
		return nil, errResponseRules
	}
	rules := make([]responseMintRule, 0, len(model.rules)+len(data.Rules))
	for _, rule := range model.rules {
		rules = append(rules, responseMintRule{ID: rule.id, Expression: rule.re.String()})
	}
	maximum := responsePatternWindow
	for _, rule := range data.Rules {
		ast, err := syntax.Parse(rule.Expression, syntax.Perl)
		if err != nil || rule.Capture != 1 || rule.NetworkEntropy != "do-not-suppress-credential-shaped-values" {
			return nil, errResponseRules
		}
		capture := responseCaptureNode(ast)
		if capture == nil {
			return nil, errResponseRules
		}
		secret, ok := responseASCIIWidthWithinWindow(capture, responseServicePatternWindow)
		full, fullOK := responseASCIIWidthWithinWindow(ast, responseServicePatternWindow)
		if !ok || !fullOK || secret != rule.SecretMaximum || full != rule.MatchMaximum || full+1 != rule.WindowMaximum || rule.WindowMaximum > responseServicePatternWindow {
			return nil, errResponseRules
		}
		maximum = max(maximum, rule.WindowMaximum)
		rules = append(rules, responseMintRule{ID: rule.ID, Expression: rule.Expression})
	}
	if maximum != responseServicePatternWindow {
		return nil, errResponseRules
	}
	return newResponseRuleSnapshotWithinWindow(rules, maximum)
}
