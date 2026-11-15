package review

import "testing"

func TestCopyReviewFlagsDestinationSuffixAndContentSelectorBeforeData(t *testing.T) {
	rules, err := DefaultRules()
	if err != nil {
		t.Fatal(err)
	}
	scanner, err := New(rules, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer scanner.Clear()
	for _, path := range []string{"out/.envrc", "out/project/package.json", "out/.github/workflows/ci.yml"} {
		findings, err := scanner.ScanCopy(Change{Path: path, Size: 1, Mode: "100644"})
		if err != nil || len(findings) == 0 {
			t.Fatalf("unreviewed copy name %q: %v %v", path, findings, err)
		}
	}
}

func TestCopyReviewChecksAllReceivedLinesForSecrets(t *testing.T) {
	rules, err := DefaultRules()
	if err != nil {
		t.Fatal(err)
	}
	scanner, err := New(rules, Options{Secrets: [][]byte{[]byte("owned-review-value")}})
	if err != nil {
		t.Fatal(err)
	}
	defer scanner.Clear()
	data := []byte("first\nowned-review-value\n")
	findings, err := scanner.ScanCopy(Change{Path: "notes.txt", Size: int64(len(data)), Mode: "100644", After: data, Added: data})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, finding := range findings {
		found = found || finding.Category == "secrets"
	}
	if !found {
		t.Fatal("copy review skipped a received line")
	}
}
