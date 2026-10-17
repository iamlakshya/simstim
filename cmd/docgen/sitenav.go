package main

import (
	"bufio"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

// A tab of the site: a part of the docs with its own sidebar (decision
// 0022). Prefixes are the addresses that belong to it; Links maps a group's
// label to its index page, for the breadcrumbs.
type tab struct {
	Label    string            `json:"label"`
	Link     string            `json:"link"`
	Prefixes []string          `json:"prefixes"`
	Items    []item            `json:"items"`
	Links    map[string]string `json:"links"`
}

// item is an entry of a sidebar, in Starlight's form: a page (Slug) or a
// group (Items).
type item struct {
	Label     string `json:"label"`
	Slug      string `json:"slug,omitempty"`
	Items     []item `json:"items,omitempty"`
	Collapsed bool   `json:"collapsed,omitempty"`
}

// sidebar builds the three tabs. Their shape is here; the order and the
// labels of the pages come from each folder's README, so the README a
// reader sees on GitHub and the sidebar on the site agree.
func sidebar(docs string, pages map[string]string) ([]tab, error) {
	at := func(src, label string) item { return item{Label: label, Slug: strings.Trim(pages[src], "/")} }
	tutorials, err := readIndex(filepath.Join(docs, "tutorials", "README.md"))
	if err != nil {
		return nil, err
	}
	howto, err := readIndex(filepath.Join(docs, "how-to", "README.md"))
	if err != nil {
		return nil, err
	}
	concepts, err := readIndex(filepath.Join(docs, "explanation", "README.md"))
	if err != nil {
		return nil, err
	}
	start := append([]item{at("docs/how-to/install.md", "Install")}, tutorials.items("docs/tutorials", pages, nil)...)
	help := []item{at("docs/how-to/troubleshoot.md", "Troubleshoot"), at("docs/how-to/debug-a-session.md", "Debug a session"),
		at("docs/faq.md", "FAQ"), at("docs/how-to/uninstall.md", "Uninstall")}
	placed := map[string]bool{"install.md": true, "troubleshoot.md": true, "debug-a-session.md": true, "uninstall.md": true}
	var guides []item
	for _, sec := range howto.sections() {
		if g := howto.items("docs/how-to", pages, func(l link) bool { return l.section == sec && !placed[l.target] }); len(g) > 0 {
			guides = append(guides, item{Label: sec, Items: g})
		}
	}
	docsTab := tab{
		Label: "Docs", Link: "/docs/", Prefixes: []string{"/docs/", "/tutorials/", "/how-to/", "/explanation/", "/faq/"},
		Items: []item{
			at("docs/README.md", "Overview"),
			{Label: "Get started", Items: start},
			{Label: "Guides", Items: guides},
			{Label: "Concepts", Items: concepts.items("docs/explanation", pages, nil)},
			{Label: "Help", Items: help},
		},
		Links: map[string]string{"Get started": "/tutorials/", "Guides": "/how-to/", "Concepts": "/explanation/"},
	}

	refTab := tab{
		Label: "Reference", Link: "/reference/", Prefixes: []string{"/reference/"},
		Items: []item{
			at("docs/reference/_index.md", "Overview"),
			{Label: "Commands", Items: append([]item{at("docs/reference/cli/_index.md", "All commands")}, commandItems(root(), pages)...)},
			at("docs/reference/config.md", "Configuration"),
			at("docs/reference/keymap.md", "Keys"),
			at("docs/reference/agents.md", "Agents"),
			at("examples/providers/README.md", "Provider scripts"),
		},
		Links: map[string]string{"Commands": "/reference/cli/"},
	}

	guide, err := readIndex(filepath.Join(docs, "contributing", "README.md"))
	if err != nil {
		return nil, err
	}
	research, err := readIndex(filepath.Join(docs, "research", "README.md"))
	if err != nil {
		return nil, err
	}
	guideItems := guide.items("docs/contributing", pages, func(l link) bool {
		return !strings.HasPrefix(l.target, "decisions") && !strings.HasPrefix(l.target, "../")
	})
	contribute := append([]item{at("CONTRIBUTING.md", "How to contribute"), at("docs/contributing/README.md", "Contributor guide")}, guideItems...)
	projectTab := tab{
		Label: "Project", Link: "/project/", Prefixes: []string{"/project/", "/roadmap/", "/contributing/", "/research/"},
		Items: []item{
			at("docs/project/README.md", "Overview"),
			at("CHANGELOG.md", "Changelog"),
			at("docs/roadmap.md", "Roadmap"),
			at("SUPPORT.md", "Getting help"),
			at("SECURITY.md", "Security policy"),
			{Label: "Contribute", Items: contribute},
			{Label: "Decision records", Collapsed: true, Items: decisionItems(filepath.Join(docs, "contributing", "decisions"), pages)},
			{Label: "Research notes", Collapsed: true, Items: append([]item{at("docs/research/README.md", "All notes")}, research.items("docs/research", pages, nil)...)},
			{Label: "Community", Items: []item{at("GOVERNANCE.md", "Governance"), at("MAINTAINERS.md", "Maintainers"), at("CODE_OF_CONDUCT.md", "Code of conduct")}},
		},
		Links: map[string]string{"Contribute": "/contributing/", "Decision records": "/contributing/decisions/", "Research notes": "/research/"},
	}
	tabs := []tab{docsTab, refTab, projectTab}
	return tabs, checkSidebar(tabs, pages)
}

// checkSidebar fails when an item names a page the site does not have, so
// a renamed page cannot leave a hole in the sidebar.
func checkSidebar(tabs []tab, pages map[string]string) error {
	have := map[string]bool{}
	for _, url := range pages {
		have[strings.Trim(url, "/")] = true
	}
	var walk func([]item) error
	walk = func(items []item) error {
		for _, it := range items {
			if it.Items == nil && (it.Slug == "" || !have[it.Slug]) {
				return fmt.Errorf("the sidebar's %q names no page; Try: check the README that lists it", it.Label)
			}
			if err := walk(it.Items); err != nil {
				return err
			}
		}
		return nil
	}
	for _, t := range tabs {
		if err := walk(t.Items); err != nil {
			return err
		}
	}
	return nil
}

// commandItems is the command tree as sidebar items: a command with
// subcommands is a collapsed group that opens with its own page.
func commandItems(c *cobra.Command, pages map[string]string) []item {
	var out []item
	for _, sub := range visible(c) {
		slug := strings.Trim(pages["docs/reference/cli/"+pageName(sub.CommandPath())], "/")
		word := sub.Name()
		if len(visible(sub)) == 0 {
			out = append(out, item{Label: word, Slug: slug})
			continue
		}
		label := shortPath(sub)
		items := append([]item{{Label: label, Slug: slug}}, commandItems(sub, pages)...)
		out = append(out, item{Label: word, Collapsed: true, Items: items})
	}
	return out
}

// decisionItems lists the decision records, newest last, each by its number
// and the short part of its title.
func decisionItems(dir string, pages map[string]string) []item {
	out := []item{{Label: "All records", Slug: strings.Trim(pages["docs/contributing/decisions/README.md"], "/")}}
	files, _ := filepath.Glob(filepath.Join(dir, "[0-9][0-9][0-9][0-9]-*.md"))
	sort.Strings(files)
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		title, _ := splitTitle(string(b))
		if i := strings.IndexAny(title, ":,"); i > 0 {
			title = title[:i]
		}
		src := path.Join("docs/contributing/decisions", filepath.Base(f))
		out = append(out, item{Label: title, Slug: strings.Trim(pages[src], "/")})
	}
	return out
}

