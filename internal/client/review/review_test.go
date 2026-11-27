package review

import (
	"encoding/json"
	"strings"
	"testing"
)

func scannerFixture(t *testing.T, options Options) *Scanner {
	t.Helper()
	rules, err := DefaultRules()
	if err != nil {
		t.Fatal(err)
	}
	scanner, err := New(rules, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(scanner.Clear)
	return scanner
}

func hasCategory(findings []Finding, category string) bool {
	for _, finding := range findings {
		if finding.Category == category {
			return true
		}
	}
	return false
}

func TestReviewFlagsExecutablePathsFromData(t *testing.T) {
	scanner := scannerFixture(t, Options{ProjectFiles: []string{".new-agent/", "GUIDE.md"}, Brand: "fixture"})
	for _, test := range []struct{ path, category string }{{".github/workflows/ci.yml", "CI"}, {".circleci/config.yml", "CI"}, {"Makefile", "build and install"}, {".envrc", "editor and shell"}, {".cargo/config.toml", "registry config"}, {".githooks/pre-commit", "git"}, {".new-agent/settings.json", "agent project files"}, {"GUIDE.md", "agent project files"}, {".fixture/setup", "this tool"}} {
		findings, err := scanner.Scan(Change{Path: test.path})
		if err != nil || !hasCategory(findings, test.category) {
			t.Fatalf("%s: %+v %v", test.path, findings, err)
		}
	}
	findings, err := scanner.Scan(Change{Path: "src/library.go", After: []byte("package library")})
	if err != nil || len(findings) != 0 {
		t.Fatalf("ordinary code was flagged: %+v %v", findings, err)
	}
}

func TestPackageScriptsAndPythonBuildSettingsNeedReview(t *testing.T) {
	scanner := scannerFixture(t, Options{})
	for _, test := range []struct {
		name, path, before, after string
		flag                      bool
	}{
		{"scripts", "package.json", `{"scripts":{"postinstall":"old"}}`, `{"scripts":{"postinstall":"new"}}`, true},
		{"version only", "package.json", `{"version":"1","scripts":{"test":"check"}}`, `{"version":"2","scripts":{"test":"check"}}`, false},
		{"script deletion", "package.json", `{"scripts":{"install":"run"}}`, "", true},
		{"malformed", "package.json", `{}`, `{} trailing`, true},
		{"backend", "pyproject.toml", "[build-system]\nbuild-backend='one'", "[build-system]\nbuild-backend='two'", true},
		{"entry point", "pyproject.toml", "[project.scripts]\nrun='one'", "[project.scripts]\nrun='two'", true},
		{"python version only", "pyproject.toml", "[project]\nversion='1'", "[project]\nversion='2'", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			findings, err := scanner.Scan(Change{Path: test.path, Before: []byte(test.before), After: []byte(test.after)})
			if err != nil || hasCategory(findings, "build and install") != test.flag {
				t.Fatalf("%+v %v", findings, err)
			}
		})
	}
}

func TestHiddenTextAndSecretsNeverEnterSummary(t *testing.T) {
	secret := "fixture-secret-value"
	scanner := scannerFixture(t, Options{Secrets: [][]byte{[]byte(secret)}})
	for _, hidden := range []string{"\u202e", "\u200b", "\u2060", "\ufeff", "\U000e0020", "\u034f"} {
		findings, err := scanner.Scan(Change{Path: "source.txt", Added: []byte("added " + hidden + secret)})
		if err != nil || !hasCategory(findings, "hidden text") || !hasCategory(findings, "secrets") {
			t.Fatalf("%+v %v", findings, err)
		}
		summary, err := json.Marshal(findings)
		if err != nil || strings.Contains(string(summary), secret) || strings.Contains(string(summary), hidden) {
			t.Fatal("risky content entered summary", string(summary), err)
		}
	}
	scanner.Clear()
	if len(scanner.secrets) != 0 {
		t.Fatal("scanner retained secret values after clear")
	}
}

func TestLargeObjectIsFlaggedWithoutInspectingContent(t *testing.T) {
	scanner := scannerFixture(t, Options{Secrets: [][]byte{[]byte("must-not-be-read")}})
	findings, err := scanner.Scan(Change{Path: "large.bin", Size: FileLimit + 1, Added: []byte("\u202emust-not-be-read"), After: []byte("must-not-be-read")})
	if err != nil || !hasCategory(findings, "large") || hasCategory(findings, "hidden text") || hasCategory(findings, "secrets") {
		t.Fatalf("large content was inspected: %+v %v", findings, err)
	}
}

func TestGitModuleDecodedCarriageReturnIsAHardRefusal(t *testing.T) {
	scanner := scannerFixture(t, Options{})
	for _, test := range []struct {
		value   string
		refused bool
	}{{"safe/path", false}, {"path\rhook", true}} {
		findings, err := scanner.Scan(Change{Path: ".gitmodules", After: []byte("[submodule]\r\npath = value\r\n"), ModulesParsed: true, ModuleValues: [][]byte{[]byte(test.value)}})
		if err != nil {
			t.Fatal(err)
		}
		refused := false
		for _, finding := range findings {
			refused = refused || finding.Refused
		}
		if refused != test.refused {
			t.Fatalf("decoded CR result: %+v", findings)
		}
	}
	if _, err := scanner.Scan(Change{Path: ".gitmodules", After: []byte("unparsed")}); err == nil {
		t.Fatal("unparsed module values were treated as safe")
	}
}

func TestUnsafeTreePathsAreNeverOrdinaryFindings(t *testing.T) {
	scanner := scannerFixture(t, Options{})
	for _, path := range []string{"/absolute", "a/../b", ".git/config", "a/.GiT/config", "a\x1bname", "a\u202ename"} {
		findings, err := scanner.Scan(Change{Path: path})
		if err != nil {
			t.Fatal(err)
		}
		refused := false
		for _, finding := range findings {
			refused = refused || finding.Refused
		}
		if !refused {
			t.Fatalf("unsafe path was not refused: %q %+v", path, findings)
		}
	}
}

func TestSafetyRulesAreCaseFoldedAndOversizedModulesStayUnread(t *testing.T) {
	scanner := scannerFixture(t, Options{})
	findings, err := scanner.Scan(Change{Path: ".GitHub/Workflows/ci.yml"})
	if err != nil || !hasCategory(findings, "CI") {
		t.Fatalf("mixed-case execution path escaped review: %+v %v", findings, err)
	}
	findings, err = scanner.Scan(Change{Path: ".GITMODULES", Size: FileLimit + 1, After: []byte("must-not-be-parsed")})
	if err != nil {
		t.Fatal(err)
	}
	refused := false
	for _, finding := range findings {
		refused = refused || finding.Refused
	}
	if !refused || !hasCategory(findings, "large") {
		t.Fatal("oversized modules were reported as clean or only overridable", findings)
	}
	findings, err = scanner.Scan(Change{Path: "source", Mode: "unknown"})
	if err != nil || len(findings) == 0 || !findings[0].Refused {
		t.Fatal("unknown tree mode was not refused", findings, err)
	}
}
