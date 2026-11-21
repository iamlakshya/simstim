// Package seedmeta describes non-authorising facts read from a selected seed.
package seedmeta

import (
	"bytes"
	"errors"
	"slices"
)

// MaxListingBytes bounds the fixed root manifest-name listing.
const MaxListingBytes = 8 << 10

var rootManifests = []struct{ Name, Ecosystem string }{
	{"Cargo.toml", "rust"}, {"Gemfile", "ruby"}, {"Package.swift", "swift"},
	{"Pipfile", "python"}, {"build.gradle", "java"}, {"build.gradle.kts", "java"},
	{"composer.json", "php"}, {"global.json", "dotnet"}, {"go.mod", "go"}, {"go.work", "go"},
	{"mix.exs", "elixir"}, {"package.json", "javascript"}, {"pom.xml", "java"},
	{"pubspec.yaml", "dart"}, {"pyproject.toml", "python"}, {"requirements.txt", "python"},
	{"setup.cfg", "python"}, {"setup.py", "python"},
}

// RootManifests returns literal Git pathspecs. No wildcard, recursion or
// working-tree file read is needed, and Git follows no manifest symlink.
func RootManifests() []string {
	names := make([]string, len(rootManifests))
	for index, manifest := range rootManifests {
		names[index] = manifest.Name
	}
	return names
}

// Ecosystems accepts only the exact bounded NUL-terminated result of the
// selected commit's fixed `ls-tree -z --name-only` query. File contents and
// loose working-tree files cannot contribute a tag.
func Ecosystems(listing []byte) ([]string, error) {
	bad := errors.New("seed manifest-name listing is invalid. Try: prepare the original committed seed again")
	if len(listing) > MaxListingBytes || len(listing) > 0 && listing[len(listing)-1] != 0 {
		return nil, bad
	}
	if len(listing) == 0 {
		return nil, nil
	}
	var result []string
	seen := make(map[string]bool)
	for _, raw := range bytes.Split(listing[:len(listing)-1], []byte{0}) {
		name := string(raw)
		if seen[name] {
			return nil, bad
		}
		seen[name] = true
		found := false
		for _, manifest := range rootManifests {
			if name == manifest.Name {
				result = append(result, manifest.Ecosystem)
				found = true
				break
			}
		}
		if !found {
			return nil, bad
		}
	}
	slices.Sort(result)
	return slices.Compact(result), nil
}

// ValidEcosystems checks the closed, sorted and duplicate-free tag set. It
// checks the data grammar only, never the seed's source or any permission.
func ValidEcosystems(values []string) bool {
	if len(values) > 16 || !slices.IsSorted(values) {
		return false
	}
	for index, value := range values {
		if index > 0 && values[index-1] == value {
			return false
		}
		known := false
		for _, manifest := range rootManifests {
			known = known || value == manifest.Ecosystem
		}
		if !known {
			return false
		}
	}
	return true
}
