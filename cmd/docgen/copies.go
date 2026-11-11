package main

// Docs for coding agents (the CLI spec, section 16): a Markdown copy of each
// page of the site, and llms.txt, which lists the copies in the format of
// llmstxt.org. Both go into the site's public folder, which Astro copies
// into the build as it is.

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/iamlakshya/simstim/internal/brand"
)

// copyURL is the address of a page's Markdown copy: the page's address
// without its trailing slash, plus .md. The copy of / is /index.md.
func copyURL(url string) string {
	if url == "/" {
		return "/index.md"
	}
	return strings.TrimSuffix(url, "/") + ".md"
}

// copyMap maps each hosted file, as pages does, to its page's copy.
func copyMap(pages map[string]string) map[string]string {
	copies := make(map[string]string, len(pages))
	for src, url := range pages {
		copies[src] = copyURL(url)
	}
	return copies
}

// alternate is the item of a page's head list that links the page to its
// Markdown copy, as llmstxt.org proposes, at the list's indent ind.
func alternate(url, ind string) string {
	return ind + "- tag: link\n" + ind + "  attrs:\n" + ind + "    rel: alternate\n" +
		ind + "    type: text/markdown\n" + ind + "    href: " + quote(relURL(url, copyURL(url))) + "\n"
}

// joinHead adds the link to the page's copy to the head list that the
// page's own front matter (keep) has, at that list's indent, and says
// whether it found one. Two head keys would make the front matter invalid.
// The head key may carry a comment, and the list may hold blank lines and
// comments, at the left margin too: none of them ends the list.
func joinHead(keep []string, url string) ([]string, bool, error) {
	for i, l := range keep {
		key, rest, _ := strings.Cut(l, ":")
		if key != "head" {
			continue
		}
		head := l // the key, with its comment
		switch yamlValue(rest) {
		case "":
		case "[]":
			head = "head:"
		default:
			return nil, false, errors.New("its head front matter is not a block list. Try: write head as a list of items, one \"- tag:\" each")
		}
		end := i + 1 // just after the list's last line that is not blank or a comment
		for j := i + 1; j < len(keep); j++ {
			if t := strings.TrimSpace(keep[j]); t == "" || strings.HasPrefix(t, "#") {
				continue
			}
			if !strings.HasPrefix(keep[j], " ") && !strings.HasPrefix(keep[j], "-") {
				break // the next key
			}
			end = j + 1
		}
		ind := "  "
		for _, item := range keep[i+1 : end] {
			if t := strings.TrimLeft(item, " "); strings.HasPrefix(t, "- ") || t == "-" {
				ind = item[:len(item)-len(t)]
				break
			}
		}
		out := append(append([]string{}, keep[:i]...), head)
		out = append(out, keep[i+1:end]...)
		out = append(out, strings.Split(strings.TrimSuffix(alternate(url, ind), "\n"), "\n")...)
		return append(out, keep[end:]...), true, nil
	}
	return keep, false, nil
}

// yamlValue is a YAML value as a line of front matter has it, without its
// comment and spaces.
func yamlValue(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "#") {
		return ""
	}
	if i := strings.Index(s, " #"); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	return s
}

// openPublic opens the site's public folder as a root. Everything that
// cmd/docgen writes there (the images, the copies and llms.txt) goes
// through it, so no write can follow a symbolic link out of the folder. It
// makes the folder when it is missing, and refuses one that is a link.
func openPublic(public string) (*os.Root, error) {
	if err := os.MkdirAll(public, 0o755); err != nil {
		return nil, fmt.Errorf("make %s: %w", public, err)
	}
	info, err := os.Lstat(public)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", public, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is a symbolic link, not a folder, so nothing was written into it. Try: make it a plain folder", public)
	}
	root, err := os.OpenRoot(public)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", public, err)
	}
	return root, nil
}

// writeIn writes b to rel, a slash path in the root public. A symbolic
// link at rel is removed first, so the write makes a new file and never
// follows the link. The root refuses a linked folder on the way that leads
// out of it.
func writeIn(public *os.Root, rel string, b []byte) error {
	name := filepath.FromSlash(rel)
	if dir := filepath.Dir(name); dir != "." {
		if err := public.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("make %s in %s: %w. Try: remove the symbolic link on that path, so that its folders are plain", dir, public.Name(), err)
		}
	}
	if info, err := public.Lstat(name); err == nil && info.Mode()&fs.ModeSymlink != 0 {
		if err := public.Remove(name); err != nil {
			return fmt.Errorf("remove the symbolic link %s in %s: %w", name, public.Name(), err)
		}
	}
	if err := public.WriteFile(name, b, 0o644); err != nil {
		return fmt.Errorf("write %s in %s: %w. Try: remove the symbolic links on that path, then build again", name, public.Name(), err)
	}
	return nil
}

