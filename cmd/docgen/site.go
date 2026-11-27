package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/brand"
)

// hosted lists the Markdown files outside docs/ that the site hosts too, so
// a link to one stays on the site. The community files at the root of the
// repository are the Project tab's pages.
var hosted = map[string]string{
	"CHANGELOG.md":                 "project/changelog.md",
	"SUPPORT.md":                   "project/support.md",
	"SECURITY.md":                  "project/security.md",
	"CONTRIBUTING.md":              "project/contributing.md",
	"CODE_OF_CONDUCT.md":           "project/code-of-conduct.md",
	"GOVERNANCE.md":                "project/governance.md",
	"MAINTAINERS.md":               "project/maintainers.md",
	"examples/providers/README.md": "reference/provider-scripts.md",
}

// projectIndex is the Project tab's overview. Its links are relative to
// docs/project/, as if it were a file there.
const projectIndex = `# The project

How the project is run, what changed, what comes next, and how to help build it.

| Page | What it covers |
| --- | --- |
| [Changelog](../../CHANGELOG.md) | What changed in each release, and what is not released yet |
| [Roadmap](../roadmap.md) | Every candidate feature, scored by demand, impact and effort |
| [Getting help](../../SUPPORT.md) | Where to ask a question, report a bug or ask for a provider |
| [Security policy](../../SECURITY.md) | How to report a security problem in private |
| [How to contribute](../../CONTRIBUTING.md) | How to set up, the rules for the code, and how to send a change |
| [Contributor guide](../contributing/README.md) | How the project is built, tested, documented and released |
| [Decision records](../contributing/decisions/README.md) | Each design choice and its reasons |
| [Research notes](../research/README.md) | The evidence behind the design |
| [Governance](../../GOVERNANCE.md) | Who decides what, and how |
| [Maintainers](../../MAINTAINERS.md) | Who maintains the project |
| [Code of conduct](../../CODE_OF_CONDUCT.md) | How we treat each other |
`

// siteDescription is the site's description: Starlight's, which
// site/astro.config.mjs reads from generated/brand.json, and the quote at
// the top of llms.txt.
const siteDescription = brand.DisplayName + " runs Claude Code, Codex or any terminal agent in a session on a machine you own, and shows you its real screen, byte for byte."

// page is one Markdown file of the repository as a page of the site.
type page struct {
	src string // its path in the repository, or "" for a generated page
	dst string // its path under content/docs
	url string // its address on the site, such as /how-to/install/
	md  string // its Markdown, before links are rewritten
}

// sitePages turns docs/ and the community files into the docs site's
// content (decision 0022). The files stay plain Markdown that reads well on
// GitHub; here each README.md becomes its folder's index page, a page's
// first "# " heading becomes its title, and every link between two hosted
// files becomes a link between their pages, so a reader never leaves the
// site for a page it hosts. Links to other files (code, scripts) go to
// their source on GitHub. Images are copied next to the pages.
//
// It writes, under out (the site's src folder):
//   - content/docs/: the pages;
//   - generated/pages.json: every hosted file's path in the repository and
//     its page, for scripts/check-site-links.py;
//   - generated/sidebar.json: the three tabs and their sidebars (sitenav.go);
//   - generated/brand.json and agents.json, for the landing page and the
//     site's description;
//   - ../public/: the images, a Markdown copy of each page, and llms.txt,
//     which lists the copies for siteURL, the address of this build
//     (copies.go).
func sitePages(docs, out, repo, siteURL string) error {
	if siteURL == "" {
		return errors.New("-site needs -site-url, the address of this build of the site. Try: scripts/docs-site.sh, which passes it")
	}
	content := filepath.Join(out, "content", "docs")
	for _, dir := range []string{content, filepath.Join(out, "generated")} {
		if err := os.RemoveAll(dir); err != nil {
			return fmt.Errorf("clear %s: %w", dir, err)
		}
	}
	public, err := openPublic(filepath.Join(out, "..", "public"))
	if err != nil {
		return err
	}
	defer func() { _ = public.Close() }()
	if err := clearCopies(public); err != nil {
		return err
	}
	pages, err := collect(docs)
	if err != nil {
		return err
	}
	byURL := map[string]string{"README.md": "/"}
	for _, p := range pages {
		byURL[p.src] = p.url
		if base := path.Base(p.src); base == "README.md" || base == "_index.md" {
			byURL[path.Dir(p.src)] = p.url // a link to the folder
		}
	}
	byURL["docs/project"] = "/project/"
	// The landing page draws its own content, so the site gets no page from
	// the repository's README. It is the landing page's Markdown copy: the
	// file that links to / stand for.
	readme, err := os.ReadFile("README.md")
	if err != nil {
		return fmt.Errorf("read README.md: %w", err)
	}
	all := append([]page{{src: "README.md", url: "/", md: string(readme)}}, pages...)
	entries := make(map[string]entry, len(all))
	for _, p := range all {
		v := viewOf(rewriteLinks(p.md, p.src, p.url, byURL, repo))
		entries[p.url] = entry{title: v.title, desc: describe(v.body)}
		if p.dst == "" {
			continue // the landing page
		}
		md, err := withFrontMatter(v, p, repo)
		if err != nil {
			return err
		}
		if err := writeFile(filepath.Join(content, p.dst), []byte(md)); err != nil {
			return err
		}
	}
	if err := copyImages(docs, public); err != nil {
		return err
	}
	nav, err := sidebar(docs, byURL)
	if err != nil {
		return err
	}
	if err := writeCopies(public, all, entries, byURL, nav, repo, siteURL); err != nil {
		return err
	}
	return siteData(filepath.Join(out, "generated"), byURL, nav)
}

