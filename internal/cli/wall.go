package cli

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/client"
)

// prepareWallStart binds policy, wall, inputs and values to the same request.
// Every actual preparation step owns its original bounded cleanup deadline.
func prepareWallStart(cmd *cobra.Command, c *client.Conn, h client.Host, a client.NewArgs, request *client.GrantRequest, options client.NewInputOptions, secrets, expose []string) (client.NewArgs, error) {
	var err error
	intent := a.Wall
	if err = newStartStep(cmd, func(ctx context.Context) error {
		a, err = c.PrepareNewInputPolicy(ctx, a, options)
		if err != nil {
			return err
		}
		a, err = c.PrepareNewPolicy(ctx, a)
		if err != nil {
			return err
		}
		a, err = c.ResolveNewWall(ctx, a)
		return err
	}); err != nil {
		return a, err
	}
	if message := client.PolicyWallNote(h, a.Layers); message != "" {
		note(cmd, "wall_floor_raised", message)
	}
	level := a.Wall
	if level == "" {
		level = "none"
	}
	if err = request.BindNewWall(cmd.Context(), level, expose); err != nil {
		return a, err
	}
	if err = newStartStep(cmd, func(ctx context.Context) error {
		return grantQuestions(cmd, func(context.Context) error { a, err = c.PrepareNewInputs(ctx, a); return err })
	}); err != nil {
		return a, err
	}
	var vm *client.VMNeedPlanV1
	if err = newStartStep(cmd, func(ctx context.Context) error {
		return grantQuestions(cmd, func(context.Context) error {
			if a.Wall == "microvm" {
				vm, err = c.PrepareNewVM(ctx, a, intent)
				if err != nil {
					return err
				}
			}
			if vm != nil {
				a, err = c.PrepareNewSecrets(ctx, a, vm)
			} else {
				a, err = c.PrepareNewSecrets(ctx, a)
			}
			return err
		})
	}); err != nil {
		return a, err
	}
	layerSecrets, err := client.PolicySecretNames(a.Layers)
	if err != nil {
		return a, err
	}
	effective := h
	effective.Secrets = nil
	names := client.SecretNames(effective, append(append(append([]string(nil), layerSecrets...), secrets...), expose...))
	err = newStartStep(cmd, func(ctx context.Context) error {
		return grantQuestions(cmd, func(context.Context) error {
			if err := c.CheckNewGrants(ctx, names, a.SSHAgent); err != nil {
				return err
			}
			if err := c.CheckNewPolicyGrants(ctx, a.Layers); err != nil {
				return err
			}
			a.Secrets, err = client.FetchHostSecrets(ctx, h, names, secretTerminal(cmd))
			return err
		})
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
	if s.Wall.Level == "none" && s.Wall.DowngradedFrom == "" {
		return
	}
	if s.Wall.Level == "process" {
		note(cmd, "wall_process", "process shares your account.")
	}
	presentation := client.PresentSessionSecrets(s)
	for _, name := range presentation.Exposed {
		note(cmd, "secret_exposed", "exposed: "+client.CleanLine(name))
	}
	for _, name := range presentation.Unknown {
		note(cmd, "secret_delivery_unknown", "secret delivery unknown: "+client.CleanLine(name))
	}
}
