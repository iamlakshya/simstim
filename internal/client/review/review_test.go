package review

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
)

func TestCheckPath(t *testing.T) {
	flagged := map[string]string{
		".githooks/pre-commit":            "git",
		".husky/pre-push":                 "git",
		".gitmodules":                     "git",
		".gitattributes":                  "git",
		".github/workflows/ci.yml":        "CI",
		"sub/.github/workflows/build.yml": "CI",
		".gitlab-ci.yml":                  "CI",
		".circleci/config.yml":            "CI",
		"azure-pipelines.yml":             "CI",
		"Jenkinsfile":                     "CI",
		"package.json":                    "build and install",
		"web/package.json":                "build and install",
		"Makefile":                        "build and install",
		"setup.py":                        "build and install",
		"build.rs":                        "build and install",
		"pyproject.toml":                  "build and install",
		".claude/settings.json":           "agent project files",
		"CLAUDE.md":                       "agent project files",
		"AGENTS.md":                       "agent project files",
		".mcp.json":                       "agent project files",
		".codex/config.toml":              "agent project files",
		".opencode/config.json":           "agent project files",
		"opencode.json":                   "agent project files",
		".gemini/settings.json":           "agent project files",
		"GEMINI.md":                       "agent project files",
		".cursor/rules.md":                "agent project files",
		".cursorrules":                    "agent project files",
		".aider.conf.yml":                 "agent project files",
		".goosehints":                     "agent project files",
		".vscode/tasks.json":              "editor and shell",
		".vscode/settings.json":           "editor and shell",
		".idea/workspace.xml":             "editor and shell",
		".envrc":                          "editor and shell",
		"sub/.ENVRC":                      "editor and shell",
		".npmrc":                          "registry config",
		".yarnrc.yml":                     "registry config",
		"pip.conf":                        "registry config",
		".cargo/config.toml":              "registry config",
		"." + brand.Name + "/setup":       "this tool",
	}
	for p, want := range flagged {
		cat, ok := CheckPath(p)
		if !ok || cat != want {
			t.Errorf("CheckPath(%q) = (%q, %v), want (%q, true)", p, cat, ok, want)
		}
	}
	safe := []string{
		"main.go",
		"README.md",
		"src/app.ts",
		"docs/guide.md",
		"notes.txt",
		".gitignore",
	}
	for _, p := range safe {
		if cat, ok := CheckPath(p); ok {
			t.Errorf("CheckPath(%q) = (%q, true), want false", p, cat)
		}
	}
}

func TestCheckHiddenText(t *testing.T) {
	if CheckHiddenText([]byte("plain ASCII\twith\nnewlines\r\nand café 日本語")) {
		t.Error("plain text flagged as hidden")
	}
	for name, sample := range map[string]string{
		"bidi_rlo":    "ok\u202eevil",
		"zwsp":        "ok\u200bevil",
		"bom":         "\ufeffhello",
		"tag_char":    "hi" + string(rune(0xE0041)),
		"bidi_lri":    "x\u2066y",
		"word_joiner": "a\u2060b",
	} {
		if !CheckHiddenText([]byte(sample)) {
			t.Errorf("%s (%q) was not flagged", name, sample)
		}
	}
}

func TestCheckGitmodulesCR(t *testing.T) {
	if CheckGitmodulesCR([]byte("[submodule \"a\"]\n\tpath = a\n\turl = https://example.com/a\n")) {
		t.Error("clean .gitmodules flagged")
	}
	if !CheckGitmodulesCR([]byte("[submodule \"a\"]\r\n\tpath = a\r\n")) {
		t.Error("CR in .gitmodules was not flagged")
	}
}

func TestCheckPackageJSONScripts(t *testing.T) {
	base := []byte(`{"name":"a","scripts":{"test":"go test"}}`)
	sameScripts := []byte(`{"name":"a","version":"1.0.0","scripts":{"test":"go test"}}`)
	diffScripts := []byte(`{"name":"a","scripts":{"test":"go test","postinstall":"evil"}}`)
	noScripts := []byte(`{"name":"a"}`)
	if CheckPackageJSONScripts(base, sameScripts) {
		t.Error("unchanged scripts flagged")
	}
	if CheckPackageJSONScripts(noScripts, noScripts) {
		t.Error("missing scripts flagged")
	}
	if !CheckPackageJSONScripts(base, diffScripts) {
		t.Error("added postinstall script was not flagged")
	}
	if !CheckPackageJSONScripts(nil, base) {
		t.Error("new package.json with scripts was not flagged")
	}
}

