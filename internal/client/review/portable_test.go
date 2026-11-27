package review

import "testing"

func TestPortableNamesFlagWithoutChangingRawGitPathOrHardRefusing(t *testing.T) {
	rules, err := DefaultRules()
	if err != nil {
		t.Fatal(err)
	}
	scanner, err := New(rules, Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"source:stream", "nested/name.", "nested/name ", ".git~1/file", "CON", "nested/NUL.txt", "lpt9"} {
		findings, err := scanner.Scan(Change{Path: path, Mode: "100644"})
		if err != nil {
			t.Fatal(err)
		}
		flagged := false
		for _, finding := range findings {
			if finding.Path != path || finding.Refused {
				t.Fatalf("valid Linux name was normalised or hard refused: %+v", finding)
			}
			flagged = flagged || finding.Category == "portable checkout"
		}
		if !flagged {
			t.Fatal("portable checkout risk was silent", path)
		}
	}
	for _, path := range []string{"console.txt", "lpt10.txt", "source/name.txt", ".git~notes/file"} {
		if portablePathRisk(path) {
			t.Fatal("ordinary positive control was treated as a reserved name", path)
		}
	}
}