// collect reads every page the site hosts.
func collect(docs string) ([]page, error) {
	var pages []page
	err := filepath.WalkDir(docs, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(docs, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel == "research/private" || rel == "images" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(rel, ".md") || rel == "contributing/decisions/template.md" {
			return nil
		}
		dst := rel
		switch {
		case rel == "README.md":
			dst = "docs.md"
		case path.Base(rel) == "README.md" || path.Base(rel) == "_index.md":
			dst = path.Join(path.Dir(rel), "index.md")
		}
		return add(&pages, path.Join("docs", rel), dst, "")
	})
	if err != nil {
		return nil, fmt.Errorf("read the pages in %s: %w", docs, err)
	}
	srcs := make([]string, 0, len(hosted))
	for src := range hosted {
		srcs = append(srcs, src)
	}
	sort.Strings(srcs)
	for _, src := range srcs {
		if err := add(&pages, src, hosted[src], ""); err != nil {
			return nil, err
		}
	}
	return pages, add(&pages, "docs/project/README.md", "project/index.md", projectIndex)
}

// add reads one page, or takes md as its text when it is generated.
func add(pages *[]page, src, dst, md string) error {
	if md == "" {
		b, err := os.ReadFile(src)
		if err != nil {
			return fmt.Errorf("read %s: %w", src, err)
		}
		md = string(b)
	}
	url := "/" + strings.TrimSuffix(dst, ".md") + "/"
	if path.Base(dst) == "index.md" {
		url = "/" + path.Dir(dst) + "/"
	}
	*pages = append(*pages, page{src: src, dst: dst, url: url, md: md})
	return nil
}

// view is a page as the site shows it. It holds the front matter that the
// page keeps (apart from the Hugo-only weight), its title from the front
// matter or the first "# " heading, and its body without that heading,
// since the site prints the title.
type view struct {
	keep  []string
	title string
	body  string
}

// viewOf splits a page's Markdown into its view.
func viewOf(md string) view {
	var v view
	if strings.HasPrefix(md, "---\n") {
		if end := strings.Index(md[4:], "\n---\n"); end >= 0 {
			for _, l := range strings.Split(md[4:4+end], "\n") {
				if t, ok := strings.CutPrefix(l, "title:"); ok {
					v.title = unquote(strings.TrimSpace(t))
				} else if !strings.HasPrefix(l, "weight:") {
					v.keep = append(v.keep, l)
				}
			}
			md = md[4+end+5:]
		}
	}
	title, body := splitTitle(md)
	if v.title == "" {
		v.title = title
	}
	v.body = body
	return v
}

