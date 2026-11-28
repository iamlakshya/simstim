package client

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/iamlakshya/simstim/internal/brand"
)

// ValidEnvName reports whether name is a valid POSIX environment variable
// identifier.
func ValidEnvName(name string) bool {
	if name == "" {
		return false
	}
	for i := range len(name) {
		c := name[i]
		letter := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_'
		digit := c >= '0' && c <= '9'
		if i == 0 && !letter {
			return false
		}
		if !letter && !digit {
			return false
		}
	}
	return true
}

// ParseEnvSpec parses one `--env` entry (`K=V` or `K`). When `=` is absent,
// lookup reads the value from the laptop's environment and returns an
// env_missing usage error (exit 2) if unset (spec 17.2).
func ParseEnvSpec(spec string, lookup func(string) (string, bool)) (string, string, error) {
	if lookup == nil {
		lookup = os.LookupEnv
	}
	if k, v, hasEq := strings.Cut(spec, "="); hasEq {
		if !ValidEnvName(k) {
			return "", "", &UserError{
				Msg:  fmt.Sprintf("invalid environment variable name %q", CleanLine(k)),
				Hint: "Use letters, digits and underscores, starting with a letter or underscore.",
				Code: "bad_env",
				Kind: KindUsage,
			}
		}
		if strings.ContainsRune(v, 0) {
			return "", "", &UserError{
				Msg:  fmt.Sprintf("environment variable %q contains a NUL byte", k),
				Hint: "Remove NUL bytes from the variable value.",
				Code: "bad_env",
				Kind: KindUsage,
			}
		}
		return k, v, nil
	}
	k := spec
	if !ValidEnvName(k) {
		return "", "", &UserError{
			Msg:  fmt.Sprintf("invalid environment variable name %q", CleanLine(k)),
			Hint: "Use letters, digits and underscores, starting with a letter or underscore.",
			Code: "bad_env",
			Kind: KindUsage,
		}
	}
	v, ok := lookup(k)
	if !ok {
		return "", "", &UserError{
			Msg:  fmt.Sprintf("environment variable %s is not set on this laptop", k),
			Hint: fmt.Sprintf("Set %s in your shell or pass --env %s=<value>.", k, k),
			Code: "env_missing",
			Kind: KindUsage,
		}
	}
	return k, v, nil
}

// ReadEnvFile reads one `--env-file` following Docker's rules (spec 17.2):
// one `K=V` or `K` per line, skipping blank lines and lines starting with `#`,
// stripping no quotes and expanding nothing. A `K` missing from the laptop's
// environment fails with exit code 2.
func ReadEnvFile(path string, lookup func(string) (string, bool)) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, &UserError{
			Msg:  fmt.Sprintf("cannot read env file %s: %v", CleanPath(path), err),
			Hint: "Check the path passed to --env-file.",
			Code: "bad_env",
			Kind: KindUsage,
		}
	}
	defer f.Close()

	out := map[string]string{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSuffix(sc.Text(), "\r")
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, err := ParseEnvSpec(line, lookup)
		if err != nil {
			var ue *UserError
			if errors.As(err, &ue) {
				ue.Msg = fmt.Sprintf("%s, line %d: %s", CleanPath(path), lineNo, ue.Msg)
				return nil, ue
			}
			return nil, err
		}
		out[k] = v
	}
	if err := sc.Err(); err != nil {
		return nil, &UserError{
			Msg:  fmt.Sprintf("read env file %s: %v", CleanPath(path), err),
			Hint: "Check that the file is readable UTF-8 text.",
			Code: "bad_env",
			Kind: KindUsage,
		}
	}
	return out, nil
}

// ResolveEnv merges environment variables from config layers (Settings.Sandbox,
// Host.Sandbox), `--env-file` files and `--env` flags in precedence order
// (narrowest layer wins; later flags win over earlier ones). Any variable whose
// name is defined in `[secrets]` is refused with env_is_secret (spec 17.2).
func ResolveEnv(s Settings, h Host, envFiles, envFlags []string, lookup func(string) (string, bool)) (map[string]string, error) {
	merged := map[string]string{}
	addMap := func(src map[string]string) error {
		for k, v := range src {
			if !ValidEnvName(k) {
				return &UserError{
					Msg:  fmt.Sprintf("invalid environment variable name %q in config", CleanLine(k)),
					Hint: brand.Name + " config check",
					Code: "bad_env",
					Kind: KindUsage,
				}
			}
			merged[k] = v
		}
		return nil
	}
	if err := addMap(s.Sandbox.Env); err != nil {
		return nil, err
	}
	if h.Sandbox != nil {
		if err := addMap(h.Sandbox.Env); err != nil {
			return nil, err
		}
	}
	for _, fpath := range envFiles {
		m, err := ReadEnvFile(fpath, lookup)
		if err != nil {
			return nil, err
		}
		for k, v := range m {
			merged[k] = v
		}
	}
	for _, spec := range envFlags {
		k, v, err := ParseEnvSpec(spec, lookup)
		if err != nil {
			return nil, err
		}
		merged[k] = v
	}
	for k := range merged {
		if _, isSecret := s.Secrets[k]; isSecret {
			return nil, &UserError{
				Msg:  k + " is configured under [secrets] and cannot be passed as a plain environment variable",
				Hint: fmt.Sprintf("Pass it with --secret %s instead of --env.", k),
				Code: "env_is_secret",
				Kind: KindRefused,
			}
		}
	}
	if len(merged) == 0 {
		return nil, nil
	}
	return merged, nil
}