// link is one link of a README's index: its text, its target relative to
// the README, and the "## " heading it sits under.
type link struct{ label, target, section string }

type index []link

// readIndex reads the links of a README, in order.
func readIndex(file string) (index, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", file, err)
	}
	defer func() { _ = f.Close() }()
	var out index
	section := ""
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "## ") {
			section = strings.TrimSpace(line[3:])
			continue
		}
		for _, m := range mdLink.FindAllStringSubmatch(line, -1) {
			if strings.HasPrefix(m[0], "!") || strings.Contains(m[2], ":") || strings.HasPrefix(m[2], "#") {
				continue
			}
			out = append(out, link{label: strings.Trim(m[1], "`"), target: strings.SplitN(m[2], "#", 2)[0], section: section})
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", file, err)
	}
	return out, nil
}

// sections lists the README's "## " headings that hold links, in order.
func (ix index) sections() []string {
	var out []string
	for _, l := range ix {
		if l.section != "" && (len(out) == 0 || out[len(out)-1] != l.section) {
			out = append(out, l.section)
		}
	}
	return out
}

// items turns the README's links to pages in dir (a repository path) into
// sidebar items; keep, if given, picks which. Each page appears once, the
// first time it is linked; links to pages the site does not host, or
// outside dir's tree of pages, are skipped.
func (ix index) items(dir string, pages map[string]string, keep func(link) bool) []item {
	var out []item
	seen := map[string]bool{}
	for _, l := range ix {
		if keep != nil && !keep(l) {
			continue
		}
		src := path.Clean(path.Join(dir, l.target))
		url, ok := pages[src]
		if !ok || seen[src] || !strings.HasSuffix(src, ".md") {
			continue
		}
		seen[src] = true
		out = append(out, item{Label: l.label, Slug: strings.Trim(url, "/")})
	}
	return out
}
