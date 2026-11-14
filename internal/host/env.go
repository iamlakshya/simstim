package host

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"os/user"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
)

// LoginShell returns the user's login shell.
func LoginShell() string {
	if s := os.Getenv("SHELL"); s != "" {
		return s
	}
	if u, err := user.Current(); err == nil {
		if b, err := os.ReadFile("/etc/passwd"); err == nil {
			for _, line := range strings.Split(string(b), "\n") {
				f := strings.Split(line, ":")
				if len(f) >= 7 && f[0] == u.Username && f[6] != "" {
					return f[6]
				}
			}
		}
	}
	return "/bin/sh"
}

// LoginEnv runs the user's login shell once and returns its environment, the way
// VS Code Remote does. This finds agents installed with nvm, mise, bun or npm.
func LoginEnv(timeout time.Duration) ([]string, error) {
	return LoginEnvContext(context.Background(), timeout)
}

func LoginEnvContext(owner context.Context, timeout time.Duration) ([]string, error) {
	if err := owner.Err(); err != nil {
		return nil, err
	}
	shell := LoginShell()
	var nonce [8]byte
	_, _ = rand.Read(nonce[:])
	marker := "__" + strings.ToUpper(brand.Name) + "_ENV_" + hex.EncodeToString(nonce[:]) + "__"
	ctx, cancel := context.WithTimeout(owner, timeout)
	defer cancel()
	args := []string{"-l", "-i", "-c", "printf '%s' " + marker + "; env -0"}
	if strings.HasSuffix(shell, "/sh") || strings.HasSuffix(shell, "/dash") {
		args = []string{"-l", "-c", "printf '%s' " + marker + "; env -0"}
	}
	cmd := exec.CommandContext(ctx, shell, args...)
	devnull, _ := os.Open(os.DevNull)
	defer devnull.Close()
	cmd.Stdin = devnull
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = nil
	cmd.Env = os.Environ()
	_ = cmd.Run()
	i := bytes.Index(out.Bytes(), []byte(marker))
	if i < 0 {
		if owner.Err() != nil {
			return nil, owner.Err()
		}
		if ctx.Err() != nil {
			return nil, errors.New("the login shell took too long to start")
		}
		return nil, errors.New("could not read the login shell's environment")
	}
	if owner.Err() != nil {
		return nil, owner.Err()
	}
	var env []string
	for _, kv := range bytes.Split(out.Bytes()[i+len(marker):], []byte{0}) {
		if s := string(kv); strings.Contains(s, "=") {
			env = append(env, s)
		}
	}
	return env, nil
}

// dropKeys are removed from the agent's environment: they describe the shell or
// service that ran the resolver, or a multiplexer the agent is not inside.
var dropKeys = map[string]bool{
	"TERM": true, "COLORTERM": true, "TERM_PROGRAM": true, "TERM_PROGRAM_VERSION": true,
	"TMUX": true, "TMUX_PANE": true, "STY": true, "ZELLIJ": true, "ZELLIJ_SESSION_NAME": true,
	"INVOCATION_ID": true, "JOURNAL_STREAM": true, "SYSTEMD_EXEC_PID": true, "MANAGERPID": true,
	"SHLVL": true, "OLDPWD": true, "PWD": true, "_": true,
}

// AgentEnv builds the environment for an agent from the login environment.
func AgentEnv(login []string, set map[string]string) []string {
	out := make([]string, 0, len(login)+len(set))
	for _, kv := range login {
		k, _, _ := strings.Cut(kv, "=")
		if dropKeys[k] || strings.HasPrefix(k, brand.EnvPrefix) {
			continue
		}
		if _, ok := set[k]; ok {
			continue
		}
		out = append(out, kv)
	}
	for k, v := range set {
		if v != "" {
			out = append(out, k+"="+v)
		}
	}
	return out
}

// HasTerminfo reports whether the host knows the given TERM.
func HasTerminfo(term string) bool {
	if term == "" {
		return false
	}
	return exec.CommandContext(context.Background(), "infocmp", term).Run() == nil
}
