package egress

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"regexp/syntax"
	"sync"
)

// The derived data's complete MIT licence and source pin are joined by the
// main change. No upstream entropy or ignore marker exempts network values.
//
//go:embed rules/minted.json
var responseMintedData string

// A deliberate source-pin update changes the data, this digest and its source
// metadata/oracles atomically. A retained source label never authenticates edits.
const responseMintedDataSHA256 = "936f8f8063899ba607ebb2c415af2e46f6df0a5aeec9f4a1c1546f432531f902"

var pinnedResponseRuleSnapshot = sync.OnceValues(func() (*responseRuleSnapshot, error) {
	model, err := parsePinnedResponseRules(responseMintedData)
	if err != nil {
		return nil, err
	}
	return appendPinnedServiceResponseRules(model, responseServiceMintedData)
})

func parsePinnedResponseRules(raw string) (*responseRuleSnapshot, error) {
	digest := sha256.Sum256([]byte(raw))
	if hex.EncodeToString(digest[:]) != responseMintedDataSHA256 {
		return nil, errResponseRules
	}
	var data struct {
		Commit  string `json:"source_commit"`
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
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		return nil, errResponseRules
	}
	if data.Commit != "83d9cd684c87d95d656c1458ef04895a7f1cbd8e" || data.Maximum != responsePatternWindow || len(data.Rules) != 3 {
		return nil, errResponseRules
	}
	rules := make([]responseMintRule, 0, len(data.Rules))
	for _, rule := range data.Rules {
		if rule.Capture != 1 || rule.NetworkEntropy != "do-not-suppress-credential-shaped-values" {
			return nil, errResponseRules
		}
		ast, err := syntax.Parse(rule.Expression, syntax.Perl)
		if err != nil {
			return nil, errResponseRules
		}
		capture := responseCaptureNode(ast)
		if capture == nil {
			return nil, errResponseRules
		}
		secret, ok := responseASCIIWidth(capture)
		full, fullOK := responseASCIIWidth(ast)
		if !ok || !fullOK || secret != rule.SecretMaximum || full != rule.MatchMaximum || full+1 != rule.WindowMaximum || rule.WindowMaximum > responsePatternWindow {
			return nil, errResponseRules
		}
		rules = append(rules, responseMintRule{ID: rule.ID, Expression: rule.Expression})
	}
	return newResponseRuleSnapshot(rules)
}
