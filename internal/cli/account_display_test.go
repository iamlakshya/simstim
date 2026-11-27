package cli

import (
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
	"strings"
	"testing"
)

func TestOwnAccountDisplayRequiresActualCurrentSessionProjection(t *testing.T) {
	s := client.Session{Alive: true, Wall: &api.WallInfo{Level: "microvm"}}
	if sessionAccountMarkV1(s) != "" || strings.Contains(showSessionWall(s), "own account") {
		t.Fatal("microvm or preparation alone implied a distinct account")
	}
	s.Wall.OwnAccount = true
	if sessionAccountMarkV1(s) == "" || !strings.Contains(showSessionWall(s), "own account") {
		t.Fatal("actual own-account run projection omitted")
	}
	s.Alive = false
	if sessionAccountMarkV1(s) != "" || strings.Contains(showSessionWall(s), "own account") {
		t.Fatal("stopped row claimed a current account mode")
	}
	s.Alive = true
	s.Wall.Level = "process"
	if sessionAccountMarkV1(s) != "" || strings.Contains(showSessionWall(s), "own account") {
		t.Fatal("another level borrowed native VM account metadata")
	}
}
