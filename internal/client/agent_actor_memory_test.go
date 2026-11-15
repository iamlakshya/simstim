//go:build darwin || linux

package client

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/paths"
)

func runAgentMemoryClient(cfg agentMemoryConfig) error {
	control, err := agentMemoryConnect(cfg.ClientControl)
	if err != nil {
		return err
	}
	defer control.conn.Close()
	if err := control.send(agentMemoryEvent{Role: "client", Event: "hello"}); err != nil {
		return err
	}
	var begin agentMemoryCommand
	if err := control.dec.Decode(&begin); err != nil || begin.Command != "begin" {
		return errors.New("memory client did not receive begin")
	}
	meter := newAgentMemoryMeter()
	defer debug.SetGCPercent(meter.oldGC)
	if err := control.send(agentMemoryEvent{Role: "client", Event: "baseline", Baseline: meter.base}); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var operation *agentMemoryClientCycle
	defer func() {
		if operation != nil {
			operation.close()
		}
	}()
	for {
		var cmd agentMemoryCommand
		if err := control.dec.Decode(&cmd); err != nil {
			return err
		}
		switch cmd.Command {
		case "start":
			if operation != nil {
				return errors.New("previous memory cycle was not closed")
			}
			if cmd.Cycle < 0 || cmd.Cycle >= len(cfg.HostNames) {
				return errors.New("unknown memory cycle")
			}
			operation = startAgentMemoryClientCycle(ctx, cfg, cmd.Cycle, control)
		case "sample":
			if err := control.send(meter.sample("client", cmd.Phase)); err != nil {
				return err
			}
		case "activate":
			if operation == nil {
				return errors.New("no memory cycle")
			}
			actor, failure := operation.ready()
			if failure == nil && actor == nil {
				failure = errors.New("memory client has no prepared actor")
			}
			if failure == nil {
				failure = actor.Activate(operation.ctx)
			}
			e := agentMemoryEvent{Role: "client", Event: "active", Phase: cmd.Phase}
			if failure != nil {
				e.Error = failure.Error()
			}
			if err := control.send(e); err != nil {
				return err
			}
		case "policy-checks":
			if operation == nil {
				return errors.New("no memory cycle")
			}
			actor, failure := operation.ready()
			checks, pings := 0, 0
			if failure == nil && (actor == nil || !actor.isActive()) {
				failure = errors.New("policy proof has no active actor")
			}
			if failure == nil {
				for range 32 {
					check, stop := context.WithTimeout(operation.ctx, 2*time.Second)
					failure = actor.checkLiveGrant(check, actor.scope.Session, actor.target)
					if failure == nil {
						checks++
						failure = actor.Ping(check)
						if failure == nil {
							pings++
						}
					}
					stop()
					if failure == nil && !actor.isActive() {
						failure = errors.New("actor revoked during repeated policy checks")
					}
					if failure != nil {
						break
					}
				}
			}
			e := meter.sample("client", cmd.Phase)
			e.PolicyChecks, e.Pings = checks, pings
			if failure != nil {
				if e.Error == "" {
					e.Error = failure.Error()
				} else {
					e.Error = errors.Join(errors.New(e.Error), failure).Error()
				}
			}
			if err := control.send(e); err != nil {
				return err
			}
		case "profile", "reject-profile":
			var p agentMemoryProfile
			var failure error
			if cmd.Command == "profile" {
				p, failure = meter.attribution("client")
			} else {
				p, failure = meter.rejectionAttribution()
			}
			e := agentMemoryEvent{Role: "client", Event: "profile", Phase: cmd.Phase, Profile: p}
			if failure != nil {
				e.Error = failure.Error()
			}
			if err := control.send(e); err != nil {
				return err
			}
		case "close":
			if operation != nil {
				operation.close()
				operation = nil
			}
			if err := control.send(meter.sample("client", cmd.Phase)); err != nil {
				return err
			}
		case "stop":
			return nil
		default:
			return errors.New("unknown client memory command")
		}
	}
}

