package image

import (
	"strings"
	"testing"
)

func TestOCIReferencesPinExplicitDigestsAndNormaliseRegistry(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	for _, test := range []struct{ text, registry, repository, id string }{{"alpine", "registry-1.docker.io", "library/alpine", "latest"}, {"docker.io/team/private:1", "registry-1.docker.io", "team/private", "1"}, {"index.docker.io/library/alpine@" + digest, "registry-1.docker.io", "library/alpine", digest}, {"registry.example/team/dev:tag@" + digest, "registry.example", "team/dev", digest}, {"[2001:db8::1]:5443/team/dev:1", "[2001:db8::1]:5443", "team/dev", "1"}} {
		got, err := ParseReference(test.text)
		if err != nil || got.Registry != test.registry || got.Repository != test.repository || got.Identifier != test.id {
			t.Fatalf("reference %s: %+v %v", test.text, got, err)
		}
	}
	if _, err := ParseReference("registry.example/team/dev__tools---base:1"); err != nil {
		t.Fatal(err)
	}
}

func TestOCIReferencesRejectPathsURLsAndAmbiguousAuthority(t *testing.T) {
	for _, text := range []string{"https://registry.example/team/dev", "registry.example/team/../dev", "registry.example/Uppercase", "user:password@registry.example/team/dev", "registry.example:65536/team/dev", "[::1]:0/team/dev", "registry.example/team/dev@sha256:bad"} {
		if _, err := ParseReference(text); err == nil {
			t.Fatalf("unsafe reference accepted: %q", text)
		}
	}
	request := Request{Ref: "registry.example/team/dev@sha256:" + strings.Repeat("a", 64), Digest: "sha256:" + strings.Repeat("b", 64), PlatformDigest: "sha256:" + strings.Repeat("b", 64), Arch: "amd64"}
	assertFlatCode(t, request.Validate(), "image_bad_layer")
}
