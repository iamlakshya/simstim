package main

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
	"go.yaml.in/yaml/v3"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata")

func TestCopyURL(t *testing.T) {
	for url, want := range map[string]string{
		"/":                    "/index.md",
		"/how-to/":             "/how-to.md",
		"/how-to/add-a-host/":  "/how-to/add-a-host.md",
		"/reference/cli/":      "/reference/cli.md",
		"/project/changelog/":  "/project/changelog.md",
		"/reference/cli/host/": "/reference/cli/host.md",
	} {
		if got := copyURL(url); got != want {
			t.Errorf("copyURL(%q) = %q, want %q", url, got, want)
		}
	}
	want := "  - tag: link\n    attrs:\n      rel: alternate\n      type: text/markdown\n      href: \"../install.md\"\n"
	if got := alternate("/how-to/install/", "  "); got != want {
		t.Errorf("alternate:\n%s\nwant:\n%s", got, want)
	}
}

// The link to the copy joins a head list that the page has already, at
// that list's indent, so the front matter keeps one head key. Comments and
// blank lines do not end the list. Only a block list can take the link.
// Each result parses as YAML, with the link as the last item of head.
func TestJoinHead(t *testing.T) {
	item := func(ind string) []string {
		return strings.Split(strings.TrimSuffix(alternate("/how-to/x/", ind), "\n"), "\n")
	}
	join := func(lines ...[]string) []string {
		var out []string
		for _, l := range lines {
			out = append(out, l...)
		}
		return out
	}
	for _, c := range []struct {
		name  string
		keep  []string
		want  []string // nil: check only that it parses
		items int      // the head list's items after the join, 0: no head
	}{
		{"no head", []string{"status: accepted"}, []string{"status: accepted"}, 0},
		{"an indented list", []string{"head:", "  - tag: meta", "    attrs:", "      name: robots", "status: accepted"},
			join([]string{"head:", "  - tag: meta", "    attrs:", "      name: robots"}, item("  "), []string{"status: accepted"}), 2},
		{"a list at the margin", []string{"status: accepted", "head:", "- tag: meta", "  content: x"},
			join([]string{"status: accepted", "head:", "- tag: meta", "  content: x"}, item("")), 2},
		{"an empty list", []string{"head: []", "status: accepted"}, join([]string{"head:"}, item("  "), []string{"status: accepted"}), 1},
		{"a comment on the key", []string{"head: # for the robots", "  - tag: meta", "    content: x", "status: accepted"},
			join([]string{"head: # for the robots", "  - tag: meta", "    content: x"}, item("  "), []string{"status: accepted"}), 2},
		{"a comment at the margin before a list at the margin", []string{"head:", "# for the robots", "- tag: meta", "  content: x", "status: accepted"},
			join([]string{"head:", "# for the robots", "- tag: meta", "  content: x"}, item(""), []string{"status: accepted"}), 2},
		{"blank lines and comments in the list", []string{"head:", "  - tag: meta", "", "  # the style", "  - tag: style", "    content: x", "", "# the status", "status: accepted"},
			join([]string{"head:", "  - tag: meta", "", "  # the style", "  - tag: style", "    content: x"}, item("  "), []string{"", "# the status", "status: accepted"}), 3},
		{"a head with no value", []string{"head:", "", "status: accepted"}, nil, 1},
	} {
		got, joined, err := joinHead(c.keep, "/how-to/x/")
		if err != nil || joined != (c.items > 0) || c.want != nil && strings.Join(got, "\n") != strings.Join(c.want, "\n") {
			t.Errorf("%s: joined %v, err %v:\n%s\nwant:\n%s", c.name, joined, err, strings.Join(got, "\n"), strings.Join(c.want, "\n"))
		}
		if c.items > 0 {
			checkHead(t, c.name, strings.Join(got, "\n"), c.items)
		}
	}
	for _, flow := range []string{"head: [{tag: meta}]", "head: [{tag: meta}] # robots"} {
		if _, _, err := joinHead([]string{flow}, "/how-to/x/"); err == nil || !strings.Contains(err.Error(), "Try:") {
			t.Errorf("%s: err %v, want an error with a hint", flow, err)
		}
	}

	md, err := withFrontMatter(viewOf("---\nhead: # robots\n# none\n- tag: meta\n  attrs:\n    name: robots\nstatus: accepted\n---\n\n# X\n\nText.\n"), page{src: "docs/how-to/x.md", url: "/how-to/x/"}, "https://github.com/o/r")
	if err != nil {
		t.Fatal(err)
	}
	fm, _, _ := strings.Cut(strings.TrimPrefix(md, "---\n"), "\n---\n")
	checkHead(t, "withFrontMatter", fm, 2)
}

