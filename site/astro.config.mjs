// @ts-check
// The docs site: Astro with Starlight (decision 0022). Build it with
// `just docs-site`, which first writes the pages and data from docs/ into
// src/content/docs and src/generated (go run ./cmd/docgen -site site/src).
// The name and the rest of the brand come from src/generated/brand.json,
// which cmd/docgen writes from internal/brand, so no file here spells it.
//
// scripts/docs-site.sh sets these:
//   SITE_URL      the site's address, such as https://docs.example.org
//   SITE_BASE     where this build is served under it: / or /v1.2/ or /dev/
//   SITE_ROOT     the path of the site's root, which holds versions.json
//   DOCS_VERSION  this build's version: dev, or a release tag
//   SITE_VERSIONS 1 when the site has a version list (GitHub Pages)
//   REPO_URL      the repository on GitHub
import { readFileSync } from 'node:fs';
import { defineConfig, passthroughImageService } from 'astro/config';
import starlight from '@astrojs/starlight';
import { satteri } from '@astrojs/markdown-satteri';

const read = (f) => JSON.parse(readFileSync(new URL(f, import.meta.url), 'utf8'));
const brand = read('./src/generated/brand.json');
const tabs = read('./src/generated/sidebar.json');
const env = process.env;
const repo = env.REPO_URL || '';

export default defineConfig({
	site: env.SITE_URL || 'https://example.invalid',
	base: env.SITE_BASE || '/',
	trailingSlash: 'always',
	outDir: '../build/site/public',
	// No image is processed: the docs' images are SVG, served as they are.
	image: { service: passthroughImageService() },
	devToolbar: { enabled: false },
	// No smart punctuation: "--detach" must not become a dash, nor a quote
	// in a command change shape.
	markdown: { processor: satteri({ features: { smartPunctuation: false } }) },
	vite: {
		define: {
			// The build's facts, for the pages: src/site.d.ts describes them.
			__SITE__: JSON.stringify({
				version: env.DOCS_VERSION || 'dev',
				root: env.SITE_ROOT || env.SITE_BASE || '/',
				repo,
				versions: env.SITE_VERSIONS === '1',
			}),
		},
	},
	integrations: [
		starlight({
			title: brand.displayName,
			// cmd/docgen writes it, for llms.txt too.
			description: brand.description,
			locales: { root: { label: 'English', lang: 'en-GB' } },
			favicon: '/favicon.svg',
			social: repo ? [{ icon: 'github', label: 'Source on GitHub', href: repo }] : [],
			// One sidebar group per tab; src/route-data.js shows only the
			// current tab's group (src/generated/sidebar.json).
			sidebar: tabs.map((t) => ({ label: t.label, items: t.items })),
			routeMiddleware: './src/route-data.js',
			pagination: true,
			lastUpdated: false,
			credits: false,
			tableOfContents: { minHeadingLevel: 2, maxHeadingLevel: 3 },
			customCss: ['./src/styles/fonts.css', './src/styles/theme.css'],
			// Code blocks as rules, not windows: no title bar, no dots, no
			// shadow; the copy button stays.
			expressiveCode: {
				themes: ['github-dark-default', 'github-light-default'],
				defaultProps: { frame: 'code' },
				styleOverrides: {
					borderRadius: '8px',
					borderColor: 'var(--sl-color-hairline-light)',
					codeFontFamily: 'var(--sl-font-mono)',
					frames: { frameBoxShadowCssValue: 'none' },
				},
			},
			pagefind: { ranking: { termFrequency: 0.8, pageLength: 0.6 } },
			components: {
				Head: './src/components/overrides/Head.astro',
				Header: './src/components/overrides/Header.astro',
				Sidebar: './src/components/overrides/Sidebar.astro',
				PageTitle: './src/components/overrides/PageTitle.astro',
				ContentPanel: './src/components/overrides/ContentPanel.astro',
				MarkdownContent: './src/components/overrides/MarkdownContent.astro',
				EditLink: './src/components/overrides/EditLink.astro',
				Footer: './src/components/overrides/Footer.astro',
			},
		}),
	],
});
