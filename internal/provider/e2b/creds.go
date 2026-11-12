package e2b

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/provider"
)

// The API key is found when needed and kept in memory only. It is never
// written, logged or sent to a host.

// cliConfigPath is where the E2B CLI keeps its login (packages/cli/src/user.ts).
func cliConfigPath() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(h, ".e2b", "config.json")
}

// cliKey reads the API key from the E2B CLI's login file, without changing
// it. Version 2 files keep it in projectApiKey, version 1 files in
// teamApiKey.
func cliKey(path string) string {
	if path == "" {
		return ""
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var c struct {
		ProjectAPIKey string `json:"projectApiKey"`
		TeamAPIKey    string `json:"teamApiKey"`
	}
	if json.Unmarshal(b, &c) != nil {
		return ""
	}
	if c.ProjectAPIKey != "" {
		return c.ProjectAPIKey
	}
	return c.TeamAPIKey
}

// runTokenCommand runs the user's token command and returns what it prints.
func runTokenCommand(ctx context.Context, command string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/C", command)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", command)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	cmd.Stdin = os.Stdin // a keychain or password manager may ask
	if err := cmd.Run(); err != nil {
		return "", &client.UserError{
			Msg:  fmt.Sprintf("the token command failed: %v: %s", err, provider.Tail(errb.String(), 2)),
			Hint: "Run " + command + " by hand, and check that it prints the key.",
			Code: "token_command",
		}
	}
	key := strings.TrimSpace(out.String())
	if key == "" {
		return "", &client.UserError{Msg: "the token command printed nothing", Hint: "Run " + command + " by hand, and check that it prints the key.", Code: "token_command"}
	}
	return key, nil
}

// apiKey finds the E2B API key: E2B_API_KEY, then the E2B CLI's login file,
// then the host's token command.
func apiKey(ctx context.Context, tokenCommand, cliPath string) (string, error) {
	if k := strings.TrimSpace(os.Getenv("E2B_API_KEY")); k != "" {
		return k, nil
	}
	if k := cliKey(cliPath); k != "" {
		return k, nil
	}
	if tokenCommand != "" {
		return runTokenCommand(ctx, tokenCommand)
	}
	return "", &client.UserError{
		Msg:  "no E2B API key: E2B_API_KEY is not set, the E2B CLI is not logged in, and the host has no token_command",
		Hint: "export E2B_API_KEY=… (or run `e2b auth login`, or pass --token-command to `" + brand.Name + " host create e2b`)",
		Code: "no_api_key",
	}
}

// KeyFound says where an API key would come from without a token command:
// "E2B_API_KEY", "the E2B CLI's login", or "" when there is none. It never
// returns the key itself. The guided setup uses it to say whether E2B can
// work on this computer.
func KeyFound() string {
	if strings.TrimSpace(os.Getenv("E2B_API_KEY")) != "" {
		return "E2B_API_KEY"
	}
	if cliKey(cliConfigPath()) != "" {
		return "the E2B CLI's login"
	}
	return ""
}
