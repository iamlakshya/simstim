package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
)

func TestPageName(t *testing.T) {
	for in, want := range map[string]string{
		brand.Name:                  "_index.md",
		brand.Name + " host":        "host.md",
		brand.Name + " host add":    "host_add.md",
		brand.Name + " config path": "config_path.md",
	} {
		if got := pageName(in); got != want {
			t.Errorf("pageName(%q) = %q, want %q", in, got, want)
		}
	}
}

// The generator writes every page, leaves no page of a removed command, and
// gives the same bytes on a second run.
func TestRunIsStable(t *testing.T) {
	if err := os.Chdir("../.."); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	stale := filepath.Join(dir, "cli", "gone.md")
	if err := os.MkdirAll(filepath.Dir(stale), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	man, comp := filepath.Join(dir, "man"), filepath.Join(dir, "comp")
	if err := run(dir, man, comp); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("the page of a removed command was kept")
	}
	first := snapshot(t, dir)
	if err := run(dir, "", ""); err != nil {
		t.Fatal(err)
	}
	if second := snapshot(t, dir); second != first {
		t.Error("a second run changed the pages")
	}
	for _, f := range []string{"config.md", "keymap.md", "agents.md", "cli/_index.md", "cli/host_add.md"} {
		b, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil || !strings.Contains(string(b), "Do not edit by hand") {
			t.Errorf("%s: missing or not marked generated", f)
		}
	}
	if m, _ := filepath.Glob(filepath.Join(man, "*.1")); len(m) < 10 {
		t.Errorf("%d man pages", len(m))
	}
	if c, _ := filepath.Glob(filepath.Join(comp, "*")); len(c) != 4 {
		t.Errorf("%d completion scripts, want 4", len(c))
	}
}

func snapshot(t *testing.T, dir string) string {
	t.Helper()
	var b strings.Builder
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.HasSuffix(p, ".md") {
			c, _ := os.ReadFile(p)
			b.WriteString(p + "\n" + string(c))
		}
		return nil
	})
	return b.String()
}