func runAgentMemoryHost(cfg agentMemoryConfig) error {
	if err := agentMemoryInstallEnvironment(cfg.HostRoot); err != nil {
		return err
	}
	forward := os.Getenv("AGENT_MODE_MEMORY_FORWARD")
	if forward == "" {
		forward = cfg.Forward
	}
	if err := os.Setenv("SSH_AUTH_SOCK", forward); err != nil {
		return err
	}
	// On-disk state is a fixture prepared before the host mode exists.
	if err := os.MkdirAll(filepath.Dir(paths.HostID()), 0700); err != nil {
		return err
	}
	if err := os.WriteFile(paths.HostID(), []byte(cfg.HostID+"\n"), 0600); err != nil {
		return err
	}
	if err := (&host.Manifest{Schema: 1, ID: "abc23def", Name: "memory", SSHAgent: true, Cwd: cfg.HostRoot}).Save(); err != nil {
		return err
	}
	control, err := agentMemoryConnect(cfg.HostControl)
	if err != nil {
		return err
	}
	defer control.conn.Close()
	if err := control.send(agentMemoryEvent{Role: "host", Event: "hello"}); err != nil {
		return err
	}
	var begin agentMemoryCommand
	if err := control.dec.Decode(&begin); err != nil || begin.Command != "begin" {
		return errors.New("memory host did not receive begin")
	}
	meter := newAgentMemoryMeter()
	defer debug.SetGCPercent(meter.oldGC)
	if err := control.send(agentMemoryEvent{Role: "host", Event: "baseline", Baseline: meter.base}); err != nil {
		return err
	}
	ownerCtx, stopOwner := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() { done <- host.RunAgentBridgeProcessContext(ownerCtx, os.Stdin, os.Stdout) }()
	var joined sync.Once
	var processCode int
	join := func() { joined.Do(func() { processCode = <-done }) }
	// Only read-only metric emission follows this process-owned bridge. The
	// enclosing actor exits instead of reusing the host library or raw lanes.
	defer func() { stopOwner(); join() }()
	for {
		var cmd agentMemoryCommand
		if err := control.dec.Decode(&cmd); err != nil {
			return err
		}
		switch cmd.Command {
		case "sample":
			if err := control.send(meter.sample("host", cmd.Phase)); err != nil {
				return err
			}
		case "profile":
			p, err := meter.attribution("host")
			e := agentMemoryEvent{Role: "host", Event: "profile", Phase: cmd.Phase, Profile: p}
			if err != nil {
				e.Error = err.Error()
			}
			if err := control.send(e); err != nil {
				return err
			}
		case "stop":
			stopOwner()
			join()
			event := meter.sample("host", "shutdown")
			if processCode != 0 {
				event.Error = fmt.Sprintf("the measured host process ended with status %d", processCode)
			}
			if err := control.send(event); err != nil {
				return err
			}
			if processCode != 0 {
				return errors.New(event.Error)
			}
			return nil
		default:
			return fmt.Errorf("unknown host memory command %q", cmd.Command)
		}
	}
}

func agentMemoryEnvironment(root string, env []string) ([]string, error) {
	values := map[string]string{"HOME": "home", "USERPROFILE": "home", "XDG_CONFIG_HOME": "config", "XDG_STATE_HOME": "state", "XDG_DATA_HOME": "data", "XDG_CACHE_HOME": "cache", "XDG_RUNTIME_DIR": "run", "XDG_CONFIG_DIRS": "config-dirs", "XDG_DATA_DIRS": "data-dirs", "GNUPGHOME": "gpg", "TMPDIR": "tmp", "TMP": "tmp", "TEMP": "tmp"}
	for key, leaf := range values {
		path := filepath.Join(root, leaf)
		if err := os.MkdirAll(path, 0700); err != nil {
			return nil, err
		}
		values[key] = path
	}
	values["GIT_CONFIG_GLOBAL"] = filepath.Join(root, "gitconfig")
	values["GIT_CONFIG_SYSTEM"] = filepath.Join(root, "gitconfig-system")
	values["GIT_CONFIG_NOSYSTEM"] = "1"
	values["GIT_CONFIG_COUNT"] = "0"
	values["GIT_CONFIG_PARAMETERS"] = ""
	values["SSH_AUTH_SOCK"] = ""
	values["SSH_AGENT_PID"] = ""
	values["DBUS_SESSION_BUS_ADDRESS"] = ""
	values["GPG_AGENT_INFO"] = ""
	values["GPG_TTY"] = ""
	return agentMemoryReplaceEnv(env, values, true), nil
}

func agentMemoryInstallEnvironment(root string) error {
	env, err := agentMemoryEnvironment(root, os.Environ())
	if err != nil {
		return err
	}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "XDG_") {
			_ = os.Unsetenv(key)
		}
	}
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			if err := os.Setenv(key, value); err != nil {
				return err
			}
		}
	}
	return nil
}

func agentMemoryReplaceEnv(env []string, values map[string]string, clearXDG bool) []string {
	out := make([]string, 0, len(env)+len(values))
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		if _, replace := values[key]; replace || clearXDG && strings.HasPrefix(key, "XDG_") {
			continue
		}
		out = append(out, entry)
	}
	for key, value := range values {
		out = append(out, key+"="+value)
	}
	return out
}
