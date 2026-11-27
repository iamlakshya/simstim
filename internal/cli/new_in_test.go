package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/client"
)

func runNewInScene(t *testing.T, args ...string) error {
	t.Helper()
	cmd := newCmd()
	original := withLine(t.Context(), append([]string{"new"}, args...))
	cmd.SetContext(original)
	cmd.SetArgs(args)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()
	if cmd.Context() != original {
		t.Fatal("new --in replaced its caller's original context after returning")
	}
	return err
}

func TestNewInRejectsSandboxChangingFlagsBeforeHostOrInputWork(t *testing.T) {
	for _, flags := range [][]string{
		{"--wall", "none"},
		{"--image", "devtools"},
		{"--memory-max", "1G"},
		{"--secret", "API_KEY"},
		{"--seed", "missing-source"},
		{"--copy", "missing-source:/work/file"},
		{"--env", "KEY=value"},
		{"--profile", "untrusted"},
		{"--allow", "api.example.test"},
		{"--count", "2"},
		{"--setup-script"},
	} {
		t.Run(flags[0], func(t *testing.T) {
			newTestHome(t)
			args := append([]string{"unsaved", "--in", "root2345", "--detach"}, flags...)
			err := runNewInScene(t, args...)
			var user *client.UserError
			if !errors.As(err, &user) || user.Code != "member_flag" || user.Kind != client.KindUsage || !strings.Contains(user.Msg, flags[0]) {
				t.Fatalf("sandbox-changing flag reached host resolution or local inputs: %v", err)
			}
			if _, err := os.Lstat(filepath.Join(client.StateDir(), "hosts")); !os.IsNotExist(err) {
				t.Fatal("refused flag created a host/session operation", err)
			}
		})
	}
}

func TestNewInEmptyAndCrossHostParentsFailBeforeDial(t *testing.T) {
	newTestHome(t)
	if err := client.SaveHosts(t.Context(), []client.Host{{Name: "box"}}); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ parent, code string }{{"", "session_missing"}, {"other/root2345", "host_conflict"}} {
		err := runNewInScene(t, "box", "--in", test.parent, "--detach")
		var user *client.UserError
		if !errors.As(err, &user) || user.Code != test.code || user.Kind != client.KindUsage {
			t.Fatalf("parent %q reached Dial: %v", test.parent, err)
		}
	}
}

type newInStoppedMachine struct{ calls []string }

func (m *newInStoppedMachine) Status(ctx context.Context) (string, error) {
	m.calls = append(m.calls, "status")
	return client.MachineStopped, ctx.Err()
}
func (m *newInStoppedMachine) Start(context.Context) error {
	m.calls = append(m.calls, "start")
	return errors.New("unexpected start in generated lifecycle")
}
func (m *newInStoppedMachine) Stop(context.Context) error {
	m.calls = append(m.calls, "stop")
	return errors.New("unexpected stop in generated lifecycle")
}
func (m *newInStoppedMachine) Destroy(context.Context) error {
	m.calls = append(m.calls, "destroy")
	return errors.New("unexpected destroy in generated lifecycle")
}
func (m *newInStoppedMachine) Transport(context.Context, bool) (client.Transport, error) {
	m.calls = append(m.calls, "transport")
	return nil, errors.New("unexpected transport in generated lifecycle")
}

func TestNewInSameHostUsesSavedLifecycleWithoutWakingIt(t *testing.T) {
	for _, parent := range []string{"root2345", "box/root2345"} {
		t.Run(parent, func(t *testing.T) {
			newTestHome(t)
			machine := &newInStoppedMachine{}
			host := client.Host{Name: "box", Machine: &client.Machine{Provider: "new-in-observation", ID: "original-machine"}}
			client.RegisterProvider("new-in-observation", func(selected client.Host) (client.Lifecycle, error) {
				if selected.Name != host.Name || selected.Machine == nil || selected.Machine.ID != host.Machine.ID {
					t.Error("new --in replaced the selected saved host", selected.Name)
				}
				return machine, nil
			})
			if err := client.SaveHosts(t.Context(), []client.Host{host}); err != nil {
				t.Fatal(err)
			}
			err := runNewInScene(t, "box", "--in", parent, "--detach", "--json")
			var user *client.UserError
			if !errors.As(err, &user) || user.Code != "host_stopped" || !slices.Equal(machine.calls, []string{"status"}) {
				t.Fatalf("same-host member lookup woke, replaced or contacted a transport: %v %v", err, machine.calls)
			}
		})
	}
}

func TestNewInCommandNeedsDetachForJSONBeforeItResolvesAHost(t *testing.T) {
	newTestHome(t)
	err := runNewInScene(t, "unsaved", "--in", "root2345", "--json", "--", "printf", "--detach")
	var user *client.UserError
	if !errors.As(err, &user) || user.Code != "json_needs_detach" || user.Kind != client.KindUsage {
		t.Fatalf("a child command bypassed attachment rules or reached host resolution: %v", err)
	}
	before, after, ok := strings.Cut(user.Hint, " -- ")
	if !ok || !strings.Contains(before, "--detach") || after != "printf --detach" {
		t.Fatalf("detach hint changed the child's literal command: %q", user.Hint)
	}
}
