package cli

import (
	"context"
	"errors"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

func TestWalledForwardWithoutInputChoosesNoClaimedPort(t *testing.T) {
	previous := sessionPorts
	sessionPorts = func(context.Context, *client.Conn, string) ([]client.Port, error) {
		return []client.Port{{Port: 3000, Claimed: true}}, nil
	}
	t.Cleanup(func() { sessionPorts = previous })
	command := forwardCmd()
	command.SetContext(context.Background())
	command.Flags().Bool("no-input", true, "test input refusal")
	_, err := selectForwardPort(command, nil, client.Host{Name: "box"}, &client.Session{ID: "port2345", Wall: &api.WallInfo{Level: "process"}})
	var user *client.UserError
	if !errors.As(err, &user) || user.Code != "port_required" || user.Kind != client.KindNeedsAnswer {
		t.Fatalf("no-input forward selected a guest port: %v", err)
	}
}
