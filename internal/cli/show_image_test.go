package cli

import (
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

func savedImageShowFixture() client.Session {
	return client.Session{ID: "image234", Alive: true, State: api.StateWorking,
		Wall: &api.WallInfo{Level: "microvm", SavedImage: &api.SavedImageInfoV1{
			Version: 1, Ref: "devtools", Digest: "sha256:" + strings.Repeat("b", 64), HostID: strings.Repeat("a", 32)}}}
}

func TestShowNamesOnlyTheLiveSavedImageOnItsAcceptedHost(t *testing.T) {
	row := savedImageShowFixture()
	if out := describeSession("box", row, strings.Repeat("a", 32)); !strings.Contains(out, "Image           devtools (made on box)") {
		t.Fatalf("saved image origin omitted: %q", out)
	}
	for _, test := range []struct {
		name string
		host string
		edit func(*client.Session)
	}{
		{"no accepted identity", "", func(*client.Session) {}},
		{"another host", strings.Repeat("c", 32), func(*client.Session) {}},
		{"stopped", strings.Repeat("a", 32), func(s *client.Session) { s.Alive = false }},
		{"other wall", strings.Repeat("a", 32), func(s *client.Session) { s.Wall.Level = "process" }},
		{"future", strings.Repeat("a", 32), func(s *client.Session) { s.Wall.SavedImage.Version = 2 }},
		{"unsafe ref", strings.Repeat("a", 32), func(s *client.Session) { s.Wall.SavedImage.Ref = "tools\x1b[31m" }},
		{"bad digest", strings.Repeat("a", 32), func(s *client.Session) { s.Wall.SavedImage.Digest = "changed" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			row := savedImageShowFixture()
			test.edit(&row)
			original := *row.Wall.SavedImage
			out := describeSession("box", row, test.host)
			if strings.Contains(out, "made on") || strings.ContainsRune(out, '\x1b') || *row.Wall.SavedImage != original {
				t.Fatalf("invalid or stale origin became live display, or was changed: %q", out)
			}
		})
	}
}
