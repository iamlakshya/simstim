package review

import (
	"bytes"
	"strings"
	"unicode/utf8"
)

// ScanCopy applies the SAME immutable path/content rules to a complete copied
// file. Every destination suffix is checked because a copied folder may itself
// be a project root. Content selectors flag the filename before bytes arrive.
// All supplied bytes count as added; callers keep FileLimit and module parsing.
func (s *Scanner) ScanCopy(change Change) ([]Finding, error) {
	change.Binary = bytes.ContainsRune(change.After, 0) || !utf8.Valid(change.After)
	var out []Finding
	parts := strings.Split(change.Path, "/")
	for index := range parts {
		selected := change
		selected.Path = strings.Join(parts[index:], "/")
		findings, err := s.Scan(selected)
		if err != nil {
			return nil, err
		}
		for _, rule := range s.rules.Content {
			if strings.EqualFold(rule.Path, selected.Path) {
				findings = append(findings, Finding{Path: selected.Path, Category: rule.Category, Reason: "this file can change a command that runs outside the sandbox"})
			}
		}
		for _, finding := range findings {
			finding.Path = change.Path
			out = append(out, finding)
		}
	}
	return sorted(out), nil
}
