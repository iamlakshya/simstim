#!/usr/bin/env python3
"""Checks the docs for coding agents in a built docs site (the CLI spec,
section 16.3): llms.txt and the Markdown copy of each page, which
cmd/docgen -site writes.

    scripts/check-llms-txt.py build/site/public site/src/generated/pages.json

Run it from the root of the repository: it reads each page's source there,
and the repository's address from go.mod. The build's address comes from
llms.txt itself: its first item is the landing page's copy, index.md. It
fails when:

- llms.txt misses a page of pages.json, lists a page twice, or lists a page
  without a description.
- a copy that llms.txt names is missing from the build.
- a copy does not match its source. Its title must be the source's title
  (the front matter's title, or else the first heading). Its text must be
  the source's text, with code exactly as it is. Each link that cmd/docgen
  rewrites must lead where cmd/docgen sends the source's link: to the copy
  of a page, to an image, or to the file on GitHub.
- a link in a copy is relative but leads to no Markdown copy or image in
  the build: a page's HTML address, or a file that does not exist.
"""

import json
import os
import posixpath
import re
import sys

ITEM = re.compile(r"^- \[((?:\\.|[^\]\\])*)\]\((\S+)\)(?:: (.*))?$")
# The links that cmd/docgen rewrites (cmd/docgen/sitelinks.go): [text](url)
# and ![alt](url), [id]: url, and href in an HTML <a>.
LINK = re.compile(r'(!?\[(?:[^\[\]]|\[[^\[\]]*\])*\])\(([^()\s]+)((?:\s+"[^"]*")?)\)')
DEF = re.compile(r"^(\s{0,3}\[[^\]]+\]:\s*)(\S+)(.*)$")
A_HREF = re.compile(r'(<a\s[^>]*href=")([^"]+)(")')
HREF = re.compile(r'(<(?:a\s[^>]*href|img\s[^>]*src)=")([^"]+)(")')
SPAN = re.compile(r"(`+).*?\1")
SCHEME = re.compile(r"^[A-Za-z][A-Za-z0-9+.-]*:")
MARK = "\x00"  # stands for a link's destination in a skeleton


def copy_of(url):
    """The path of a page's copy: its address without the slash, plus .md."""
    return "index.md" if url == "/" else url.strip("/") + ".md"


def code_spans(line):
    """The code spans of a line, as (start, end) pairs, as cmd/docgen finds
    them: a run of backticks up to the next run of the same length."""
    out, i = [], 0
    while i < len(line):
        if line[i] != "`":
            i += 1
            continue
        n = 0
        while i + n < len(line) and line[i + n] == "`":
            n += 1
        end, j = -1, i + n
        while j <= len(line) - n:
            if line[j:j + n] == "`" * n and (j + n == len(line) or line[j + n] != "`") and line[j - 1] != "`":
                end = j + n
                break
            j += 1
        if end < 0:
            i += n
            continue
        out.append((i, end))
        i = end
    return out


def mark(line, rx, dests):
    """The line with the destination of each match of rx outside code
    replaced by MARK, and the destinations added to dests."""
    spans = code_spans(line)
    out, at = [], 0
    for m in rx.finditer(line):
        if any(a <= m.start() < b for a, b in spans):
            continue
        out += [line[at:m.start(2)], MARK]
        dests.append(m.group(2))
        at = m.end(2)
    return "".join(out) + line[at:]


def skeleton(text):
    """The text with each link destination that cmd/docgen rewrites replaced
    by MARK, and those destinations in order. Code stays as it is."""
    out, dests, fence = [], [], ""
    for line in text.split("\n"):
        trimmed = line.lstrip(" ")
        if not fence and trimmed.startswith(("```", "~~~")):
            fence = trimmed[:3]
        elif fence:
            if trimmed.startswith(fence):
                fence = ""
        elif m := DEF.match(line):
            dests.append(m.group(2))
            line = m.group(1) + MARK + m.group(3)
        else:
            line = mark(mark(line, LINK, dests), A_HREF, dests)
        out.append(line)
    return "\n".join(out), dests


