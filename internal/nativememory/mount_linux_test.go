//go:build linux

package nativememory

import (
	"errors"
	"strings"
	"testing"
)

func TestNativeMemoryPathsAndMappingRejectTraversalAndPrefixAliases(t *testing.T) {
	for _, path := range []string{"relative", "/../unit", "/unit/../other", "/unit//child", "/unit/", "/unit/\x00child"} {
		if closedNativePath(path) {
			t.Fatalf("unclosed native path accepted: %q", path)
		}
	}
	if _, ok := relativeGroup("/unit", "/unit-other/child"); ok {
		t.Fatal("a path prefix was mistaken for cgroup ancestry")
	}
	if relative, ok := relativeGroup("/unit", "/unit/child"); !ok || relative != "child" {
		t.Fatalf("actual child mapping lost: %q, %v", relative, ok)
	}
	input := "11 1 0:1 /unit /clipped rw - cgroup2 cgroup rw\n12 1 0:1 / /whole rw - cgroup2 cgroup rw\n"
	mount, err := findMemoryMount(t.Context(), strings.NewReader(input), "/unit/child")
	if err != nil || mount.id != 12 || mount.root != "/" || mount.path != "/whole" {
		t.Fatalf("widest visible native hierarchy = %+v, %v", mount, err)
	}
	if _, err := findMemoryMount(t.Context(), strings.NewReader("11 1 0:1 /unit /clipped rw - cgroup2 cgroup rw\n"), "/unit-other"); !errors.Is(err, ErrUnknown) {
		t.Fatalf("unmapped current scope acquired a fallback: %v", err)
	}
}

func TestMemoryMembershipRejectsUnsupportedAndAmbiguousScope(t *testing.T) {
	for _, input := range []string{"", "0::/\n0::/other\n", "0::/../outside\n", "wrong::/unit\n"} {
		if _, err := currentUnifiedPath([]byte(input)); err == nil {
			t.Fatalf("unprovable current scope accepted: %q", input)
		}
	}
	if _, err := currentUnifiedPath([]byte("7:memory:/limited\n0::/unit\n")); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("legacy memory hierarchy became an unlimited provider: %v", err)
	}
	path, err := currentUnifiedPath([]byte("7:cpu:/legacy\n0::/unit\n"))
	if err != nil || path != "/unit" {
		t.Fatalf("real unified memory membership was lost: %q, %v", path, err)
	}
}

func TestMemoryMountEscapesAreExactAndUnknownInputRefuses(t *testing.T) {
	path, err := mountPath(`/native\040root/with\134slash`)
	if err != nil || path != "/native root/with\\slash" {
		t.Fatalf("kernel mount escape = %q, %v", path, err)
	}
	for _, invalid := range []string{`/native\777root`, `/native\0`, `/../native`} {
		if _, err := mountPath(invalid); err == nil {
			t.Fatalf("unknown mount path acquired authority: %q", invalid)
		}
	}
}