// withFrontMatter gives a page the front matter the site needs: its title,
// a description from its first paragraph, where its source is, where to
// suggest a change (unless the page is generated), and a link to its
// Markdown copy in its head. Front matter the page has already is kept,
// apart from the Hugo-only weight. The link joins the page's own head list
// when it has one.
func withFrontMatter(v view, p page, repo string) (string, error) {
	keep, joined, err := joinHead(v.keep, p.url)
	if err != nil {
		return "", fmt.Errorf("%s: %w", p.src, err)
	}
	var fm strings.Builder
	fm.WriteString("---\n")
	if v.title != "" {
		fm.WriteString("title: " + quote(v.title) + "\n")
	}
	for _, l := range keep {
		fm.WriteString(l + "\n")
	}
	if d := describe(v.body); d != "" {
		fm.WriteString("description: " + quote(d) + "\n")
	}
	fm.WriteString("srcPath: " + quote(p.src) + "\n")
	generated := strings.HasPrefix(p.src, "docs/reference/") && p.src != "docs/reference/README.md" || p.src == "docs/project/README.md"
	if generated {
		fm.WriteString("editUrl: false\ngenerated: true\n")
	} else {
		fm.WriteString("editUrl: " + quote(repo+"/edit/main/"+p.src) + "\n")
	}
	if !joined {
		fm.WriteString("head:\n" + alternate(p.url, "  "))
	}
	fm.WriteString("---\n")
	return fm.String() + strings.TrimLeft(v.body, "\n"), nil
}

// splitTitle takes a page's first "# " heading outside code as its title.
func splitTitle(md string) (string, string) {
	var title string
	var out strings.Builder
	sc := bufio.NewScanner(strings.NewReader(md))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	inFence := false
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "```") {
			inFence = !inFence
		}
		if title == "" && !inFence && strings.HasPrefix(line, "# ") {
			title = strings.TrimSpace(strings.TrimPrefix(line, "# "))
			continue
		}
		out.WriteString(line + "\n")
	}
	return title, out.String()
}

// describe is a page's first paragraph as plain text, cut to one or two
// sentences: the page's description for search engines and link previews.
func describe(body string) string {
	for _, para := range strings.Split(body, "\n\n") {
		para = strings.TrimSpace(para)
		if para == "" || strings.ContainsAny(para[:1], "#|<`>-*![0123456789") || strings.HasPrefix(para, "**") {
			continue
		}
		text := strings.Join(strings.Fields(para), " ")
		text = mdLink.ReplaceAllString(text, "$1")
		text = strings.NewReplacer("`", "", "**", "", "_", "").Replace(text)
		if len(text) > 200 {
			if i := strings.Index(text[100:], ". "); i >= 0 && i+101 < 240 {
				text = text[:i+101]
			} else {
				text = text[:197] + "…"
			}
		}
		return text
	}
	return ""
}

// copyImages copies docs/images into the images folder of the site's
// public folder, which it empties first. A link at images is removed, not
// followed.
func copyImages(docs string, public *os.Root) error {
	if err := public.RemoveAll("images"); err != nil {
		return fmt.Errorf("clear the images in %s: %w", public.Name(), err)
	}
	files, err := filepath.Glob(filepath.Join(docs, "images", "*"))
	if err != nil {
		return fmt.Errorf("list the images: %w", err)
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return fmt.Errorf("read %s: %w", f, err)
		}
		if err := writeIn(public, "images/"+filepath.Base(f), b); err != nil {
			return err
		}
	}
	return nil
}

// siteData writes the site's data files.
func siteData(dir string, pages map[string]string, nav []tab) error {
	type agent struct {
		Title   string `json:"title"`
		Kind    string `json:"kind"`
		Support string `json:"support"`
		Program string `json:"program"`
	}
	list := make([]agent, 0, len(agents.Table))
	for _, a := range agents.Table {
		list = append(list, agent{Title: a.Title, Kind: a.Kind, Support: string(a.Support), Program: a.Binaries[0]})
	}
	files := map[string]any{
		"pages":   pages,
		"agents":  list,
		"sidebar": nav,
		"brand": map[string]string{
			"name": brand.Name, "displayName": brand.DisplayName, "tagline": brand.Tagline,
			"hostBinary": brand.HostBinary, "envPrefix": brand.EnvPrefix,
			"description": siteDescription,
		},
	}
	for name, v := range files {
		b, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			return fmt.Errorf("encode %s: %w", name, err)
		}
		if err := writeFile(filepath.Join(dir, name+".json"), append(b, '\n')); err != nil {
			return err
		}
	}
	return nil
}

func writeFile(dst string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("make %s: %w", filepath.Dir(dst), err)
	}
	if err := os.WriteFile(dst, b, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", dst, err)
	}
	return nil
}
