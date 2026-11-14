package onboard

import (
	"fmt"
	"path"
	"strings"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

// Line is one agent as a summary shows it: a mark, its title, what is true,
// and what to try next.
type Line struct {
	// Mark is ✓ ready, ! not logged in, ? login unknown, or ✕ not ready.
	Mark  string
	Title string
	// State says what is true, with the path and how the host knows; Short
	// says it in a few words.
	State string
	Short string
	Try   string
}

func cleanLine(l Line) Line {
	l.Title = api.CleanText(l.Title, 200)
	l.State = api.CleanText(l.State, 4000)
	l.Short = api.CleanText(l.Short, 200)
	l.Try = api.CleanText(l.Try, 4000)
	return l
}

// Describe says where an agent stands on host, from the host's check, and
// what to do next. The check is the host's text, so the line holds it
// cleaned (api.CleanText): the launcher and the command line draw it.
func Describe(host string, c api.AgentCheck) Line {
	c.Kind, c.Title = api.CleanText(c.Kind, 200), api.CleanText(c.Title, 200)
	c.Path, c.LoginHow = api.CleanText(c.Path, 4000), api.CleanText(c.LoginHow, 4000)
	a, _ := agents.Find(c.Kind)
	l := Line{Mark: "✓", Title: c.Title}
	switch {
	case !c.Installed:
		l.Mark, l.State, l.Short = "✕", "not installed", "not installed"
		l.Try = InstallCommand(host, c.Kind)
	case !c.OnPath:
		l.Mark, l.State, l.Short = "✕", c.Path+" is not on PATH in a login shell", "installed, not on PATH"
		l.Try = PathHint(path.Dir(c.Path))
	case c.Login == api.LoginYes:
		l.State, l.Short = fmt.Sprintf("%s, logged in (%s)", c.Path, c.LoginHow), "installed, logged in"
	case c.Login == api.LoginNo:
		l.Mark, l.State, l.Short = "!", fmt.Sprintf("%s, not logged in (%s)", c.Path, c.LoginHow), "installed, not logged in"
		l.Try = LoginCommand(host, a)
	default:
		l.Mark, l.State, l.Short = "?", fmt.Sprintf("%s, login unknown (%s)", c.Path, c.LoginHow), "installed, login unknown"
		if cmd := LoginCommand(host, a); cmd != "" {
			l.Try = "if it asks you to log in: " + cmd
		}
	}
	if l.Try == "" && c.Login != api.LoginYes && a.Setup != nil && len(a.Setup.LoginEnv) > 0 && c.OnPath {
		l.Try = fmt.Sprintf("pass its key from your laptop: %s new %s --agent %s --secret %s", brand.Name, host, c.Kind, a.Setup.LoginEnv[0])
	}
	return l
}

// PathHint is the command that adds dir, a folder that the host named, to
// PATH in ~/.profile on the host. dir is quoted in the profile line, and the
// line is quoted for the shell that runs the command, so that a quote, $( )
// or a backtick in dir stays text in both.
func PathHint(dir string) string {
	if dir != "" && strings.Trim(dir, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_./") == "" {
		return fmt.Sprintf("on the host, add it to PATH: echo 'export PATH=\"%s:$PATH\"' >> ~/.profile", dir)
	}
	line := "export PATH=" + client.ShellWord(dir) + `:"$PATH"`
	return "on the host, add it to PATH: printf '%s\\n' " + client.ShellWord(line) + " >> ~/.profile"
}