// The site hosts every page in docs/ and the community files at the root,
// maps each one's path in the repository to its page, and rewrites the
// links between them, so they stay on the site under any base.
func TestSitePages(t *testing.T) {
	if _, err := os.Stat("go.mod"); err != nil {
		if err := os.Chdir("../.."); err != nil {
			t.Fatal(err)
		}
	}
	out := filepath.Join(t.TempDir(), "src")
	if err := sitePages("docs", out, "https://github.com/o/r"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(out, "generated", "pages.json"))
	if err != nil {
		t.Fatal(err)
	}
	var pages map[string]string
	if err := json.Unmarshal(b, &pages); err != nil {
		t.Fatal(err)
	}
	for src, want := range map[string]string{
		"README.md":                      "/",
		"docs/README.md":                 "/docs/",
		"docs/how-to/README.md":          "/how-to/",
		"docs/how-to":                    "/how-to/",
		"docs/how-to/install.md":         "/how-to/install/",
		"docs/reference/_index.md":       "/reference/",
		"docs/reference/cli/host_add.md": "/reference/cli/host_add/",
		"CHANGELOG.md":                   "/project/changelog/",
		"CONTRIBUTING.md":                "/project/contributing/",
		"examples/providers/README.md":   "/reference/provider-scripts/",
	} {
		if got := pages[src]; got != want {
			t.Errorf("pages[%q] = %q, want %q", src, got, want)
		}
	}
	for _, f := range []string{"content/docs/docs.md", "content/docs/how-to/index.md", "content/docs/project/index.md", "content/docs/project/changelog.md", "generated/brand.json", "generated/agents.json", "generated/sidebar.json", "../public/images/sessions.svg"} {
		if _, err := os.Stat(filepath.Join(out, f)); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
	page, _ := os.ReadFile(filepath.Join(out, "content", "docs", "how-to", "install.md"))
	for _, want := range []string{"srcPath: \"docs/how-to/install.md\"\n", "editUrl: \"https://github.com/o/r/edit/main/docs/how-to/install.md\"\n", "](../../contributing/releasing/#checking-a-release)"} {
		if !strings.Contains(string(page), want) {
			t.Errorf("install.md has no %q:\n%.400s", want, page)
		}
	}
}

func TestResolve(t *testing.T) {
	if _, err := os.Stat("go.mod"); err != nil {
		if err := os.Chdir("../.."); err != nil {
			t.Fatal(err)
		}
	}
	pages := map[string]string{"docs/how-to/install.md": "/how-to/install/", "docs/README.md": "/docs/", "README.md": "/", "CHANGELOG.md": "/project/changelog/"}
	for _, c := range []struct{ dest, src, url, want string }{
		{"install.md#from-source", "docs/how-to/add-a-host.md", "/how-to/add-a-host/", "../install/#from-source"},
		{"../how-to/install.md", "docs/tutorials/getting-started.md", "/tutorials/getting-started/", "../../how-to/install/"},
		{"../CHANGELOG.md", "docs/README.md", "/docs/", "../project/changelog/"},
		{"../README.md", "docs/README.md", "/docs/", "../"},
		{"../images/attach.svg", "docs/how-to/install.md", "/how-to/install/", "../../images/attach.svg"},
		{"../../internal/agents", "docs/reference/agents.md", "/reference/agents/", "https://github.com/o/r/tree/main/internal/agents"},
		{"../../scripts/setup.sh", "docs/contributing/testing.md", "/contributing/testing/", "https://github.com/o/r/blob/main/scripts/setup.sh"},
		{"#top", "docs/README.md", "/docs/", "#top"},
		{"https://example.com", "docs/README.md", "/docs/", "https://example.com"},
	} {
		if got := resolve(c.dest, c.src, c.url, pages, "https://github.com/o/r"); got != c.want {
			t.Errorf("resolve(%q from %s) = %q, want %q", c.dest, c.src, got, c.want)
		}
	}
	md := "See [install](install.md) and `[not](install.md)`.\n| [`install`](install.md) | x |\n\n```sh\n[no](install.md)\n```\n\n[ref]: install.md\n"
	got := rewriteLinks(md, "docs/how-to/x.md", "/how-to/x/", pages, "https://github.com/o/r")
	want := "See [install](../install/) and `[not](install.md)`.\n| [`install`](../install/) | x |\n\n```sh\n[no](install.md)\n```\n\n[ref]: ../install/\n"
	if got != want {
		t.Errorf("rewriteLinks:\n%s\nwant:\n%s", got, want)
	}
	md = "![Holder & PTY](../images/attach.svg \"A < B\") and `![code](../images/attach.svg)`\n"
	got = rewriteLinks(md, "docs/how-to/x.md", "/how-to/x/", pages, "https://github.com/o/r")
	want = "<img src=\"../../images/attach.svg\" alt=\"Holder &amp; PTY\" title=\"A &lt; B\"> and `![code](../images/attach.svg)`\n"
	if got != want {
		t.Errorf("image URLs must be public assets, not Astro source imports:\n%s\nwant:\n%s", got, want)
	}
}

// Help text keeps its backslashes, angle brackets and web addresses in
// Markdown, and flags become code.
func TestInline(t *testing.T) {
	for in, want := range map[string]string{
		`detach with Ctrl+\); - is the last one`: `detach with Ctrl+\\); - is the last one`,
		`unless you add --add, then <host>`:      "unless you add `--add`, then &lt;host&gt;",
		"keep `ctrl+\\` as it is":                "keep `ctrl+\\` as it is",
		"(https://ntfy.sh/<topic> or yours)":     "(`https://ntfy.sh/<topic>` or yours)",
		"see https://example.com.":               "see `https://example.com`.",
	} {
		if got := inline(in); got != want {
			t.Errorf("inline(%q) = %q, want %q", in, got, want)
		}
	}
}
