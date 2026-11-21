package seedmeta

import (
	"reflect"
	"strings"
	"testing"
)

func TestSelectedTreeManifestNamesProduceOnlyOwnedSortedTags(t *testing.T) {
	actual, err := Ecosystems([]byte("package.json\x00go.mod\x00pyproject.toml\x00go.work\x00"))
	if err != nil || !reflect.DeepEqual(actual, []string{"go", "javascript", "python"}) || !ValidEcosystems(actual) {
		t.Fatal("selected manifest facts changed", actual, err)
	}
	names := RootManifests()
	names[0] = "mutated"
	if RootManifests()[0] == "mutated" {
		t.Fatal("caller changed the manifest-name table")
	}
}

func TestSeedManifestParserRejectsUnexpectedPathsAndMalformedListings(t *testing.T) {
	for _, listing := range []string{"go.mod", "go.mod\x00go.mod\x00", "nested/go.mod\x00", ".env\x00", "go.mod\x00\x00", strings.Repeat("x", MaxListingBytes+1)} {
		if _, err := Ecosystems([]byte(listing)); err == nil {
			t.Fatal("unexpected or unbounded seed metadata accepted")
		}
	}
	for _, tags := range [][]string{{"python", "go"}, {"go", "go"}, {"unrecognised"}} {
		if ValidEcosystems(tags) {
			t.Fatal("invalid ecosystem set accepted")
		}
	}
}

func TestJudgeRemoteDropsCredentialsDecorationsAndLocalPaths(t *testing.T) {
	for raw, want := range map[string]string{
		"https://name:password@example.com/team/repo.git?token=excluded#fragment": "https://example.com/team/repo.git",
		"ssh://name:password@example.com/team/repo.git":                           "ssh://example.com/team/repo.git",
		"git@example.com:team/repo.git":                                           "example.com:team/repo.git",
		"file:///local/repo":                                                      "", "/local/repo": "", "../repo": "", "name:password@example.com:team/repo.git": "",
	} {
		if got := Remote(raw); got != want {
			t.Fatalf("remote sanitisation got %q, want %q", got, want)
		}
	}
}
