package tui

import (
	tea "charm.land/bubbletea/v2"
	"context"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
	"time"
)

func (m *Model) startSessionWithSource(h *hostConn, r startReq) tea.Cmd {
	c, be, originalHost, kept, cols, rows := h.conn, m.be, h.host, m.settings, m.width, m.height
	read := m.beginSettingsRead()
	workContext := m.backgroundContext()
	if r.selected != nil {
		workContext = r.selected.currentAttempt().Context()
	}
	return m.bgCmdFrom(workContext, func(ctx context.Context) (message tea.Msg) {
		if r.selected == nil {
			defer func() { message = m.finishStartGrant(ctx, message) }()
		}
		settings, read := readSettings(be, kept, read)
		if _, err := client.ReadPolicySettings(); err != nil {
			return startedMsg{startReq: r, err: err, settingsRead: read}
		}
		hostClone := r.args.Cwd != ""
		if r.grants == nil {
			r.wallIntent = r.args.Wall
			r.grants = client.NewGrantRequest(false, r.args.SSHAgent)
			m.grantOwners.add(r.grants)
		}
		cctx, cancel := context.WithTimeout(client.WithGrantRequest(ctx, r.grants), 60*time.Second)
		defer cancel()
		if r.selected == nil {
			endAttempt, err := m.grantOwners.begin(r.grants, cctx, cancel)
			if err != nil {
				return startedMsg{startReq: r, err: err, settingsRead: read}
			}
			defer endAttempt()
		} else if r.selected.currentAttempt().Request() != r.grants {
			return startedMsg{startReq: r, err: changedNewSelection(), settingsRead: read}
		}
		args, err := client.PrepareNewSessionArgs(cctx, r.args, cols, rows)
		if err != nil {
			return startedMsg{startReq: r, err: err, settingsRead: read}
		}
		r.args = args
		if r.inputOptions == nil {
			options := startInputOptions(r.args, hostClone)
			r.inputOptions = &options
		}
		if !r.inputOptions.HostClone {
			r.args.Cwd = ""
		}
		if planner, ok := unclean(c).(interface {
			PrepareNewInputPolicy(context.Context, client.NewArgs, client.NewInputOptions) (client.NewArgs, error)
		}); ok {
			err = m.grantQuestions(cctx, func(ctx context.Context) error {
				next, stepErr := planner.PrepareNewInputPolicy(ctx, r.args, *r.inputOptions)
				if stepErr == nil {
					r.args = next
				}
				return stepErr
			})
			if err != nil {
				return startedMsg{startReq: r, err: err, settingsRead: read}
			}
		}
		if planner, ok := unclean(c).(interface {
			PrepareNewPolicy(context.Context, client.NewArgs) (client.NewArgs, error)
		}); ok {
			err = m.grantQuestions(cctx, func(ctx context.Context) error {
				next, stepErr := planner.PrepareNewPolicy(ctx, r.args)
				if stepErr == nil {
					r.args = next
				}
				return stepErr
			})
			if err != nil {
				return startedMsg{startReq: r, err: err, settingsRead: read}
			}
		}
		if planner, ok := unclean(c).(interface {
			ResolveNewWall(context.Context, client.NewArgs) (client.NewArgs, error)
		}); ok {
			err = m.grantQuestions(cctx, func(ctx context.Context) error {
				next, stepErr := planner.ResolveNewWall(ctx, r.args)
				if stepErr == nil {
					r.args = next
				}
				return stepErr
			})
			if err != nil {
				return startedMsg{startReq: r, err: err, settingsRead: read}
			}
		} else if r.args.Wall != "" {
			return startedMsg{startReq: r, err: &client.UserError{Msg: "this host connection cannot select a wall",
				Hint: "Update the client and host helper, then try again.", Code: "wall_helper_old"}, settingsRead: read}
		}
		level := r.args.Wall
		if level == "" {
			level = "none"
		}
		if err := r.grants.BindNewWall(cctx, level, nil); err != nil {
			return startedMsg{startReq: r, err: err, settingsRead: read}
		}
		if planner, ok := unclean(c).(interface {
			PrepareNewInputs(context.Context, client.NewArgs) (client.NewArgs, error)
		}); ok {
			err = m.grantQuestions(cctx, func(ctx context.Context) error {
				next, stepErr := planner.PrepareNewInputs(ctx, r.args)
				if stepErr == nil {
					r.args = next
				}
				return stepErr
			})
			if err != nil {
				return startedMsg{startReq: r, err: err, settingsRead: read}
			}
		}
		if r.args.Wall == "microvm" && r.vm == nil {
			planner, ok := unclean(c).(interface {
				PrepareNewVM(context.Context, client.NewArgs, string) (*client.VMNeedPlanV1, error)
			})
			if !ok {
				return startedMsg{startReq: r, err: &client.UserError{Msg: "this connection cannot prepare a microvm", Hint: "Update the client and host helper, then try again.", Code: "vm_prepare", Kind: client.KindRefused}, settingsRead: read}
			}
			if err := m.grantQuestions(cctx, func(ctx context.Context) error {
				r.vm, err = planner.PrepareNewVM(ctx, r.args, r.wallIntent)
				return err
			}); err != nil {
				return startedMsg{startReq: r, err: err, settingsRead: read}
			}
		}
		if planner, ok := unclean(c).(interface {
			PrepareNewSecrets(context.Context, client.NewArgs, ...*client.VMNeedPlanV1) (client.NewArgs, error)
		}); ok {
			err = m.grantQuestions(cctx, func(ctx context.Context) error {
				var next client.NewArgs
				var stepErr error
				if r.vm != nil {
					next, stepErr = planner.PrepareNewSecrets(ctx, r.args, r.vm)
				} else {
					next, stepErr = planner.PrepareNewSecrets(ctx, r.args)
				}
				if stepErr == nil {
					r.args = next
				}
				return stepErr
			})
			if err != nil {
				return startedMsg{startReq: r, err: err, settingsRead: read}
			}
		} else if r.args.Wall != "" && r.args.Wall != "none" {
			return startedMsg{startReq: r, err: &client.UserError{Msg: "this connection cannot prepare sandbox inputs", Hint: "Update the client and host helper, then try again.", Code: "secret_helper_old", Kind: client.KindRefused}, settingsRead: read}
		}
		effectiveNames := client.SecretNames(originalHost, nil)
		if len(r.args.Layers) > 0 {
			effectiveNames, err = client.PolicySecretNames(r.args.Layers)
			if err != nil {
				return startedMsg{startReq: r, err: err, settingsRead: read}
			}
		}
		if err := m.grantQuestions(cctx, func(ctx context.Context) error {
			return c.CheckNewGrants(ctx, effectiveNames, r.args.SSHAgent)
		}); err != nil {
			return startedMsg{startReq: r, err: err, settingsRead: read}
		}
		if planner, ok := unclean(c).(interface {
			CheckNewPolicyGrants(context.Context, []api.Layer) error
		}); ok {
			if err := m.grantQuestions(cctx, func(ctx context.Context) error {
				return planner.CheckNewPolicyGrants(ctx, r.args.Layers)
			}); err != nil {
				return startedMsg{startReq: r, err: err, settingsRead: read}
			}
		}
		var warn *client.UserError
		if r.args.Setup == "" && !r.answered {
			answers := be.SetupAnswers(r.host)
			answers.Apply(&r.args)
			r.answered, r.answers, warn = true, answers, answers.Warning
		}
		// One fetch of the secrets for each start. A question about the
		// start keeps them for the start after the answer.
		if r.args.Secrets == nil {
			var secrets map[string]api.Secret
			err := m.grantQuestions(cctx, func(ctx context.Context) error {
				var err error
				secrets, err = be.Secrets(ctx, settings, originalHost, effectiveNames)
				return err
			})
			if err != nil {
				return startedMsg{startReq: r, err: err, warn: warn, settingsRead: read}
			}
			r.args.Secrets = secrets
		}
		var s *client.Session
		err = m.grantQuestions(cctx, func(ctx context.Context) error {
			var err error
			if r.selected != nil {
				r.selectedNewCalled = true
				s, err = r.selected.currentAttempt().NewSession(ctx, r.args)
			} else {
				s, err = c.NewSession(ctx, r.args)
			}
			return err
		})
		return startedMsg{startReq: r, s: s, err: err, warn: warn, settingsRead: read}
	})
}
