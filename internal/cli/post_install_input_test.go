//go:build !windows

package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/muesli/cancelreader"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/keyread"
	"github.com/iamlakshya/simstim/internal/onboard"
)

type interactiveLifecycle struct {
	client.Lifecycle
	mu    sync.Mutex
	calls []bool
}

func (s *interactiveLifecycle) Transport(ctx context.Context, interactive bool) (client.Transport, error) {
	s.mu.Lock()
	s.calls = append(s.calls, interactive)
	s.mu.Unlock()
	tr, err := s.Lifecycle.Transport(ctx, interactive)
	if err != nil {
		return nil, fmt.Errorf("open the test transport: %w", err)
	}
	return tr, nil
}

func (s *interactiveLifecycle) flags() []bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.calls)
}

func hostWithInteractiveSpy(t *testing.T) (*testHost, client.Host, *interactiveLifecycle) {
	t.Helper()
	th := newTestHost(t)
	lc, err := client.OpenMachine(th.ch)
	if err != nil {
		t.Fatal(err)
	}
	spy := &interactiveLifecycle{Lifecycle: lc}
	provider := "interactive-" + strings.ReplaceAll(t.Name(), "/", "-")
	client.RegisterProvider(provider, func(client.Host) (client.Lifecycle, error) { return spy, nil })
	h := th.ch
	machine := *h.Machine
	machine.Provider = provider
	h.Machine = &machine
	return th, h, spy
}

func TestGuidedAgentChecksUseNoInputAfterAStuckReader(t *testing.T) {
	for _, stuck := range []bool{false, true} {
		t.Run(fmt.Sprint(stuck), func(t *testing.T) {
			_, h, spy := hostWithInteractiveSpy(t)
			g := newGuide(context.Background(), guideOpts{}, nil)
			g.readerStuck = stuck
			if _, err := g.agentChecks(h); err != nil {
				t.Fatal(err)
			}
			if got := spy.flags(); !slices.Equal(got, []bool{!stuck}) {
				t.Errorf("post-session transport interactive=%v, want %v", got, !stuck)
			}
		})
	}
}

type installerInput struct {
	*heldKeyReader
	stuck bool
}

func (r installerInput) Cancel() bool {
	r.cancels.Add(1)
	if !r.stuck {
		r.letGo()
	}
	return !r.stuck
}

// These are real install, attach and post-install RPCs. Only the provider
// transport records the requested prompt policy, without a production hook.
func TestAgentInstallPostCheckRespectsItsAttachedReader(t *testing.T) {
	for _, stuck := range []bool{false, true} {
		t.Run(fmt.Sprint(stuck), func(t *testing.T) {
			th, h, spy := hostWithInteractiveSpy(t)
			terminal := hosttest.NewTerminal(t, 90, 30)
			gate := installInputGate(t, th.h.Home)
			a, ok := agents.Find("codex")
			if !ok || a.Setup == nil {
				t.Fatal("the agent table has no Codex setup")
			}
			setup := *a.Setup
			setup.Install = "IFS= read -r step < " + shellWord(gate.Name()) + "; printf 'INSTALLER_DONE\\n'"
			setup.Source = "https://example.invalid/install"
			a.Setup = &setup
			installFakeAgent(t, th.h.Home)
			r := installerInput{heldKeyReader: newHeldKeyReader(!stuck), stuck: stuck}
			oldIn, oldOut := os.Stdin, os.Stdout
			os.Stdin, os.Stdout = terminal.TTY, terminal.TTY
			restoreReader := client.SetAttachReader(func(io.Reader) (cancelreader.CancelReader, error) { return r, nil })
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			result := make(chan error, 1)
			ended := make(chan struct{})
			go func() {
				defer close(ended)
				result <- installAgent(ctx, h, a, true, true)
			}()
			t.Cleanup(func() {
				cancel()
				r.letGo()
				<-ended
				select {
				case <-r.started:
					<-r.readEnded
				default:
				}
				hosttest.WaitFor(t, 5*time.Second, "the installer reader to stop", func() bool {
					return keyread.CheckStuck(terminal.TTY) == nil
				})
				_ = keyread.Release(terminal.TTY)
				restoreReader()
				os.Stdin, os.Stdout = oldIn, oldOut
			})
			select {
			case <-r.started:
			case err := <-result:
				t.Fatalf("installer ended before its attached reader started: %v", err)
			case <-ctx.Done():
				t.Fatal("installer never began its attached input read")
			}
			if _, err := gate.WriteString("done\n"); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-result:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("installer did not finish its post-install check")
			}
			<-ended
			if got := spy.flags(); !slices.Equal(got, []bool{true, true, !stuck}) {
				t.Errorf("install/attach/post-check transport flags=%v, want [true true %v]", got, !stuck)
			}
			if r.closedDuringRead.Load() {
				t.Error("the attach reader closed before its active read ended")
			}
			r.letGo()
			<-r.readEnded
			hosttest.WaitFor(t, 5*time.Second, "the installer input reader to release", func() bool {
				return keyread.CheckStuck(terminal.TTY) == nil
			})
		})
	}
}

