//go:build !windows

package client_test

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/client/clienttest"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
)

// Before the terminal is raw, Ctrl+C and Ctrl+\ come as SIGINT and SIGQUIT.
// Either one stops an attach that waits for the bridge, with a user error,
// and does not end the client (#84).
func TestSignalStopsAWaitingAttach(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGQUIT} {
		t.Run(sig.String(), func(t *testing.T) {
			tr := &clienttest.Transport{Silent: true}
			h := register(t, clienttest.NewMachine(client.MachineRunning, tr))
			done := make(chan error, 1)
			go func() {
				_, err := client.Attach(context.Background(), client.AttachOptions{Host: h, Session: "s", Settings: client.DefaultSettings()})
				done <- err
			}()
			// The attach catches the signals before it starts the bridge.
			hosttest.WaitFor(t, 5*time.Second, "the bridge to start", func() bool {
				return slices.ContainsFunc(tr.Remotes(), func(r string) bool { return strings.HasSuffix(r, " bridge") })
			})
			if err := syscall.Kill(os.Getpid(), sig); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				var ue *client.UserError
				if !errors.As(err, &ue) || !strings.Contains(ue.Msg, "stopped waiting for box") || !strings.Contains(ue.Hint, "host doctor box") {
					t.Fatalf("err = %v, want a user error saying the attach stopped waiting", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("the attach still waits for the bridge")
			}
		})
	}
}
