package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

func savedOriginSessionFixture() client.Session {
	return client.Session{ID: "image234", Alive: true, State: api.StateWorking, Cwd: "/work",
		Wall: &api.WallInfo{Level: "microvm", SavedImage: &api.SavedImageInfoV1{
			Version: 1, Ref: "devtools", Digest: "sha256:" + strings.Repeat("b", 64), HostID: strings.Repeat("a", 32)}}}
}

func savedOriginHostFixture() *hostConn {
	return &hostConn{host: client.Host{Name: "box"}, loaded: true, status: stOnline,
		conn: &fakeConn{hostID: strings.Repeat("a", 32)}}
}

func TestLauncherShowsLiveSavedImageOriginWithinItsDetailWidth(t *testing.T) {
	model := &Model{}
	host, row := savedOriginHostFixture(), savedOriginSessionFixture()
	for _, width := range []int{80, 120} {
		text := ansi.Strip(model.sessionDetail(host, row, width, true))
		if !strings.Contains(text, "devtools (made on box)") || ansi.StringWidth(text) > width {
			t.Fatalf("width %d lost saved image origin or overflowed: %q", width, text)
		}
	}
	if row.Wall.SavedImage.Ref != "devtools" || row.Wall.SavedImage.HostID != strings.Repeat("a", 32) {
		t.Fatal("drawing changed the protected origin projection")
	}
}

func TestLauncherDoesNotNameASavedImageFromStaleOrDifferentHostData(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*hostConn, *client.Session)
	}{
		{"cached", func(h *hostConn, _ *client.Session) { h.loaded = false }},
		{"offline", func(h *hostConn, _ *client.Session) { h.status = stOffline }},
		{"identity changed", func(h *hostConn, _ *client.Session) { h.idChanged = true }},
		{"connection changed", func(h *hostConn, _ *client.Session) { h.conn.(*fakeConn).idChanged = true }},
		{"another host", func(h *hostConn, _ *client.Session) { h.conn.(*fakeConn).hostID = strings.Repeat("c", 32) }},
		{"stopped", func(_ *hostConn, s *client.Session) { s.Alive = false }},
		{"unsafe projection", func(_ *hostConn, s *client.Session) { s.Wall.SavedImage.Ref = "tools\x1b[31m" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			host, row := savedOriginHostFixture(), savedOriginSessionFixture()
			test.edit(host, &row)
			original := *row.Wall.SavedImage
			text := ansi.Strip((&Model{}).sessionDetail(host, row, 120, true))
			if strings.Contains(text, "made on") || strings.ContainsRune(text, '\x1b') || *row.Wall.SavedImage != original {
				t.Fatalf("stale or invalid origin was displayed or mutated: %q", text)
			}
		})
	}
}
