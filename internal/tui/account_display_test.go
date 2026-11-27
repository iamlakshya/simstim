package tui

import (
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
	"strings"
	"testing"
)

func TestLauncherOwnAccountLabelIsOnlyDrawnFromCurrentNativeRunProjection(t *testing.T) {
	s := client.Session{Alive: true, Wall: &api.WallInfo{Level: "microvm"}}
	if sessionAccountLabelV1(s) != "" {
		t.Fatal("prepared or ordinary VM implied distinct account mode")
	}
	s.Wall.OwnAccount = true
	row := accountRowFolderV1(s, "/work/project", 40)
	if !strings.Contains(row, "own account") || !strings.Contains(sessionWallLevel(s), "own account") {
		t.Fatal("actual native mode absent from row and detail", row)
	}
	if cells(accountRowFolderV1(s, "/work/project", 12)) > 12 {
		t.Fatal("account mode overran the row column")
	}
	s.Alive = false
	if strings.Contains(accountRowFolderV1(s, "/work/project", 40), "own account") {
		t.Fatal("stopped row claimed current native mode")
	}
}
