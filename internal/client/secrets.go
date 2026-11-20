package client

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
)

// SecretTimeout is how long the command of one secret may run. It is long
// enough for a password manager that asks you to unlock it.
var SecretTimeout = 60 * time.Second

// maxSecret is the most a secret's command may print.
const maxSecret = 64 << 10

var secretNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// SecretNames is the host's own secrets, then the ones asked for, without repeats.
func SecretNames(h Host, asked []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, n := range append(append([]string(nil), h.Secrets...), asked...) {
		if n = strings.TrimSpace(n); n != "" && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

// FetchSecrets runs the command of each named secret from config.toml on this
// computer, and returns the values, for one session's start. Nothing is
// written down. With a terminal, a command may ask for a password or a touch
// on it; without one it gets no input and its messages are dropped. No error
// holds a value or anything a command printed.
func FetchSecrets(ctx context.Context, s Settings, names []string, terminal bool) (map[string]api.Secret, error) {
	if len(names) == 0 {
		return nil, nil
	}
	out := make(map[string]api.Secret, len(names))
	for _, name := range names {
		if !secretNameRE.MatchString(name) {
			return nil, &UserError{Msg: fmt.Sprintf("%q cannot name a secret", name), Hint: "Use letters, digits and underscores, as in OPENAI_API_KEY.", Code: "bad_secret", Kind: KindUsage}
		}
		command := strings.TrimSpace(s.Secrets[name])
		if command == "" {
			return nil, &UserError{Msg: fmt.Sprintf("no secret named %s in config.toml", name),
				Hint: fmt.Sprintf("Add %s = \"<the command that prints it>\" under [secrets] in %s.", name, settingsPath()), Code: "secret_not_found", Kind: KindNotFound}
		}
		v, err := runSecret(ctx, name, command, terminal)
		if err != nil {
			return nil, err
		}
		out[name] = v
	}
	return out, nil
}

// runSecret runs one secret's command and returns what it printed, less one
// final line break.
func runSecret(ctx context.Context, name, command string, terminal bool) (api.Secret, error) {
	cctx, cancel := context.WithTimeout(ctx, SecretTimeout)
	defer cancel()
	shell := []string{"/bin/sh", "-c", command}
	if runtime.GOOS == "windows" {
		shell = []string{"cmd", "/C", command}
	}
	cmd := exec.CommandContext(cctx, shell[0], shell[1:]...)
	var stdout bytes.Buffer
	cmd.Stdout = &limitWriter{w: &stdout, n: maxSecret}
	cmd.Stderr = io.Discard
	if terminal {
		cmd.Stdin, cmd.Stderr = os.Stdin, os.Stderr
	}
	cmd.WaitDelay = time.Second
	hint := fmt.Sprintf("Run the command of %s from config.toml yourself, to see what it says.", name)
	if err := cmd.Run(); err != nil {
		if cctx.Err() != nil {
			return "", &UserError{Msg: fmt.Sprintf("the command of the secret %s did not finish in %s", name, SecretTimeout), Hint: hint, Code: "secret_command"}
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return "", &UserError{Msg: fmt.Sprintf("the command of the secret %s failed (exit code %d)", name, ee.ExitCode()), Hint: hint, Code: "secret_command"}
		}
		return "", &UserError{Msg: fmt.Sprintf("the command of the secret %s did not run", name), Hint: hint, Code: "secret_command"}
	}
	if stdout.Len() > maxSecret {
		return "", &UserError{Msg: fmt.Sprintf("the command of the secret %s printed more than 64 KiB", name), Hint: hint, Code: "secret_command"}
	}
	v := strings.TrimSuffix(strings.TrimSuffix(stdout.String(), "\n"), "\r")
	switch {
	case v == "":
		return "", &UserError{Msg: fmt.Sprintf("the command of the secret %s printed nothing", name), Hint: hint, Code: "secret_command"}
	case strings.ContainsRune(v, 0):
		return "", &UserError{Msg: fmt.Sprintf("the command of the secret %s printed a NUL byte", name), Hint: hint, Code: "secret_command"}
	}
	return api.Secret(v), nil
}

// limitWriter keeps up to n bytes, and one more to show that there were more.
type limitWriter struct {
	w io.Writer
	n int
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if l.n >= 0 {
		k := min(len(p), l.n+1)
		_, _ = l.w.Write(p[:k])
		l.n -= k
	}
	return len(p), nil
}

// RequireSecrets fails when the host's helper cannot take secrets.
func (c *Conn) RequireSecrets(a NewArgs) error {
	if len(a.Secrets) == 0 || c.HasFeature("secrets") {
		return nil
	}
	return &UserError{Msg: "the helper on " + c.Host.Name + " is too old for secrets", Hint: brand.Name + " host add " + c.Host.Name + " (updates the helper)", Code: "helper_old"}
}
