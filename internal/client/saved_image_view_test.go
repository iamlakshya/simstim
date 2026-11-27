package client

import (
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestSavedImageViewRejectsHostileFieldsAndOwnsDisplayCopy(t *testing.T) {
	original := &api.SavedImageInfoV1{Version: 1, Ref: "fixture_saved", Digest: "sha256:" + strings.Repeat("a", 64), HostID: strings.Repeat("b", 32)}
	input := Session{Wall: &api.WallInfo{Level: dirty, Driver: dirty, Version: dirty, Floor: dirty, DowngradedFrom: dirty, Preset: dirty, SavedImage: original}}
	got := CleanSession(input)
	if got.Wall == input.Wall || got.Wall.SavedImage == original || got.Wall.Level != tidy || got.Wall.Driver != tidy || got.Wall.Version != tidy || got.Wall.Floor != tidy || got.Wall.DowngradedFrom != tidy || got.Wall.Preset != tidy {
		t.Fatal("saved image display kept host-owned mutable/control fields")
	}
	got.Wall.SavedImage.Ref = "changed"
	if original.Ref != "fixture_saved" || input.Wall.Driver != dirty {
		t.Fatal("clean display mutated original host metadata")
	}
	for _, change := range []func(*api.SavedImageInfoV1){func(v *api.SavedImageInfoV1) { v.Version = 2 }, func(v *api.SavedImageInfoV1) { v.Ref = dirty }, func(v *api.SavedImageInfoV1) { v.Digest = dirty }, func(v *api.SavedImageInfoV1) { v.HostID = dirty }} {
		copy := *original
		change(&copy)
		input.Wall.SavedImage = &copy
		if row := CleanSession(input); row.Wall.SavedImage != nil {
			t.Fatal("invalid saved origin reached display")
		}
	}
}
