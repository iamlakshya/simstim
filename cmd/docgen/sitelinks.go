package main

import (
	"html"
	"os"
	"path"
	"regexp"
	"strings"
)

var (
	// [text](url) and ![alt](url), with an optional title. The text may hold
	// one level of brackets, as in [`[keymap]`](config.md).
	mdLink = regexp.MustCompile(`!?\[((?:[^\[\]]|\[[^\[\]]*\])*)\]\(([^()\s]+)((?:\s+"[^"]*")?)\)`)
	// [id]: url, a link reference definition.
	mdDef = regexp.MustCompile(`^(\s{0,3}\[[^\]]+\]:\s*)(\S+)(.*)$`)
	// href="url" in the odd HTML link.
	htmlHref = regexp.MustCompile(`(<a\s[^>]*href=")([^"]+)(")`)
)

// rewriteLinks makes every relative link in a page's Markdown work on the
// site. src is the page's path in the repository and url its address.
//   - A link to a file the site hosts goes to its page, as a path relative
//     to this page, so it works under any base (/dev/, /v1.2/).
//   - A link to an image in docs/images goes to the site's copy.
//   - A link to any other file goes to its source on GitHub.
//
// Links in code blocks and code spans are left alone.
func rewriteLinks(md, src, url string, pages map[string]string, repo string) string {
	fix := func(dest string) string { return resolve(dest, src, url, pages, repo) }
	var out strings.Builder
	fence := ""
	for _, line := range strings.SplitAfter(md, "\n") {
		trimmed := strings.TrimLeft(line, " ")
		if fence == "" && (strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~")) {
			fence = trimmed[:3]
			out.WriteString(line)
			continue
		}
		if fence != "" {
			if strings.HasPrefix(trimmed, fence) {
				fence = ""
			}
			out.WriteString(line)
			continue
		}
		if m := mdDef.FindStringSubmatch(strings.TrimRight(line, "\n")); m != nil {
			out.WriteString(m[1] + fix(m[2]) + m[3] + line[len(strings.TrimRight(line, "\n")):])
			continue
		}
		out.WriteString(rewriteLine(line, fix))
	}
	return out.String()
}

// rewriteLine rewrites the links of one line of text. A link in a code span
// is left alone; a link whose text holds a code span, as in
// [`host add`](host_add.md), is a link.
func rewriteLine(line string, fix func(string) string) string {
	spans := codeSpans(line)
	inCode := func(i int) bool {
		for _, sp := range spans {
			if i >= sp[0] && i < sp[1] {
				return true
			}
		}
		return false
	}
	var b strings.Builder
	at := 0
	for _, m := range mdLink.FindAllStringSubmatchIndex(line, -1) {
		if inCode(m[0]) {
			continue
		}
		if line[m[0]] == '!' {
			// Relative Markdown images are source imports to Astro, not
			// URLs into public/. HTML keeps the page-relative asset URL,
			// including when the site is served under a version prefix.
			b.WriteString(line[at:m[0]])
			b.WriteString(`<img src="` + html.EscapeString(fix(line[m[4]:m[5]])) + `" alt="` + html.EscapeString(line[m[2]:m[3]]) + `"`)
			if title := strings.TrimSpace(line[m[6]:m[7]]); title != "" {
				b.WriteString(` title="` + html.EscapeString(strings.Trim(title, `"`)) + `"`)
			}
			b.WriteString(">")
			at = m[1]
			continue
		}
		b.WriteString(line[at:m[4]] + fix(line[m[4]:m[5]]))
		at = m[5]
	}
	b.WriteString(line[at:])
	line = b.String()
	spans = codeSpans(line)
	b.Reset()
	at = 0
	for _, m := range htmlHref.FindAllStringSubmatchIndex(line, -1) {
		if inCode(m[0]) {
			continue
		}
		b.WriteString(line[at:m[4]] + fix(line[m[4]:m[5]]))
		at = m[5]
	}
	b.WriteString(line[at:])
	return b.String()
}

// codeSpans finds the code spans of a line, as [start, end) pairs: a run of
// backticks up to the next run of the same length.
func codeSpans(line string) [][2]int {
	var out [][2]int
	for i := 0; i < len(line); {
		if line[i] != '`' {
			i++
			continue
		}
		n := 0
		for i+n < len(line) && line[i+n] == '`' {
			n++
		}
		closer := strings.Repeat("`", n)
		end := -1
		for j := i + n; j <= len(line)-n; j++ {
			if line[j:j+n] == closer && (j+n == len(line) || line[j+n] != '`') && line[j-1] != '`' {
				end = j + n
				break
			}
		}
		if end < 0 {
			i += n
			continue
		}
		out = append(out, [2]int{i, end})
		i = end
	}
	return out
}

// resolve turns one link destination into its address on the site.
func resolve(dest, src, url string, pages map[string]string, repo string) string {
	if dest == "" || strings.HasPrefix(dest, "#") || strings.HasPrefix(dest, "/") || strings.Contains(dest, ":") {
		return dest // a fragment, an absolute path, or a link with a scheme
	}
	target, frag, _ := strings.Cut(dest, "#")
	if frag != "" {
		frag = "#" + frag
	}
	repoPath := path.Clean(path.Join(path.Dir(src), target))
	if page, ok := pages[repoPath]; ok {
		return relURL(url, page) + frag
	}
	if strings.HasPrefix(repoPath, "docs/images/") {
		return relURL(url, "/images/"+path.Base(repoPath)) + frag
	}
	kind := "blob"
	if info, err := os.Stat(repoPath); strings.HasSuffix(target, "/") || err == nil && info.IsDir() {
		kind = "tree"
	}
	return repo + "/" + kind + "/main/" + repoPath + frag
}

// relURL is the path from the page at from to the address to, both
// absolute, as a relative link. Page addresses end in a slash.
func relURL(from, to string) string {
	f := strings.Split(strings.Trim(from, "/"), "/")
	if from == "/" {
		f = nil
	}
	t := strings.Split(strings.Trim(to, "/"), "/")
	if to == "/" {
		t = nil
	}
	i := 0
	for i < len(f) && i < len(t) && f[i] == t[i] {
		i++
	}
	rel := strings.Repeat("../", len(f)-i) + strings.Join(t[i:], "/")
	if strings.HasSuffix(to, "/") && len(t) > i {
		rel += "/"
	}
	if rel == "" {
		return "./"
	}
	return rel
}