// A real guided install carries its attached reader's state into the next
// metadata check. The guide's own check method is used, without seeding that
// state or replacing the post-install RPC.
func TestGuidedInstallPostCheckUsesItsAttachedReader(t *testing.T) {
	for _, stuck := range []bool{false, true} {
		t.Run(fmt.Sprint(stuck), func(t *testing.T) {
			th, h, spy := hostWithInteractiveSpy(t)
			terminal := hosttest.NewTerminal(t, 90, 30)
			gate := installInputGate(t, th.h.Home)
			a, ok := agents.Find("codex")
			if !ok || a.Setup == nil {
				t.Fatal("the agent table has no Codex setup")
			}
			setup := *a.Setup
			setup.Install = "IFS= read -r step < " + shellWord(gate.Name()) + "; printf 'GUIDED_INSTALLER_DONE\n'"
			setup.Source = "https://example.invalid/install"
			a.Setup = &setup
			installFakeAgent(t, th.h.Home)
			r := installerInput{heldKeyReader: newHeldKeyReader(!stuck), stuck: stuck}
			oldIn, oldOut := os.Stdin, os.Stdout
			os.Stdin, os.Stdout = terminal.TTY, terminal.TTY
			restoreReader := client.SetAttachReader(func(io.Reader) (cancelreader.CancelReader, error) { return r, nil })
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			var ended chan struct{}
			t.Cleanup(func() {
				defer func() {
					restoreReader()
					os.Stdin, os.Stdout = oldIn, oldOut
				}()
				cancel()
				r.letGo()
				if ended != nil {
					<-ended
				}
				select {
				case <-r.started:
					<-r.readEnded
				default:
				}
				hosttest.WaitFor(t, 5*time.Second, "the guided installer reader to stop", func() bool {
					return keyread.CheckStuck(terminal.TTY) == nil
				})
				_ = keyread.Release(terminal.TTY)
			})
			g := newGuide(ctx, guideOpts{}, nil)
			var out syncBuffer
			g.in, g.out, g.tty = terminal.TTY, &out, true
			flow := onboard.NewFlow(h.Name, nil, g.log)
			flow.NoLogin = true
			if err := flow.Choose(a.Kind); err != nil {
				t.Fatal(err)
			}
			action := flow.Next()
			if action.Kind != onboard.ActInstall || action.Agent.Kind != a.Kind {
				t.Fatalf("the guided flow chose %+v, want the selected installer", action)
			}
			action.Agent, action.Args = a, onboard.InstallArgs(a)
			result := make(chan error, 1)
			ended = make(chan struct{})
			go func() {
				defer close(ended)
				result <- g.inSession(h, flow, action)
			}()

			select {
			case <-r.started:
			case err := <-result:
				t.Fatalf("guided installer ended before its attached reader started: %v", err)
			case <-ctx.Done():
				t.Fatal("guided installer never began its attached input read")
			}
			if _, err := gate.WriteString("done\n"); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-result:
				if err != nil {
					t.Fatalf("guided install: %v\n%s", err, out.String())
				}
			case <-ctx.Done():
				t.Fatal("guided installer did not finish its post-install check")
			}
			<-ended
			if got := spy.flags(); !slices.Equal(got, []bool{true, true, !stuck}) {
				t.Errorf("guided install/attach/post-check transport flags=%v, want [true true %v]", got, !stuck)
			}
			if r.closedDuringRead.Load() {
				t.Error("the guided attach reader closed before its active read ended")
			}
			r.letGo()
			<-r.readEnded
			hosttest.WaitFor(t, 5*time.Second, "the guided installer input reader to release", func() bool {
				return keyread.CheckStuck(terminal.TTY) == nil
			})
		})
	}
}

func installInputGate(t *testing.T, home string) *os.File {
	t.Helper()
	path := filepath.Join(home, "installer-input-gate")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	gate, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = gate.Close() })
	return gate
}

func installFakeAgent(t *testing.T, home string) {
	t.Helper()
	dir := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "codex"), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
}
