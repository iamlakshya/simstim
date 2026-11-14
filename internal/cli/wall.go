package cli

import (
	"context"
	"sort"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/client"
)

// Resolve the actual wall before creating H5's immutable local intent and
// before a password manager can fetch a refused secret.
func prepareWallStart(cmd *cobra.Command, c *client.Conn, h client.Host, a client.NewArgs, untrusted bool, secrets, expose []string) (client.NewArgs, error) {
	var err error
	a, err = c.PrepareNewPolicy(cmd.Context(), a)
	if err != nil {
		return a, err
	}
	a, err = c.ResolveNewWall(cmd.Context(), a)
	if err != nil {
		return a, err
	}
	if message := client.PolicyWallNote(h, a.Layers); message != "" {
		note(cmd, "wall_floor_raised", message)
	}
	level := a.Wall
	if level == "" {
		level = "none"
	}
	request, err := client.NewWallGrantRequest(untrusted, a.SSHAgent, level, expose)
	if err != nil {
		return a, err
	}
	cmd.SetContext(client.WithGrantRequest(cmd.Context(), request))
	layerSecrets, err := client.PolicySecretNames(a.Layers)
	if err != nil {
		return a, err
	}
	effective := h
	effective.Secrets = nil
	names := client.SecretNames(effective, append(append(append([]string(nil), layerSecrets...), secrets...), expose...))
	err = grantQuestions(cmd, func(ctx context.Context) error {
		if err := c.CheckNewGrants(ctx, names, a.SSHAgent); err != nil {
			return err
		}
		if err := c.CheckNewPolicyGrants(ctx, a.Layers); err != nil {
			return err
		}
		var err error
		a.Secrets, err = client.FetchHostSecrets(ctx, h, names, secretTerminal(cmd))
		return err
	})
	return a, err
}

// These notes end before attach starts. The attach pump owns all attached
// payload bytes, and no launcher notice is written into that payload.
func wallStartNotes(cmd *cobra.Command, s client.Session) {
	if s.Wall == nil {
		return
	}
	if s.Wall.DowngradedFrom != "" {
		note(cmd, "wall_downgraded", s.Wall.Level+", below the floor "+s.Wall.DowngradedFrom+".")
	}
	if s.Wall.Level == "none" {
		return
	}
	if s.Wall.Level == "process" {
		note(cmd, "wall_process", "process shares your account.")
	}
	names := append([]string(nil), s.Secrets...)
	sort.Strings(names)
	for _, name := range names {
		note(cmd, "secret_exposed", "exposed: "+client.CleanLine(name))
	}
}
