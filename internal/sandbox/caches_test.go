package sandbox

import (
	"slices"
	"strings"
	"testing"
)

func TestDefaultCachesTable(t *testing.T) {
	entries, err := DefaultCaches()
	if err != nil {
		t.Fatalf("DefaultCaches: %v", err)
	}
	wantPaths := []string{
		".npm/_cacache",
		".local/share/pnpm/store",
		".cache/pnpm",
		".yarn/berry/cache",
		".cache/pip",
		".cache/uv",
		".cargo/registry",
		".cargo/git",
		"go/pkg/mod",
		".cache/go-build",
		".gradle/caches",
		".m2/repository",
	}
	if len(entries) != len(wantPaths) {
		t.Fatalf("len(DefaultCaches) = %d, want %d", len(entries), len(wantPaths))
	}
	got := DefaultCachePaths()
	if !slices.Equal(got, wantPaths) {
		t.Fatalf("DefaultCachePaths = %v, want %v", got, wantPaths)
	}
	for _, e := range entries {
		if e.Tool == "" {
			t.Errorf("entry %q missing tool", e.Path)
		}
		if !strings.HasPrefix(e.Source, "https://") {
			t.Errorf("entry %q source = %q, want https:// URL", e.Path, e.Source)
		}
		if e.Notes == "" {
			t.Errorf("entry %q missing notes", e.Path)
		}
	}
	if slices.Contains(got, ".cargo/bin") {
		t.Fatal("DefaultCachePaths must never include .cargo/bin")
	}
}

func TestValidateCachePathRefusals(t *testing.T) {
	allowed := []string{
		".npm/_cacache",
		".local/share/pnpm/store",
		".cache/my-tool",
		".cache/go-build/",
		"go/pkg/mod",
		".cargo/registry",
		".m2/repository",
	}
	for _, p := range allowed {
		clean, err := ValidateCachePath(p)
		if err != nil {
			t.Errorf("ValidateCachePath(%q) = %v, want nil", p, err)
		}
		if clean == "" || strings.HasSuffix(clean, "/") {
			t.Errorf("ValidateCachePath(%q) returned uncleaned %q", p, clean)
		}
	}

	refused := []string{
		"",
		"   ",
		".",
		"..",
		"../etc",
		"a/../b",
		"/tmp/cache",
		"~/.npm/_cacache",
		".cache\\pip",
		".cache/pip\x00x",
		" .cache/pip",
		".cargo/bin",
		".cargo/bin/sub",
		".cargo",
		"go/bin",
		"go",
		".local/bin",
		".local",
		"bin",
		"foo/bin",
		"foo/sbin",
		".pyenv/shims",
		".yarn/bin",
		".yarn",
		".npm-global",
		".rustup",
		".bashrc",
		".profile",
		".bash_profile",
		".bash_login",
		".zshrc",
		".zprofile",
		".zshenv",
		".claude/settings.json",
		".gitconfig",
		".config/git",
		".config",
		".ssh",
		".ssh/known_hosts",
		".gnupg",
		".aws",
		".kube",
		".docker",
		".npmrc",
		".cargo/credentials.toml",
	}
	for _, p := range refused {
		if _, err := ValidateCachePath(p); err == nil {
			t.Errorf("ValidateCachePath(%q) succeeded, want refusal", p)
		} else if !strings.Contains(err.Error(), "Try:") {
			t.Errorf("ValidateCachePath(%q) error missing Try: hint: %v", p, err)
		}
	}
}

func TestResolveCachesLayers(t *testing.T) {
	// Default: keep=true with no overrides yields all 11 built-in caches.
	got, err := ResolveCaches(true)
	if err != nil {
		t.Fatalf("ResolveCaches(true): %v", err)
	}
	if !slices.Equal(got, DefaultCachePaths()) {
		t.Fatalf("ResolveCaches(true) = %v", got)
	}

	// keep=false returns nil.
	off, err := ResolveCaches(false, []string{".cache/extra"})
	if err != nil {
		t.Fatalf("ResolveCaches(false): %v", err)
	}
	if len(off) != 0 {
		t.Fatalf("ResolveCaches(false) = %v, want empty", off)
	}

	// keep=false still refuses invalid paths so bad config fails closed.
	if _, err := ResolveCaches(false, []string{".cargo/bin"}); err == nil {
		t.Fatal("ResolveCaches(false, [.cargo/bin]) succeeded, want error")
	}

	// Layering: global drops npm and adds .cache/custom; host re-adds npm and drops .cache/custom and gradle.
	layered, err := ResolveCaches(
		true,
		[]string{"!.npm/_cacache", ".cache/custom", ".cache/uv"},
		[]string{".npm/_cacache", "!.cache/custom", "!.gradle/caches", ".cache/ccache"},
	)
	if err != nil {
		t.Fatalf("ResolveCaches(layered): %v", err)
	}
	if !slices.Contains(layered, ".npm/_cacache") {
		t.Errorf("host layer should re-add .npm/_cacache: %v", layered)
	}
	if slices.Contains(layered, ".cache/custom") {
		t.Errorf("host layer should drop .cache/custom: %v", layered)
	}
	if slices.Contains(layered, ".gradle/caches") {
		t.Errorf("host layer should drop .gradle/caches: %v", layered)
	}
	if !slices.Contains(layered, ".cache/ccache") {
		t.Errorf("host layer should add .cache/ccache: %v", layered)
	}
	// No duplicates even when .cache/uv was added again.
	countUV := 0
	for _, p := range layered {
		if p == ".cache/uv" {
			countUV++
		}
	}
	if countUV != 1 {
		t.Errorf(".cache/uv appeared %d times in %v, want 1", countUV, layered)
	}

	// Malformed drop rule rejected.
	for _, bad := range []string{"!", "!/abs", "!../escape", ""} {
		if _, err := ResolveCaches(true, []string{bad}); err == nil {
			t.Errorf("ResolveCaches(%q) succeeded, want error", bad)
		}
	}
}

func TestSortMountOrder(t *testing.T) {
	in := []string{".local/share/pnpm/store", ".cache/uv", ".cache", ".npm/_cacache"}
	got := SortMountOrder(in)
	want := []string{".cache", ".cache/uv", ".npm/_cacache", ".local/share/pnpm/store"}
	if !slices.Equal(got, want) {
		t.Fatalf("SortMountOrder = %v, want %v", got, want)
	}
}