// clearCopies removes what writeCopies wrote last time: llms.txt and every
// Markdown file in the root public, which git ignores (.gitignore). It
// removes only regular files, and never follows a symbolic link: a link is
// left as it is, and writeIn removes one that stands where a copy goes.
func clearCopies(public *os.Root) error {
	err := fs.WalkDir(public.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// WalkDir does not go into a linked folder: its entry is a link.
		if d.Type().IsRegular() && (strings.HasSuffix(p, ".md") || p == "llms.txt") {
			return public.Remove(filepath.FromSlash(p))
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("clear the Markdown copies in %s: %w", public.Name(), err)
	}
	return nil
}

// entry is one page in llms.txt: its title and its description, as the
// site's page has them.
type entry struct{ title, desc string }

// writeCopies writes the Markdown copy of each page into the root public,
// and then llms.txt, which links each copy at siteURL, the address of this
// build. A copy is "# " and the page's title, a blank line, then the page's
// body without front matter. Its links to other pages go to their copies,
// and its images stay Markdown images.
func writeCopies(public *os.Root, pages []page, entries map[string]entry, byURL map[string]string, nav []tab, repo, siteURL string) error {
	copies := copyMap(byURL)
	for _, p := range pages {
		v := viewOf(copyLinks(p.md, p.src, p.url, copies, repo))
		text := "# " + v.title + "\n\n" + strings.TrimLeft(v.body, "\n")
		if err := writeIn(public, strings.TrimPrefix(copyURL(p.url), "/"), []byte(text)); err != nil {
			return err
		}
	}
	return writeIn(public, "llms.txt", llmsTxt(nav, entries, siteURL))
}

// llmsTxt is the site's llms.txt: the name, the site's description, how to
// read the pages, then a section for each tab, in the sidebar's order, with
// the section "Optional" last. Each page is listed once, as a link to its
// copy with its description. The landing page comes first, so a reader
// (and scripts/check-llms-txt.py) finds the build's address in it.
func llmsTxt(nav []tab, entries map[string]entry, siteURL string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n> %s\n\n", brand.DisplayName, siteDescription)
	fmt.Fprintf(&b, "Each link below goes to the Markdown copy of a page. Every page of the site has one, at its address without the trailing slash, plus `.md`. `%s <command> --help` prints a command's description, usage, examples and flags, as its page under Reference does.\n", brand.Name)
	base := strings.TrimSuffix(siteURL, "/")
	esc := strings.NewReplacer(`\`, `\\`, "[", `\[`, "]", `\]`)
	for _, sec := range sections(nav, entries) {
		fmt.Fprintf(&b, "\n## %s\n\n", sec.name)
		for _, url := range sec.urls {
			e := entries[url]
			fmt.Fprintf(&b, "- [%s](%s)", esc.Replace(e.title), base+copyURL(url))
			if e.desc != "" {
				b.WriteString(": " + e.desc)
			}
			b.WriteString("\n")
		}
	}
	return []byte(b.String())
}

// section is one H2 of llms.txt, with its pages' addresses in order.
type section struct {
	name     string
	optional bool
	urls     []string
}

// sections puts each page of entries in one section: first in the order
// of the sidebars, then, for a page that no sidebar lists, in the tab whose
// addresses it shares, in the order of its address. A page that shares no
// tab's addresses goes to the first tab. The optional tab is the section
// "Optional", last whatever the order of the tabs: the contributing docs,
// the research notes and the project pages. The landing page goes at the
// top of the first section. Empty sections are left out.
func sections(nav []tab, entries map[string]entry) []section {
	if len(nav) == 0 {
		return nil
	}
	out := make([]section, len(nav))
	placed := map[string]bool{"/": true} // the landing page, placed last of all
	for i, t := range nav {
		out[i].name, out[i].optional = t.Label, t.optional
		if t.optional {
			out[i].name = "Optional"
		}
		var walk func([]item)
		walk = func(items []item) {
			for _, it := range items {
				url := "/" + it.Slug + "/"
				if _, ok := entries[url]; ok && it.Slug != "" && !placed[url] {
					placed[url] = true
					out[i].urls = append(out[i].urls, url)
				}
				walk(it.Items)
			}
		}
		walk(t.Items)
	}
	rest := make([]string, 0, len(entries))
	for url := range entries {
		if !placed[url] {
			rest = append(rest, url)
		}
	}
	sort.Strings(rest)
	for _, url := range rest {
		i := max(tabOf(nav, url), 0)
		out[i].urls = append(out[i].urls, url)
	}
	sort.SliceStable(out, func(a, b int) bool { return !out[a].optional && out[b].optional })
	if _, ok := entries["/"]; ok {
		out[0].urls = append([]string{"/"}, out[0].urls...)
	}
	kept := out[:0]
	for _, s := range out {
		if len(s.urls) > 0 {
			kept = append(kept, s)
		}
	}
	return kept
}

// tabOf is the index of the tab whose addresses url shares, or -1.
func tabOf(nav []tab, url string) int {
	for i, t := range nav {
		for _, p := range t.Prefixes {
			if strings.HasPrefix(url, p) {
				return i
			}
		}
	}
	return -1
}

// unquote reads a front matter value written by quote, or a plain one.
func unquote(s string) string {
	if u, err := strconv.Unquote(s); err == nil {
		return u
	}
	return strings.Trim(s, `"'`)
}