def expected(dest, src, rel, pages, repo):
    """Where cmd/docgen sends the link dest of the source src in its copy
    rel (resolve in cmd/docgen/sitelinks.go): to a page's copy, to an image,
    or to the file on GitHub."""
    if not dest or dest.startswith(("#", "/")) or ":" in dest:
        return dest
    target, _, frag = dest.partition("#")
    frag = "#" + frag if frag else ""
    path = posixpath.normpath(posixpath.join(posixpath.dirname(src), target))
    here = posixpath.dirname("/" + rel)
    if path in pages:
        return posixpath.relpath("/" + copy_of(pages[path]), here) + frag
    if path.startswith("docs/images/"):
        return posixpath.relpath("/images/" + posixpath.basename(path), here) + frag
    kind = "tree" if target.endswith("/") or os.path.isdir(path) else "blob"
    return f"{repo}/{kind}/main/{path}{frag}"


def read_repo():
    """The repository's address on GitHub, from go.mod's module path."""
    with open("go.mod", encoding="utf-8") as f:
        for line in f:
            if line.startswith("module "):
                return "https://" + line.split()[1]
    return ""


def unquote(value):
    """A front matter value, as cmd/docgen's quote writes it, or a plain one."""
    value = value.strip()
    if value.startswith('"'):
        try:
            return json.loads(value)
        except ValueError:
            pass
    return value.strip("\"'")


def split(source):
    """A source page as its copy holds it: its title, from the front matter
    or else the first heading, and its body, without either."""
    title = None
    if source.startswith("---\n"):
        end = source.find("\n---\n", 4)
        if end >= 0:
            for line in source[4:end].split("\n"):
                if line.startswith("title:"):
                    title = unquote(line[len("title:"):])
            source = source[end + 5:]
    out, fence, heading = [], False, None
    for line in source.split("\n"):
        if line.startswith("```"):
            fence = not fence
        if heading is None and not fence and line.startswith("# "):
            heading = line[2:].strip()
            continue
        out.append(line)
    # An empty title in the front matter gives way to the heading, as in cmd/docgen.
    return (title or heading or ""), "\n".join(out).lstrip("\n").rstrip("\n")


def links(text):
    """Each link destination of a Markdown text, outside code."""
    fence = ""
    for line in text.split("\n"):
        trimmed = line.lstrip(" ")
        if not fence and trimmed.startswith(("```", "~~~")):
            fence = trimmed[:3]
            continue
        if fence:
            if trimmed.startswith(fence):
                fence = ""
            continue
        line = SPAN.sub("", line)
        m = DEF.match(line)
        if m:
            yield m.group(2)
            continue
        for m in LINK.finditer(line):
            yield m.group(2)
        for m in HREF.finditer(line):
            yield m.group(2)


def bad_link(public, rel, dest):
    """Why a link of the copy rel goes nowhere a copy may lead, or None."""
    if dest.startswith("#") or SCHEME.match(dest):
        return None  # a part of the page, or another site
    target = dest.split("#", 1)[0].split("?", 1)[0]
    if target.startswith("/"):
        path = posixpath.normpath(target.lstrip("/"))
    else:
        path = posixpath.normpath(posixpath.join(posixpath.dirname(rel), target))
    if path.startswith("../") or path == "..":
        return f"leads out of the build: {dest}"
    if not (path.endswith(".md") or path.startswith("images/")):
        return f"leads to {dest}, which is not a Markdown copy or an image"
    if not os.path.isfile(os.path.join(public, path)):
        return f"leads to {dest}, which does not exist"
    return None


