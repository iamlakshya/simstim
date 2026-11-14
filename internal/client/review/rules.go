package review

import (
	"embed"
	"fmt"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
)

//go:embed flags.toml
var ruleData embed.FS

// PathRule flags paths that can change code execution outside the sandbox.
type PathRule struct {
	Category string   `toml:"category"`
	Patterns []string `toml:"patterns"`
}

// ContentRule selects one bounded static configuration check.
type ContentRule struct {
	Path     string `toml:"path"`
	Checker  string `toml:"checker"`
	Category string `toml:"category"`
}

// Rules is the data-defined review policy. It contains no executable hooks.
type Rules struct {
	Paths   []PathRule    `toml:"path"`
	Content []ContentRule `toml:"content"`
}

// DefaultRules loads the embedded review paths and content selectors.
func DefaultRules() (Rules, error) {
	data, err := ruleData.ReadFile("flags.toml")
	if err != nil {
		return Rules{}, fmt.Errorf("read review rules: %w", err)
	}
	var rules Rules
	if _, err = toml.Decode(string(data), &rules); err != nil {
		return Rules{}, fmt.Errorf("decode review rules: %w", err)
	}
	return rules, rules.Validate()
}

// Validate refuses unknown content checkers and invalid path patterns.
func (r Rules) Validate() error {
	for _, rule := range r.Paths {
		if rule.Category == "" || len(rule.Patterns) == 0 {
			return fmt.Errorf("review path rule has no category or patterns")
		}
		for _, pattern := range rule.Patterns {
			if _, err := glob(pattern); err != nil {
				return err
			}
		}
	}
	for _, rule := range r.Content {
		if unsafePath(rule.Path) || rule.Category == "" {
			return fmt.Errorf("invalid review content path")
		}
		switch rule.Checker {
		case "scripts", "python-build", "attributes-filter":
		default:
			return fmt.Errorf("unknown static review checker %q", rule.Checker)
		}
	}
	return nil
}

func matchesAny(patterns []string, name string) bool {
	for _, pattern := range patterns {
		if matches(pattern, name) {
			return true
		}
	}
	return false
}

func matches(pattern, name string) bool {
	if strings.HasSuffix(pattern, "/") {
		pattern += "**"
	}
	expression, err := glob(pattern)
	return err == nil && expression.MatchString(strings.ToLower(name))
}

func glob(pattern string) (*regexp.Regexp, error) {
	pattern = strings.ToLower(pattern)
	if pattern == "" || strings.HasPrefix(pattern, "/") || strings.Contains(pattern, "..") || strings.ContainsRune(pattern, '\\') {
		return nil, fmt.Errorf("invalid review pattern %q", pattern)
	}
	var out strings.Builder
	out.WriteString("^")
	for at := 0; at < len(pattern); at++ {
		switch pattern[at] {
		case '*':
			if at+1 < len(pattern) && pattern[at+1] == '*' {
				out.WriteString(".*")
				at++
			} else {
				out.WriteString("[^/]*")
			}
		case '?':
			out.WriteString("[^/]")
		default:
			out.WriteString(regexp.QuoteMeta(pattern[at : at+1]))
		}
	}
	out.WriteString("$")
	expression, err := regexp.Compile(out.String())
	if err != nil {
		return nil, fmt.Errorf("compile review pattern: %w", err)
	}
	return expression, nil
}
