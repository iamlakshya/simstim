// Static data constraints keep the source pin and finite byte budget explicit.
package egress

import (
	"encoding/json"
	"os"
	"regexp"
	"regexp/syntax"
	"strings"
	"testing"
)

type mintedRuleDataOracle struct {
	ID             string  `json:"id"`
	Regex          string  `json:"regex"`
	Capture        int     `json:"capture_group"`
	Entropy        float64 `json:"upstream_entropy"`
	NetworkEntropy string  `json:"network_entropy_policy"`
	SecretMax      int     `json:"max_secret_bytes"`
	MatchMax       int     `json:"max_match_bytes"`
	LeftContext    int     `json:"left_context_bytes"`
	WindowMax      int     `json:"max_scan_window_bytes"`
}

func loadMintedDataOracle(t *testing.T) map[string]mintedRuleDataOracle {
	t.Helper()
	raw, err := os.ReadFile("rules/minted.json")
	if err != nil {
		t.Fatal(err)
	}
	var data struct {
		Commit  string                 `json:"source_commit"`
		Maximum int                    `json:"maximum_scan_window_bytes"`
		Rules   []mintedRuleDataOracle `json:"rules"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	if data.Commit != "83d9cd684c87d95d656c1458ef04895a7f1cbd8e" || data.Maximum != 170 || len(data.Rules) != 3 {
		t.Fatal("minted rule source identity or finite subset changed")
	}
	result := make(map[string]mintedRuleDataOracle)
	for _, rule := range data.Rules {
		if _, exists := result[rule.ID]; exists {
			t.Fatal("duplicate minted source rule")
		}
		result[rule.ID] = rule
	}
	return result
}

// Finite ASCII source analysis. No token detector or streaming logic lives here.
func mintedDataMaximum(t *testing.T, node *syntax.Regexp) int {
	t.Helper()
	switch node.Op {
	case syntax.OpEmptyMatch, syntax.OpBeginLine, syntax.OpEndLine, syntax.OpBeginText, syntax.OpEndText, syntax.OpWordBoundary, syntax.OpNoWordBoundary:
		return 0
	case syntax.OpLiteral:
		for _, r := range node.Rune {
			if r > 127 {
				t.Fatal("non-ASCII literal needs a new byte-bound analysis")
			}
		}
		return len(node.Rune)
	case syntax.OpCharClass:
		for _, r := range node.Rune {
			if r > 127 {
				t.Fatal("non-ASCII class needs a new byte-bound analysis")
			}
		}
		return 1
	case syntax.OpCapture, syntax.OpQuest:
		return mintedDataMaximum(t, node.Sub[0])
	case syntax.OpConcat:
		total := 0
		for _, sub := range node.Sub {
			total += mintedDataMaximum(t, sub)
			if total > 4096 {
				t.Fatal("minted source width exceeds the narrow finite contract")
			}
		}
		return total
	case syntax.OpAlternate:
		maximum := 0
		for _, sub := range node.Sub {
			if n := mintedDataMaximum(t, sub); n > maximum {
				maximum = n
			}
		}
		return maximum
	case syntax.OpRepeat:
		if node.Max < 0 || node.Max > 4096 {
			t.Fatal("unbounded repetition cannot share this finite streaming carry")
		}
		n := node.Max * mintedDataMaximum(t, node.Sub[0])
		if n > 4096 {
			t.Fatal("minted repeat exceeds the narrow finite contract")
		}
		return n
	default:
		t.Fatalf("unsupported or unbounded source regex operation %s", node.Op)
		return 0
	}
}

func mintedDataCapture(node *syntax.Regexp, number int) *syntax.Regexp {
	if node.Op == syntax.OpCapture && node.Cap == number {
		return node.Sub[0]
	}
	for _, sub := range node.Sub {
		if found := mintedDataCapture(sub, number); found != nil {
			return found
		}
	}
	return nil
}

func TestMintedSourcePatternsHaveExactFiniteByteBounds(t *testing.T) {
	for id, rule := range loadMintedDataOracle(t) {
		t.Run(id, func(t *testing.T) {
			ast, err := syntax.Parse(rule.Regex, syntax.Perl)
			if err != nil {
				t.Fatal(err)
			}
			capture := mintedDataCapture(ast, rule.Capture)
			if rule.Capture != 1 || capture == nil {
				t.Fatal("the upstream first credential capture was lost")
			}
			if got := mintedDataMaximum(t, ast); got != rule.MatchMax {
				t.Fatalf("full match bound=%d declared=%d", got, rule.MatchMax)
			}
			if got := mintedDataMaximum(t, capture); got != rule.SecretMax {
				t.Fatalf("secret bound=%d declared=%d", got, rule.SecretMax)
			}
			if rule.LeftContext != 1 || rule.WindowMax != rule.MatchMax+1 || rule.WindowMax > 170 {
				t.Fatal("ASCII boundary or carry window metadata changed")
			}
			if rule.NetworkEntropy != "do-not-suppress-credential-shaped-values" {
				t.Fatal("network entropy suppression changed without the main decision")
			}
			if id == "openai-api-key" && rule.Entropy != 3 {
				t.Fatal("upstream OpenAI entropy provenance was lost")
			}
		})
	}
}

func TestMintedSourceCapturesExcludeResponseDelimiters(t *testing.T) {
	rules := loadMintedDataOracle(t)
	api := "sk-ant-api03-" + strings.Repeat("A", 93) + "AA"
	admin := "sk-ant-admin01-" + strings.Repeat("B", 93) + "AA"
	legacy := "sk-" + strings.Repeat("A", 20) + "T3BlbkFJ" + strings.Repeat("B", 20)
	longest := "sk-svcacct-" + strings.Repeat("A", 74) + "T3BlbkFJ" + strings.Repeat("B", 74)
	for _, tc := range []struct{ id, token string }{
		{"anthropic-api-key", api}, {"anthropic-admin-api-key", admin}, {"openai-api-key", legacy}, {"openai-api-key", longest},
	} {
		rule := rules[tc.id]
		pattern, err := regexp.Compile(rule.Regex)
		if err != nil {
			t.Fatal(err)
		}
		for _, delimiter := range []string{"", "\"", "'", "\n", ";", "\\n", "\\r"} {
			input := "before=" + tc.token + delimiter
			indexes := pattern.FindStringSubmatchIndex(input)
			if len(indexes) != 4 || indexes[2] < 0 || input[indexes[2]:indexes[3]] != tc.token {
				t.Fatalf("%s credential capture includes context or is absent", tc.id)
			}
			// A replacement uses indexes 2 and 3 only. The trailing delimiter
			// remains present even when the full upstream match consumes it.
			replaced := input[:indexes[2]] + strings.Repeat("S", len(tc.token)) + input[indexes[3]:]
			if !strings.HasSuffix(replaced, delimiter) || len(replaced) != len(input) {
				t.Fatal("candidate span changes response delimiter bytes or length")
			}
		}
		if pattern.MatchString("x" + tc.token + "\n") {
			t.Fatal("left ASCII word context was ignored")
		}
		if pattern.MatchString(tc.token + "A\n") {
			t.Fatal("an extended credential was mislabelled as a shorter candidate")
		}
		if pattern.MatchString(tc.token + ":") {
			t.Fatal("the selected upstream terminator set was silently widened")
		}
	}
}

func TestMintedSourceRetainsEveryBoundedOpenAIBranch(t *testing.T) {
	rule := loadMintedDataOracle(t)["openai-api-key"]
	pattern, err := regexp.Compile(rule.Regex)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"proj", "svcacct", "admin"} {
		for _, left := range []int{58, 74} {
			for _, right := range []int{58, 74} {
				token := "sk-" + kind + "-" + strings.Repeat("A", left) + "T3BlbkFJ" + strings.Repeat("B", right)
				match := pattern.FindStringSubmatch(token + ";")
				if len(match) != 2 || match[1] != token || len(token) > rule.SecretMax {
					t.Fatal("a source-backed OpenAI shape was omitted or its fixed marker lost")
				}
			}
		}
	}
}