def read_llms(public, bad):
    """The copies that llms.txt lists, each with its line."""
    with open(os.path.join(public, "llms.txt"), encoding="utf-8") as f:
        lines = f.read().split("\n")
    items = [(n, ITEM.match(line), line) for n, line in enumerate(lines, 1) if line.startswith("- ")]
    if not items or not items[0][1] or not items[0][1].group(2).endswith("/index.md"):
        bad.append("llms.txt: the first item is not the landing page's copy, index.md")
        return {}
    site = items[0][1].group(2)[: -len("index.md")]
    listed = {}
    for n, m, line in items:
        if not m or not m.group(2).startswith(site):
            bad.append(f"llms.txt:{n}: not a link to a copy under {site}: {line}")
            continue
        rel = m.group(2)[len(site):]
        if not (m.group(3) or "").strip():
            bad.append(f"llms.txt:{n}: {rel} has no description")
        if rel in listed:
            bad.append(f"llms.txt:{n}: {rel} is listed twice (first on line {listed[rel]})")
        listed.setdefault(rel, n)
        if not os.path.isfile(os.path.join(public, rel)):
            bad.append(f"llms.txt:{n}: the copy {rel} is missing from {public}")
    return listed


def check_copy(public, src, rel, pages, repo, bad):
    """The copy rel against its source src."""
    with open(os.path.join(public, rel), encoding="utf-8") as f:
        text = f.read()
    for dest in links(text):
        why = bad_link(public, rel, dest)
        if why:
            bad.append(f"{rel}: a link {why}")
    if not src:
        return  # a generated page: no source file to compare with
    with open(src, encoding="utf-8") as f:
        title, body = split(f.read())
    head, _, got = text.partition("\n\n")
    if head != "# " + title:
        bad.append(f"{rel}: the title {head!r} is not {src}'s title {'# ' + title!r}")
        return
    want, want_dests = skeleton(body)
    have, have_dests = skeleton(got.rstrip("\n"))
    if have != want:
        a, b = have.split("\n"), want.split("\n")
        n = next((i for i, (x, y) in enumerate(zip(a, b)) if x != y), min(len(a), len(b)))
        bad.append(f"{rel} does not match its source {src}, from line {n + 3} of the copy")
        return
    for dest, got_dest in zip(want_dests, have_dests):
        want_dest = expected(dest, src, rel, pages, repo)
        if got_dest != want_dest:
            bad.append(f"{rel}: the link to {dest} in {src} leads to {got_dest}, want {want_dest}")


def main(public, pages_json):
    with open(pages_json, encoding="utf-8") as f:
        pages = json.load(f)
    if not os.path.isfile(os.path.join(public, "llms.txt")):
        print(f"{public}/llms.txt is missing.\nTry: just docs-site, which writes it through cmd/docgen -site.")
        return 1
    if not os.path.isfile("go.mod"):
        print("go.mod is missing.\nTry: run the check from the root of the repository.")
        return 1
    repo = read_repo()
    bad = []
    listed = read_llms(public, bad)
    # Each copy once, with its source file: pages.json also maps a folder
    # to its README's page, and names generated pages that have no file.
    sources = {}
    for src, url in sorted(pages.items()):
        rel = copy_of(url)
        if os.path.isfile(src) or rel not in sources:
            sources[rel] = (src if os.path.isfile(src) else "", url)
    for rel, (src, url) in sorted(sources.items()):
        if listed and rel not in listed:
            bad.append(f"llms.txt misses {url} ({src or 'a generated page'})")
        if os.path.isfile(os.path.join(public, rel)):
            check_copy(public, src, rel, pages, repo, bad)
    for line in bad:
        print(line)
    if bad:
        print(f"\nError: {len(bad)} problem(s) with llms.txt or the Markdown copies.")
        print("Try: just docs-site, which writes llms.txt and the copies again. A page with no")
        print("description needs a first paragraph of plain text, before its first table or list.")
        return 1
    print(f"check-llms-txt: ok ({len(listed)} pages, each with a description and a copy that matches its source)")
    return 0


if __name__ == "__main__":
    if len(sys.argv) != 3:
        print(__doc__.strip(), file=sys.stderr)
        sys.exit(2)
    sys.exit(main(*sys.argv[1:]))