// checkHead parses front matter as YAML, which refuses a key given twice,
// and checks that its head list has items items, the last of them the link
// to the copy of /how-to/x/.
func checkHead(t *testing.T, name, fm string, items int) {
	t.Helper()
	var m map[string]any
	if err := yaml.Unmarshal([]byte(fm+"\n"), &m); err != nil {
		t.Errorf("%s: the front matter is not valid YAML: %v\n%s", name, err, fm)
		return
	}
	head, _ := m["head"].([]any)
	if len(head) != items {
		t.Errorf("%s: head has %d items, want %d:\n%s", name, len(head), items, fm)
		return
	}
	last, _ := head[len(head)-1].(map[string]any)
	attrs, _ := last["attrs"].(map[string]any)
	if last["tag"] != "link" || attrs["rel"] != "alternate" || attrs["type"] != "text/markdown" || attrs["href"] != "../x.md" {
		t.Errorf("%s: the last item of head is not the link to the copy: %v", name, last)
	}
}

// A small docs tree: each copy is its page's title and its body after the
// link rewrite, with links to pages going to their copies, and llms.txt
// matches its golden file.
func TestCopies(t *testing.T) {
	pages := []page{
		{src: "README.md", url: "/", md: "# Tool\n\n**Bold line.**\n\nRun agents on hosts. See [install](docs/how-to/install.md) and the [script](install.sh).\n"},
		{src: "docs/README.md", url: "/docs/", md: "# Docs\n\nThe docs. Start with [how-to guides](how-to/README.md) or [the guides folder](how-to/).\n"},
		{src: "docs/how-to/README.md", url: "/how-to/", md: "# How-to guides\n\nOne task per page.\n\n| Guide | Task |\n| --- | --- |\n| [Install](install.md) | Install it |\n"},
		{src: "docs/how-to/install.md", url: "/how-to/install/", md: "# Install\n\nInstall the client. Then read [the reference](../reference/_index.md#flags), go [home](../../README.md), or [back](#top).\n\n![A screen](../images/screen.svg \"The screen\")\n\n```sh\n[not a link](install.md)\n```\n\nSee `[code](install.md)` and [`the guides`](README.md).\n\n[ref]: ../README.md\n"},
		{src: "docs/reference/_index.md", url: "/reference/", md: "---\ntitle: \"Reference [all]\"\nweight: 1\n---\n\n<!-- Generated. -->\n\nGenerated from the code.\n"},
		{src: "docs/contributing/notes.md", url: "/contributing/notes/", md: "# Notes\n\nNotes for contributors.\n"},
		{src: "docs/faq.md", url: "/faq/", md: "# FAQ\n\n| Question | Answer |\n"},
	}
	byURL := map[string]string{"docs": "/docs/", "docs/how-to": "/how-to/", "docs/reference": "/reference/"}
	for _, p := range pages {
		byURL[p.src] = p.url
	}
	// The Project tab comes first here: "Optional" must still be last. The
	// tab "Empty" has no page, so llms.txt has no section for it.
	nav := []tab{
		{Label: "Project", Prefixes: []string{"/contributing/"}, optional: true},
		{Label: "Docs", Prefixes: []string{"/docs/", "/how-to/", "/faq/"}, Items: []item{
			{Label: "Overview", Slug: "docs"},
			{Label: "Guides", Items: []item{{Label: "Install", Slug: "how-to/install"}, {Label: "All guides", Slug: "how-to"}}},
		}},
		{Label: "Reference", Prefixes: []string{"/reference/"}, Items: []item{{Label: "Overview", Slug: "reference"}}},
		{Label: "Empty", Prefixes: []string{"/empty/"}},
	}
	public := t.TempDir()
	root := openRoot(t, public)
	if err := writeCopies(root, pages, entriesOf(pages, byURL), byURL, nav, "https://github.com/o/r", "https://docs.example.org/dev/"); err != nil {
		t.Fatal(err)
	}
	for file, want := range map[string]string{
		"index.md":     "# Tool\n\n**Bold line.**\n\nRun agents on hosts. See [install](how-to/install.md) and the [script](https://github.com/o/r/blob/main/install.sh).\n",
		"docs.md":      "# Docs\n\nThe docs. Start with [how-to guides](how-to.md) or [the guides folder](how-to.md).\n",
		"how-to.md":    "# How-to guides\n\nOne task per page.\n\n| Guide | Task |\n| --- | --- |\n| [Install](how-to/install.md) | Install it |\n",
		"reference.md": "# Reference [all]\n\n<!-- Generated. -->\n\nGenerated from the code.\n",
		"how-to/install.md": "# Install\n\nInstall the client. Then read [the reference](../reference.md#flags), go [home](../index.md), or [back](#top).\n\n" +
			"![A screen](../images/screen.svg \"The screen\")\n\n```sh\n[not a link](install.md)\n```\n\nSee `[code](install.md)` and [`the guides`](../how-to.md).\n\n[ref]: ../docs.md\n",
		"contributing/notes.md": "# Notes\n\nNotes for contributors.\n",
		"faq.md":                "# FAQ\n\n| Question | Answer |\n",
	} {
		got, err := os.ReadFile(filepath.Join(public, file))
		if err != nil {
			t.Error(err)
			continue
		}
		if string(got) != want {
			t.Errorf("%s:\n%s\nwant:\n%s", file, got, want)
		}
	}
	got, err := os.ReadFile(filepath.Join(public, "llms.txt"))
	if err != nil {
		t.Fatal(err)
	}
	golden := filepath.Join("cmd", "docgen", "testdata", "llms.txt")
	if _, err := os.Stat("go.mod"); err != nil {
		golden = filepath.Join("testdata", "llms.txt") // run from cmd/docgen
	}
	if *update {
		if err := os.WriteFile(golden, []byte(toGolden(string(got))), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(want), brand.Name) || strings.Contains(string(want), brand.DisplayName) {
		t.Errorf("%s spells the name: write {{name}} or {{display}}, which the test fills in from internal/brand", golden)
	}
	if string(got) != fromGolden(string(want)) {
		t.Errorf("llms.txt:\n%s\nwant (%s, -update rewrites it):\n%s", got, golden, fromGolden(string(want)))
	}
}

// entriesOf is each page's title and description, as sitePages finds them.
func entriesOf(pages []page, byURL map[string]string) map[string]entry {
	entries := map[string]entry{}
	for _, p := range pages {
		v := viewOf(rewriteLinks(p.md, p.src, p.url, byURL, "https://github.com/o/r"))
		entries[p.url] = entry{title: v.title, desc: describe(v.body)}
	}
	return entries
}

// The golden file holds no product name, so a rename leaves it as it is:
// {{name}} stands for brand.Name in a command, {{display}} for
// brand.DisplayName everywhere else.
func fromGolden(s string) string {
	return strings.NewReplacer("{{name}}", brand.Name, "{{display}}", brand.DisplayName).Replace(s)
}

func toGolden(s string) string {
	s = strings.ReplaceAll(s, "`"+brand.Name+" <command>", "`{{name}} <command>")
	return strings.ReplaceAll(s, brand.DisplayName, "{{display}}")
}

// openRoot opens public as the site's public folder, for the length of the
// test.
func openRoot(t *testing.T, public string) *os.Root {
	t.Helper()
	root, err := openPublic(public)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return root
}

// clearCopies removes the copies and llms.txt of the last run, and nothing
// else. openPublic makes a public folder that does not exist yet.
func TestClearCopies(t *testing.T) {
	public := t.TempDir()
	for _, f := range []string{"llms.txt", "index.md", "how-to/install.md", "fonts/sans.woff2", "favicon.svg", "fonts/LICENSE.txt"} {
		if err := writeFile(filepath.Join(public, f), []byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	if err := clearCopies(openRoot(t, public)); err != nil {
		t.Fatal(err)
	}
	for f, kept := range map[string]bool{"llms.txt": false, "index.md": false, "how-to/install.md": false, "fonts/sans.woff2": true, "favicon.svg": true, "fonts/LICENSE.txt": true} {
		if _, err := os.Stat(filepath.Join(public, f)); (err == nil) != kept {
			t.Errorf("%s: kept is %v, want %v", f, err == nil, kept)
		}
	}
	missing := filepath.Join(public, "missing")
	if err := clearCopies(openRoot(t, missing)); err != nil {
		t.Errorf("a missing folder: %v", err)
	}
	if info, err := os.Stat(missing); err != nil || !info.IsDir() {
		t.Errorf("openPublic did not make the missing folder: %v", err)
	}
}

// clearCopies follows no symbolic link: it does not go into a linked
// folder, and leaves a link to a file. openPublic refuses a public folder
// that is a link.
func TestClearCopiesLinks(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(dir, "outside")
	for _, f := range []string{"a.md", "sub/b.md", "llms.txt", "c.md"} {
		if err := writeFile(filepath.Join(outside, f), []byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	public := filepath.Join(dir, "public")
	if err := writeFile(filepath.Join(public, "old.md"), []byte("x")); err != nil {
		t.Fatal(err)
	}
	for link, target := range map[string]string{"linked": outside, "link.md": filepath.Join(outside, "c.md")} {
		if err := os.Symlink(target, filepath.Join(public, link)); err != nil {
			t.Skip("no symbolic links here:", err)
		}
	}
	if err := clearCopies(openRoot(t, public)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(public, "old.md")); !os.IsNotExist(err) {
		t.Error("old.md was kept")
	}
	if _, err := os.Lstat(filepath.Join(public, "link.md")); err != nil {
		t.Errorf("the link link.md was removed: %v", err)
	}
	linkedPublic := filepath.Join(dir, "linked-public")
	if err := os.Symlink(outside, linkedPublic); err != nil {
		t.Fatal(err)
	}
	if _, err := openPublic(linkedPublic); err == nil || !strings.Contains(err.Error(), "Try:") {
		t.Errorf("a linked public folder: err %v, want a refusal with a hint", err)
	}
	for _, f := range []string{"a.md", "sub/b.md", "llms.txt", "c.md"} {
		if _, err := os.Stat(filepath.Join(outside, f)); err != nil {
			t.Errorf("outside/%s, behind a link, was removed: %v", f, err)
		}
	}
}

// The cleanup, then the writes: a link where a copy or llms.txt goes is
// replaced by the file, not followed, and a linked folder that leads out of
// public stops the write with a hint. The files outside stay as they were.
func TestWritesStayInPublic(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(dir, "outside")
	for _, f := range []string{"index.md", "llms.txt", "sub/keep.txt"} {
		if err := writeFile(filepath.Join(outside, f), []byte("outside\n")); err != nil {
			t.Fatal(err)
		}
	}
	public := filepath.Join(dir, "public")
	if err := os.MkdirAll(public, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"index.md", "llms.txt"} {
		if err := os.Symlink(filepath.Join(outside, f), filepath.Join(public, f)); err != nil {
			t.Skip("no symbolic links here:", err)
		}
	}
	pages := []page{{src: "README.md", url: "/", md: "# Tool\n\nRun agents.\n"}, {src: "docs/faq.md", url: "/faq/", md: "# FAQ\n\nAsk.\n"}}
	byURL := map[string]string{"README.md": "/", "docs/faq.md": "/faq/"}
	nav := []tab{{Label: "Docs", Prefixes: []string{"/faq/"}}}
	root := openRoot(t, public)
	if err := clearCopies(root); err != nil {
		t.Fatal(err)
	}
	if err := writeCopies(root, pages, entriesOf(pages, byURL), byURL, nav, "https://github.com/o/r", "https://docs.example.org/"); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"index.md", "llms.txt"} {
		if b, err := os.ReadFile(filepath.Join(outside, f)); err != nil || string(b) != "outside\n" {
			t.Errorf("outside/%s changed: %q, %v", f, b, err)
		}
		info, err := os.Lstat(filepath.Join(public, f))
		if err != nil || !info.Mode().IsRegular() {
			t.Errorf("public/%s is not a plain file: %v", f, err)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(public, "index.md")); !strings.HasPrefix(string(b), "# Tool\n") {
		t.Errorf("public/index.md is not the copy: %q", b)
	}

	linked := filepath.Join(dir, "linked-folder")
	if err := os.MkdirAll(linked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "sub"), filepath.Join(linked, "how-to")); err != nil {
		t.Fatal(err)
	}
	how := []page{{src: "docs/how-to/x.md", url: "/how-to/x/", md: "# X\n\nText.\n"}}
	err := writeCopies(openRoot(t, linked), how, entriesOf(how, map[string]string{}), map[string]string{}, nav, "https://github.com/o/r", "https://docs.example.org/")
	if err == nil || !strings.Contains(err.Error(), "Try:") {
		t.Errorf("a linked folder that leads out: err %v, want a refusal with a hint", err)
	}
	if entries, _ := os.ReadDir(filepath.Join(outside, "sub")); len(entries) != 1 {
		t.Errorf("outside/sub has %d files, want only keep.txt", len(entries))
	}
}

// The real docs: llms.txt lists every page of pages.json once, with a
// description, as a link to a copy that exists, with "Optional" last, and
// each page links to its copy in its head.
func TestSiteCopies(t *testing.T) {
	if _, err := os.Stat("go.mod"); err != nil {
		t.Chdir("../..") // back to cmd/docgen at the end, where TestRunIsStable expects to start
	}
	dir := t.TempDir()
	out, public := filepath.Join(dir, "src"), filepath.Join(dir, "public")
	// Without an address, it stops before it reads or clears anything.
	if err := sitePages(filepath.Join(dir, "no-docs"), out, "https://github.com/o/r", ""); err == nil || !strings.Contains(err.Error(), "-site-url") {
		t.Errorf("no -site-url: err %v", err)
	}
	stale := filepath.Join(public, "how-to", "gone.md")
	if err := writeFile(stale, []byte("old")); err != nil {
		t.Fatal(err)
	}
	const site = "https://docs.example.org/v1.2/"
	if err := sitePages("docs", out, "https://github.com/o/r", site); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("the copy of a removed page was kept")
	}
	b, err := os.ReadFile(filepath.Join(out, "generated", "pages.json"))
	if err != nil {
		t.Fatal(err)
	}
	var pages map[string]string
	if err := json.Unmarshal(b, &pages); err != nil {
		t.Fatal(err)
	}
	llms, err := os.ReadFile(filepath.Join(public, "llms.txt"))
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]int{}
	var first, lastH2 string
	line := regexp.MustCompile(`^- \[(?:\\.|[^\]\\])*\]\(` + regexp.QuoteMeta(site) + `(\S+)\): \S`)
	for _, l := range strings.Split(string(llms), "\n") {
		if strings.HasPrefix(l, "## ") {
			lastH2 = l
		}
		if m := line.FindStringSubmatch(l); m != nil {
			listed["/"+m[1]]++
			if first == "" {
				first = m[1]
			}
		} else if strings.HasPrefix(l, "- ") {
			t.Errorf("llms.txt: a page without a description, or not under %s: %s", site, l)
		}
	}
	for _, url := range pages {
		c := copyURL(url)
		if n := listed[c]; n != 1 {
			t.Errorf("llms.txt lists %s %d times, want once", c, n)
		}
		if _, err := os.Stat(filepath.Join(public, filepath.FromSlash(c))); err != nil {
			t.Errorf("no copy of %s: %v", url, err)
		}
	}
	if first != "index.md" || lastH2 != "## Optional" || !strings.HasPrefix(string(llms), "# ") || !strings.HasSuffix(string(llms), "\n") {
		t.Errorf("llms.txt: the first page is %q and the last section %q, want index.md and ## Optional:\n%.300s", first, lastH2, llms)
	}
	page, _ := os.ReadFile(filepath.Join(out, "content", "docs", "how-to", "install.md"))
	if !strings.Contains(string(page), "type: text/markdown\n      href: \"../install.md\"\n") {
		t.Errorf("install.md does not link to its copy:\n%.600s", page)
	}
	// The snippet for a project's AGENTS.md: at most 15 lines of plain
	// Markdown, with no front matter (the CLI spec, 16.1).
	guide, err := os.ReadFile(filepath.Join("docs", "how-to", "use-with-agents.md"))
	if err != nil {
		t.Fatal(err)
	}
	_, rest, ok := strings.Cut(string(guide), "```markdown\n")
	snippet, _, ok2 := strings.Cut(rest, "\n```\n")
	if !ok || !ok2 || strings.HasPrefix(snippet, "---\n") || strings.Count(snippet, "\n")+1 > 15 {
		t.Errorf("the AGENTS.md snippet in use-with-agents.md must be at most 15 lines of plain Markdown, with no front matter:\n%s", snippet)
	}
	brandJSON, _ := os.ReadFile(filepath.Join(out, "generated", "brand.json"))
	if !strings.Contains(string(brandJSON), `"description": `) {
		t.Error("brand.json has no description for the site")
	}
}
