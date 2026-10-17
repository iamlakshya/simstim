# How the docs work

The docs live in `docs/`, as plain Markdown that reads well on GitHub. The same files build the docs site.

## Where a page goes

The user docs follow [Diátaxis](https://diataxis.fr/): each page does one of four jobs.

| Folder | Job | Example |
| --- | --- | --- |
| `docs/tutorials/` | Teach by doing, start to finish | The first five minutes |
| `docs/how-to/` | Do one task | Add a host; get alerts |
| `docs/reference/` | Look something up. **Generated; never edit by hand.** | Every command and flag |
| `docs/explanation/` | Understand why | The fidelity contract |

Contributor docs are in `docs/contributing/`, the evidence behind the design in `docs/research/`, and what comes next in `docs/roadmap.md`.

## Docs ship with the change

A change that a user can notice updates its docs and adds a line to `CHANGELOG.md` under Unreleased, in the same commit. CI fails a pull request that changes commands, config, keys or agents without touching the docs and the CHANGELOG, unless it is labelled `no docs needed`.

## The generated reference

`just docs` runs `cmd/docgen`, which writes `docs/reference/` from the code:

- a page per command, from the command tree;
- `config.toml` and `hosts.toml`, from the comments on the settings structs in `internal/client`;
- the launcher's keys, from its command registry;
- the agent table, from `internal/agents`.

To change a reference page, change the code or its comment, then run `just docs`. CI fails when the committed pages differ from a fresh run. The release build also writes man pages and shell completions this way.

## The site

`just docs-site` builds the site into `build/site/public` with Astro and its docs framework, Starlight (`site/`). It needs Node and pnpm, at the versions `scripts/setup.sh` installs. [Decision 0022](decisions/0022-site-rebuild.md) has its design, [the research note](../research/website.md) the evidence behind it, and [the audit](../research/website-audit.md) the problems it fixed.

### How the pages get there

`go run ./cmd/docgen -site site/src` (the build runs it) turns the Markdown into the site's pages, in `site/src/content/docs/`, which git ignores:

- each `README.md` becomes its folder's page, each page's first heading its title, and its first paragraph its description;
- every link between two hosted files becomes a link to that page on the site, relative, so it works under any base (`/dev/`, `/v1.2/`). Markdown images become HTML images with relative URLs to the copied public assets, so Astro does not treat them as source imports. A link to any other file, such as code or a script, goes to its source on GitHub;
- the community files at the root of the repository (`CHANGELOG.md`, `CONTRIBUTING.md`, `SECURITY.md` and the rest) become pages of the Project tab.

It also writes `site/src/generated/`: `pages.json`, which maps each hosted file to its page; `sidebar.json`, the tabs; and `brand.json` and `agents.json` for the landing page. `just docs-check` fails if a built page links to GitHub for a page the site hosts (`scripts/check-site-links.py`).

### Tabs and the sidebar

The site has three tabs, each with its own sidebar: **Docs** (Overview, Get started, Guides, Concepts, Help), **Reference** (generated) and **Project** (the changelog, roadmap, contributor guide, decision records and research notes). `cmd/docgen/sitenav.go` sets their shape; **the order and the labels come from each folder's README**, in the order of its links and under its `##` headings. So to move a guide, move its row in `docs/how-to/README.md`; to add one, add a row. The build fails if the sidebar names a page that does not exist.

### Where things are

| Path | What |
| --- | --- |
| `site/astro.config.mjs` | Starlight's settings: the tabs, the overrides, search, the fonts |
| `site/src/route-data.js` | Shows the current tab's sidebar only, keeps previous and next inside it, and works out the breadcrumbs |
| `site/src/components/overrides/` | Starlight's parts we replace: the top bar, the sidebar, the title with its breadcrumbs, the footer, the source links |
| `site/src/pages/index.astro` | The landing page |
| `site/src/components/landing/` | The landing page's drawings: the hero's story (`HeroStory.astro`), the hosts, and the plates' corner marks |
| `site/src/scripts/` | The live launcher and the copy buttons |
| `site/src/styles/` | The look (`theme.css`), the fonts, and the landing page |
| `site/package.json`, `site/pnpm-lock.yaml` | The npm packages, at exact versions, with their hashes |

The name is never spelled in `site/`: pages take it from the site's title and from `src/generated/brand.json`, which `cmd/docgen` writes from `internal/brand`. `just check-name` covers the site's pages, scripts and styles.

### Third-party files

The site loads only its own files: no CDN, no web fonts from elsewhere, no analytics, and Astro's telemetry is off in every build. The npm packages are pinned in `site/pnpm-lock.yaml`; pnpm waits a week before it uses a new release, and runs no package's install script. The fonts are committed files: `scripts/site-vendor.sh` fetches them at pinned versions, checks each one's SHA-256, and cuts them down. Run it only to change a version, then commit the result.

| File | What | Licence |
| --- | --- | --- |
| `site/public/fonts/sans.woff2` | Inter 4, Latin, at its text optical size, for text | SIL OFL 1.1 |
| `site/public/fonts/display.woff2` | Schibsted Grotesk, Latin, at one weight, for headings | SIL OFL 1.1 |
| `site/public/fonts/mono.woff2` | JetBrains Mono 2.304, cut to the characters the site uses, for code and the launcher | SIL OFL 1.1 |
| `site/public/fonts/braille.woff2` | The 256 braille patterns, drawn by `scripts/site-fonts.py` on the monospace cell | Apache-2.0 |

### Motion

One thing moves by itself: the hero's story, about 14 s, played once, then held on its last frame. It plays only while it is on screen and the tab is visible. The live launcher moves only while a reader points at it or gives it focus. There is no motion switch on the site: motion follows the reader's reduced-motion setting, and with it set, the story stands still on one frame that tells it (the lid shut, the agents still working). [Decision 0022](decisions/0022-site-rebuild.md) records why there is no pause, and what that costs.

The story is inline SVG. Every moving part reads its times from one table in `HeroStory.astro`, and the build writes the CSS keyframes from it; only `transform` and `opacity` move, so the browser runs it off the main thread. To change the story, change the table. Keep the rest that way for anything new: draw the still frame first, in the HTML, and add motion on top.

### Versions

CI builds the site on every push and keeps it as an artifact named `docs-site`. Once the repository is public, GitHub Pages serves the latest release at the root, each release at `/vX.Y/`, and `main` at `/dev/` (`scripts/pages-assemble.sh`). Each is built on its own, with its own base and its own search index. The version menu in the top bar reads `versions.json` at the root, so an old version lists the newer ones too.

## Checks

`just docs-check` runs what CI runs:

- the generated reference is current;
- Markdown lint (markdownlint-cli2; `.markdownlint-cli2.yaml`);
- British spelling (typos; `.typos.toml`);
- plain English (Vale, with the project's small style in `.vale/styles/Plain`);
- every command in the tutorials and how-to guides: each must exist with its flags, and with `DOCS_HOST` set, `just test-docs` runs them on a test host (a comment before a block can mark it `skip` or `help`);
- every link on the built site (lychee, offline). `just docs-check --external` also checks links to other sites, which CI does on `main` and weekly;
- no built page links to GitHub for a page the site hosts.

## Images

Screenshots and recordings come from scripts, never by hand. `just screenshots` runs the `demo` command, where nothing connects anywhere, inside `test/ttyshot`:

- the stills in `docs/images/*.svg` come from the shot list in `scripts/screenshots.sh`;
- the recorded demos come from the tapes in `docs/demos/*.tape`, with motion on. Each tape is a script of keys and pauses (`send`, `type`, `sleep`, `wait`) between `record start` and `record save`, which writes an animated SVG (it stands still for readers who ask for reduced motion) and an asciicast (`.cast`) for asciinema's player.

Run it after any change to the launcher, so the images always show the current build.

### Architecture diagram

The architecture diagram is separate from the terminal recordings. Its editable Mermaid source is `docs/images/architecture.mmd`; the committed SVG renders on GitHub and the site without client-side JavaScript. It includes an accessible title and description, and uses SVG text rather than HTML labels.

With Node, npm and a [Puppeteer-compatible browser](https://pptr.dev/guides/configuration) available, regenerate it with the pinned [Mermaid CLI](https://github.com/mermaid-js/mermaid-cli):

```sh
npx --yes --package=@mermaid-js/mermaid-cli@12.0.0 mmdc \
  -i docs/images/architecture.mmd -o docs/images/architecture.svg -b white
```

Review the rendered SVG, not just its source, before committing it. Keep the source and generated image together. The site's normal build copies the image; it does not need Mermaid or a browser.
