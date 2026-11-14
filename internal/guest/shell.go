package guest

import (
	"bufio"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/sandbox"
)

func (g *Init) resolveShell(a *proto.CtlStartArgs) error {
	if !a.DefaultShell {
		return nil
	}
	shell := a.Shell
	if shell == "" {
		f, err := os.Open(g.path("/etc/passwd"))
		if err == nil {
			shell, err = imageLoginShell(f)
			f.Close()
		}
		if err != nil {
			// Only the explicitly installed no-wall test spawner may preserve its
			// original host command. RootEnv alone never enables this branch.
			if g.root != "" && plainHolderSpawnerForTests != nil && len(a.Argv) > 0 && a.Argv[0] != "" {
				shell = a.Argv[0]
			} else {
				return shellError("the guest image has no agent login shell")
			}
		}
	}
	if err := sandbox.ValidateShell(shell); err != nil {
		return shellError("the configured guest shell is invalid")
	}
	if len(a.Argv) == 0 {
		return shellError("the shell command is missing")
	}
	a.Argv = append([]string(nil), a.Argv...)
	a.Argv[0] = shell
	return nil
}

// imageLoginShell reads guest account data, never a host NSS command. Account
// text can choose only a shell; it never chooses the process credential.
func imageLoginShell(input io.Reader) (string, error) {
	reader := bufio.NewReader(input)
	var shell string
	found := false
	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			fields := strings.Split(strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"), ":")
			if len(fields) > 0 && fields[0] == "agent" {
				if found || len(fields) != 7 || fields[2] != "1000" || fields[3] != "1000" || !filepath.IsAbs(fields[6]) {
					return "", shellError("the guest agent account has an invalid login shell")
				}
				shell, found = fields[6], true
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return "", err
			}
			break
		}
	}
	if !found || shell == "" {
		return "", shellError("the guest image has no agent login shell")
	}
	return shell, nil
}
func shellError(message string) error {
	return &proto.Error{Code: "shell_missing", Message: message, Hint: "Choose an installed guest shell with sandbox.shell, or update the guest image."}
}
