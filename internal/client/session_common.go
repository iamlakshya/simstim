package client

import (
	"context"
	"fmt"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
)

// CheckSessionName refuses an ID-shaped or overlong session name before
// any request leaves the client, as the helper does too.
func CheckSessionName(name string) error {
	if msg, hint, ok := api.CheckName(name); !ok {
		return &UserError{Msg: msg, Hint: hint, Code: "bad_name", Kind: KindUsage}
	}
	return nil
}

// DeleteNamed removes id only while its name is still the exact name the
// user confirmed. An empty confirmed name is sent too.
func (c *Conn) DeleteNamed(ctx context.Context, id, name string) (DeleteResult, error) {
	var r DeleteResult
	err := c.Call(ctx, "delete", map[string]string{"id": id, "name": name}, &r)
	return r, err
}

// DeleteLines describes what deleting s stops and keeps. Host metadata is
// cleaned before these lines enter a command prompt or launcher sheet.
func DeleteLines(s Session, host, folder string) []string {
	oneLine := strings.NewReplacer("\r\n", " ", "\n", " ", "\t", " ")
	agent := api.CleanText(oneLine.Replace(s.Agent), 200)
	host = api.CleanText(oneLine.Replace(host), 200)
	folder = api.CleanText(oneLine.Replace(folder), 1000)
	stays := fmt.Sprintf("The folder %s stays.", folder)
	if MaybeWorktree(s) {
		stays = fmt.Sprintf("If the session made the worktree %s, it goes with its branch, unless it has changes or new commits.", folder)
	}
	if Ended(s) {
		// Nothing runs that could lose work: say only what goes.
		return []string{fmt.Sprintf("This removes the session from %s.", host), stays}
	}
	return []string{
		fmt.Sprintf("This stops %s on %s and removes the session.", agent, host),
		stays + " Unsaved work in the agent is lost.",
	}
}

// MaybeWorktree reports whether s runs under the helper's worktree folder.
// The location alone does not say that the session owns that worktree.
func MaybeWorktree(s Session) bool {
	return strings.Contains(s.Cwd, "/"+brand.Name+"/worktrees/")
}