func TestReviewGitDiff(t *testing.T) {
	dir := t.TempDir()
	gitRun := func(args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(context.Background(), "git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	gitRun("init", "-q", "-b", "main")
	write := func(rel string, data []byte) {
		t.Helper()
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.txt", []byte("hello\n"))
	write("package.json", []byte(`{"name":"x","scripts":{"test":"ok"}}`))
	gitRun("add", ".")
	gitRun("commit", "-q", "-m", "base")
	base := gitRun("rev-parse", "HEAD")

	// Clean change plus package.json version bump (scripts unchanged) => no findings.
	write("a.txt", []byte("hello world\n"))
	write("package.json", []byte(`{"name":"x","version":"2","scripts":{"test":"ok"}}`))
	gitRun("add", ".")
	gitRun("commit", "-q", "-m", "clean")
	cleanHead := gitRun("rev-parse", "HEAD")

	res, err := ReviewGitDiff(context.Background(), dir, base, cleanHead)
	if err != nil {
		t.Fatal(err)
	}
	if res.Flagged() {
		t.Fatalf("clean diff flagged: %+v", res.Findings)
	}

	// Flagged workflow, hidden text, .gitmodules with CR, and a >1 MiB file.
	write(".github/workflows/ci.yml", []byte("name: ci\n"))
	write("hidden.txt", []byte("ok\u202eevil\n"))
	write(".gitmodules", []byte("[submodule \"x\"]\r\n\tpath = x\r\n"))
	write("big.bin", make([]byte, MaxScanSize+10))
	gitRun("add", ".")
	gitRun("commit", "-q", "-m", "risky")
	riskyHead := gitRun("rev-parse", "HEAD")

	res, err = ReviewGitDiff(context.Background(), dir, cleanHead, riskyHead)
	if err != nil {
		t.Fatal(err)
	}
	if _, hard := res.HardBlocked(); !hard {
		t.Errorf("expected hard block for .gitmodules CR, got %+v", res.Findings)
	}
	cats := map[string]bool{}
	for _, f := range res.Findings {
		cats[f.Category] = true
	}
	for _, want := range []string{"CI", "hidden text", "gitmodules_cr", "large"} {
		if !cats[want] {
			t.Errorf("missing category %q in %+v", want, res.Findings)
		}
	}

	// Regression for CVE-2025-48384 on >1 MiB .gitmodules: carriage return
	// placed past MaxScanSize must still trigger the hard stop.
	bigGM := make([]byte, MaxScanSize+2048)
	for i := range bigGM {
		bigGM[i] = 'a'
	}
	bigGM[MaxScanSize+1024] = '\r'
	write(".gitmodules", bigGM)
	gitRun("add", ".gitmodules")
	gitRun("commit", "-q", "-m", "large gitmodules with CR")
	largeGMHead := gitRun("rev-parse", "HEAD")

	res, err = ReviewGitDiff(context.Background(), dir, riskyHead, largeGMHead)
	if err != nil {
		t.Fatal(err)
	}
	if hard, ok := res.HardBlocked(); !ok || hard.Category != "gitmodules_cr" {
		t.Fatalf("expected hard block on >1 MiB .gitmodules with CR, got %+v", res.Findings)
	}

	// Secret value and gateway sentinel detection in added lines (value never printed).
	const secretVal = "sk-ant-super-secret-value-987654321"
	write("leak.txt", []byte("token="+secretVal+"\n"))
	write("sentinel.txt", []byte("key="+DefaultSentinelPrefix+"abc123\n"))
	gitRun("add", "leak.txt", "sentinel.txt")
	gitRun("commit", "-q", "-m", "secret and sentinel leak")
	leakHead := gitRun("rev-parse", "HEAD")

	res, err = ReviewGitDiffWithOptions(context.Background(), dir, largeGMHead, leakHead, Options{
		Secrets: map[string]string{
			"ANTHROPIC_API_KEY": secretVal,
			"SHORT":             "ab", // ignored (< 4 chars)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	summary := res.Summary()
	if strings.Contains(summary, secretVal) {
		t.Fatalf("review summary leaked secret value: %q", summary)
	}
	var sawSecret, sawSentinel bool
	for _, f := range res.Findings {
		if strings.Contains(f.Detail, secretVal) || strings.Contains(f.Path, secretVal) {
			t.Fatalf("finding leaked secret value: %+v", f)
		}
		if f.Path == "leak.txt" && f.Category == "secrets" && f.Detail == "contains secret ANTHROPIC_API_KEY" {
			sawSecret = true
		}
		if f.Path == "sentinel.txt" && f.Category == "secrets" && f.Detail == "contains a session sentinel" {
			sawSentinel = true
		}
	}
	if !sawSecret || !sawSentinel {
		t.Fatalf("expected both secret and sentinel findings, got %+v", res.Findings)
	}
}
