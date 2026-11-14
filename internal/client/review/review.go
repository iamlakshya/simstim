// Package review inspects changed Git objects as data before a branch moves.
// It never checks out files, runs content, opens paths or prints secret values.
package review

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// FileLimit is the largest changed file whose content a review may read.
const FileLimit int64 = 1 << 20

// Change is one exact changed object supplied by the Git reader. A supplier
// must obtain Size first and leave content unset when it exceeds FileLimit.
type Change struct {
	Path          string
	Size          int64
	Before        []byte
	After         []byte
	Added         []byte
	Deleted       bool
	Binary        bool
	Mode          string
	BeforeMode    string
	ModuleValues  [][]byte
	ModulesParsed bool
}

// Finding names a risk without copying the risky or secret content.
type Finding struct {
	Path     string `json:"path"`
	Category string `json:"category"`
	Reason   string `json:"reason"`
	Refused  bool   `json:"refused,omitempty"`
}

// Options supplies project paths from agent DATA and ephemeral secret values.
type Options struct {
	ProjectFiles []string
	Secrets      [][]byte
	Brand        string
}

// Scanner owns immutable path rules and in-memory values for one review.
type Scanner struct {
	rules    Rules
	projects []string
	secrets  [][]byte
	brand    string
}

// New constructs a scanner from checked static rules and an ephemeral plan.
func New(rules Rules, options Options) (*Scanner, error) {
	if err := rules.Validate(); err != nil {
		return nil, err
	}
	for _, pattern := range options.ProjectFiles {
		if _, err := glob(strings.TrimSuffix(pattern, "/") + "/**"); err != nil {
			return nil, err
		}
	}
	scanner := &Scanner{rules: rules, projects: append([]string(nil), options.ProjectFiles...), brand: options.Brand}
	for _, value := range options.Secrets {
		if len(value) > 0 {
			scanner.secrets = append(scanner.secrets, bytes.Clone(value))
		}
	}
	return scanner, nil
}

// Clear overwrites this scanner's copies of secret values after use.
func (s *Scanner) Clear() {
	for _, value := range s.secrets {
		clear(value)
	}
	s.secrets = nil
}

// Scan returns deterministic path and content findings for one changed file.
func (s *Scanner) Scan(change Change) ([]Finding, error) {
	var findings []Finding
	add := func(category, reason string, refused bool) {
		findings = append(findings, Finding{Path: change.Path, Category: category, Reason: reason, Refused: refused})
	}
	if !ValidMode(change.Mode) || !ValidMode(change.BeforeMode) {
		add("path", "the changed entry has an unsupported Git mode", true)
	}
	if unsafePath(change.Path) {
		add("path", "the tree path is unsafe or cannot be displayed unambiguously", true)
	}
	if portablePathRisk(change.Path) {
		add("portable checkout", "this Git name is ambiguous on another filesystem; inspect it before any manual checkout", false)
	}
	for _, rule := range s.rules.Paths {
		if matchesAny(rule.Patterns, change.Path) {
			add(rule.Category, "this path can change code that runs outside the sandbox", false)
		}
	}
	if matchesAny(s.projects, change.Path) {
		add("agent project files", "the agent reads this path as project settings", false)
	}
	if change.Mode == "120000" {
		add("path", "the changed entry is a symbolic link", false)
	}
	if change.Mode == "160000" {
		add("git", "the changed entry is a submodule", false)
	}
	if strings.EqualFold(change.Path, ".gitmodules") && (change.Mode == "120000" || change.Mode == "160000") {
		add("git", "the Git module file is not a regular file", true)
		return sorted(findings), nil
	}
	if s.brand != "" && matches("."+s.brand+"/**", change.Path) {
		add("this tool", "this path can change session setup or policy", false)
	}
	if change.Size > FileLimit {
		if strings.EqualFold(change.Path, ".gitmodules") {
			add("git", "the oversized Git module file cannot be checked without breaking the content-read bound", true)
		}
		add("large", fmt.Sprintf("the changed file is %d bytes, so its content was not read", change.Size), false)
		return sorted(findings), nil
	}

	for _, rule := range s.rules.Content {
		if !strings.EqualFold(rule.Path, change.Path) {
			continue
		}
		changed, err := contentChanged(rule.Checker, change)
		if err != nil {
			add(rule.Category, "the changed configuration could not be parsed safely", false)
			continue
		}
		if changed {
			add(rule.Category, "the change can alter a build, install or Git filter command", false)
		}
	}
	if strings.EqualFold(change.Path, ".gitmodules") {
		if !change.ModulesParsed && len(change.After) > 0 {
			return nil, fmt.Errorf("Git module values were not parsed by the safe Git reader")
		}
		for _, value := range change.ModuleValues {
			if bytes.ContainsRune(value, '\r') {
				add("git", "a Git module value contains a carriage return", true)
				break
			}
		}
	}
	if !change.Binary && hidden(change.Added) {
		add("hidden text", "added lines contain hidden formatting, bidirectional or tag characters", false)
	}
	for _, value := range s.secrets {
		if bytes.Contains(change.Added, value) {
			add("secrets", "an added line contains a session secret or sentinel", false)
			break
		}
	}
	return sorted(findings), nil
}

// ValidMode recognises only ordinary tree file, link, gitlink and absent modes.
// Readers check both sides before launching any content operation.
func ValidMode(mode string) bool {
	switch mode {
	case "", "000000", "100644", "100755", "120000", "160000":
		return true
	default:
		return false
	}
}

func portablePathRisk(name string) bool {
	for _, part := range strings.Split(name, "/") {
		if strings.ContainsRune(part, ':') || strings.TrimRight(part, ". ") != part {
			return true
		}
		lower := strings.ToLower(part)
		if suffix, ok := strings.CutPrefix(lower, ".git~"); ok && suffix != "" && strings.Trim(suffix, "0123456789") == "" {
			return true
		}
		stem, _, _ := strings.Cut(lower, ".")
		if stem == "con" || stem == "prn" || stem == "aux" || stem == "nul" {
			return true
		}
		if len(stem) == 4 && (strings.HasPrefix(stem, "com") || strings.HasPrefix(stem, "lpt")) && stem[3] >= '1' && stem[3] <= '9' {
			return true
		}
	}
	return false
}

func sorted(findings []Finding) []Finding {
	sort.Slice(findings, func(i, j int) bool {
		a, b := findings[i], findings[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.Category != b.Category {
			return a.Category < b.Category
		}
		return a.Reason < b.Reason
	})
	out := findings[:0]
	for _, f := range findings {
		if len(out) == 0 || out[len(out)-1] != f {
			out = append(out, f)
		}
	}
	return out
}

func unsafePath(name string) bool {
	if name == "" || !utf8.ValidString(name) || strings.HasPrefix(name, "/") || strings.ContainsRune(name, '\\') {
		return true
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." || strings.EqualFold(part, ".git") {
			return true
		}
	}
	for _, r := range name {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return true
		}
	}
	return false
}

func hidden(data []byte) bool {
	for len(data) > 0 {
		r, size := utf8.DecodeRune(data)
		if r == utf8.RuneError && size == 1 {
			return true
		}
		if unicode.Is(unicode.Cf, r) || r >= 0xe0000 && r <= 0xe007f || r == 0x034f {
			return true
		}
		data = data[size:]
	}
	return false
}
