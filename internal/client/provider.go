package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
)

// Provider lets a host be started and stopped by a script the user writes, so
// cloud VMs can sleep when idle without tying the tool to any provider.
//
// The script is called as `script <action>`, with a JSON job on stdin:
//
//	{"action": "start", "host": {"name": "...", "ssh": "...", "labels": {...}}}
//
// Actions: start, stop, status. status prints {"state": "running"} or
// {"state": "stopped"}. A non-zero exit is an error; stderr is shown to the user.
type Provider struct {
	// Script names a program that starts, stops and reports on the host. It gets
	// the action as its argument and a JSON job on stdin (see
	// examples/providers).
	Script string `toml:"script"`
	// WakeOnAttach starts the host through the script when it does not answer
	// and you attach or start a session.
	WakeOnAttach bool `toml:"wake_on_attach,omitempty"`
}

// ProviderStatus is the answer to the status action.
type ProviderStatus struct {
	State string `json:"state"`
}

// RunProvider runs one provider action for h.
func RunProvider(ctx context.Context, h Host, action string) ([]byte, error) {
	if h.Provider == nil || h.Provider.Script == "" {
		return nil, &UserError{Msg: h.Name + " has no provider script", Hint: brand.Name + " host add " + h.Name + " --provider-script <path>", Code: "no_provider_script"}
	}
	job, _ := json.Marshal(map[string]any{"action": action, "host": map[string]any{"name": h.Name, "ssh": h.SSH, "labels": h.Labels}})
	cmd := exec.CommandContext(ctx, expand(h.Provider.Script), action)
	cmd.Stdin = bytes.NewReader(job)
	cmd.Env = append(os.Environ(), brand.Env("HOST")+"="+h.Name, brand.Env("ACTION")+"="+action)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, &UserError{Msg: fmt.Sprintf("the provider script could not %s %s: %s", action, h.Name, msg), Hint: "Run " + pathWord(h.Provider.Script) + " " + action + " by hand to see what fails.", Code: "provider_script"}
	}
	return out.Bytes(), nil
}

// wake starts a stopped host through its provider, then waits until SSH works.
func wake(ctx context.Context, h Host) error {
	if _, err := RunProvider(ctx, h, "start"); err != nil {
		return err
	}
	deadline := time.Now().Add(4 * time.Minute)
	for time.Now().Before(deadline) {
		s := &SSH{Host: h}
		pctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		_, err := s.Run(pctx, "true", nil)
		cancel()
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
	return errors.New("the host did not answer within 4 minutes of starting")
}
