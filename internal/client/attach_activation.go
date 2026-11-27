package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/iamlakshya/simstim/internal/brand"
)

// AttachArmAttemptV1 is one fixed local arm operation on the connection that
// will attach. It exposes no names, values, Source or transport replacement.
// Its Run method always executes the actual canonical arm facade.
type AttachArmAttemptV1 struct {
	mu           sync.Mutex
	request      *GrantRequest
	conn         *Conn
	id           string
	ctx          context.Context
	cancel       context.CancelFunc
	terminal     bool
	busy, closed bool
	fatal        error
	work         sync.WaitGroup
}

// AttachArmQuestionsV1 retries only this fixed operation using the front end's
// existing scoped questions. Its return value cannot approve an operation:
// the client independently calls the real facade again before the handshake.
type AttachArmQuestionsV1 func(context.Context, *AttachArmAttemptV1) error
type attachArmQuestionsKeyV1 struct{}
type attachConfigWarningKeyV1 struct{}

// dialAttach gives each connection, including a reconnect, a fresh arm request
// before Dial. Its values and question state end before the terminal pump.
func dialAttach(ctx context.Context, h Host, session string, interactive bool) (connection *Conn, prepared *attachedAgent, returned error) {
	if err := checkSessionID(h.Name, session); err != nil {
		return nil, nil, err
	}
	request, err := NewSecretArmGrantRequest(session)
	if err != nil {
		return nil, nil, err
	}
	owned := WithGrantRequest(ctx, request)
	defer func() {
		returned = errors.Join(returned, request.Close())
		if returned != nil {
			if prepared != nil {
				prepared.Close()
			}
			if connection != nil {
				connection.Close()
			}
			connection, prepared = nil, nil
		}
	}()
	// This negative read chooses only a value-free attach. A later settings
	// change during an authorised arm remains a failure on its original R.
	if _, policyErr := ReadPolicySettings(); policyErr != nil {
		connection, err = Dial(owned, h, interactive)
		if err != nil {
			return nil, nil, err
		}
		if err := connection.CheckSavedSource(owned); err != nil {
			return connection, nil, err
		}
		warnAttachConfigReadOnlyV1(owned, policyErr)
		return connection, nil, nil
	}
	connection, prepared, err = dialAttachTransport(owned, h, session, interactive)
	if err != nil {
		return connection, prepared, err
	}
	if err := armAttachConnectionV1(owned, connection, request, session, interactive); err != nil {
		return connection, prepared, err
	}
	return connection, prepared, nil
}

func (*AttachArmAttemptV1) Format(f fmt.State, _ rune) {
	_, _ = io.WriteString(f, "<attach arm operation>")
}

// WithAttachArmQuestionsV1 installs a UI retry service for the fixed attach
// operation. A nil return never replaces its actual final delivery gates.
func WithAttachArmQuestionsV1(ctx context.Context, questions AttachArmQuestionsV1) context.Context {
	return context.WithValue(ctx, attachArmQuestionsKeyV1{}, questions)
}

// WithAttachConfigWarningShownV1 records only an already displayed warning.
// It grants no arm, value selection or successful configuration read.
func WithAttachConfigWarningShownV1(ctx context.Context) context.Context {
	return context.WithValue(ctx, attachConfigWarningKeyV1{}, true)
}

// Context keeps terminal answers on this attempt's original private request.
func (a *AttachArmAttemptV1) Context(ctx context.Context) context.Context {
	if a == nil || a.request == nil {
		return ctx
	}
	return WithGrantRequest(ctx, a.request)
}

func (a *AttachArmAttemptV1) Run(ctx context.Context) (returned error) {
	if a == nil || ctx == nil || a.request == nil || a.conn == nil || a.ctx == nil || a.cancel == nil {
		return grantOperationError()
	}
	a.mu.Lock()
	if a.closed || a.busy || a.fatal != nil {
		err := a.fatal
		if err == nil {
			err = grantOperationError()
		}
		a.mu.Unlock()
		return err
	}
	a.busy = true
	a.work.Add(1)
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.busy = false
		var question *ConfirmError
		if returned != nil && !errors.As(returned, &question) {
			a.fatal = returned
		}
		a.mu.Unlock()
		a.work.Done()
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	owned, cancel := context.WithCancel(a.ctx)
	stop := context.AfterFunc(ctx, cancel)
	defer stop()
	defer cancel()
	if !InputAllowed(ctx) {
		owned = WithoutInput(owned)
	}
	if err := a.conn.CheckSavedSource(owned); err != nil {
		return err
	}
	_, err := a.conn.ArmSecrets(owned, a.id, a.terminal && InputAllowed(owned))
	return err
}

func (a *AttachArmAttemptV1) finish() error {
	a.mu.Lock()
	a.closed = true
	a.cancel()
	a.mu.Unlock()
	a.work.Wait()
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.fatal
}

func armAttachConnectionV1(ctx context.Context, c *Conn, request *GrantRequest, id string, interactive bool) error {
	owned := WithGrantRequest(ctx, request)
	if err := c.CheckSavedSource(owned); err != nil {
		return err
	}
	_, err := c.ArmSecrets(owned, id, interactive && InputAllowed(owned))
	if err == nil {
		return c.CheckSavedSource(owned)
	}
	var question *ConfirmError
	if !errors.As(err, &question) {
		return err
	}
	questions, _ := ctx.Value(attachArmQuestionsKeyV1{}).(AttachArmQuestionsV1)
	if !interactive || !InputAllowed(owned) || questions == nil {
		return attachArmQuestionRefusedV1(c, id, err)
	}
	questionCtx, cancel := context.WithCancel(owned)
	attempt := &AttachArmAttemptV1{request: request, conn: c, id: id, ctx: questionCtx, cancel: cancel, terminal: true}
	runnerErr := questions(questionCtx, attempt)
	fatal := attempt.finish()
	if runnerErr != nil {
		return runnerErr
	}
	if fatal != nil {
		return fatal
	}
	// Always execute actual delivery/readiness and Source gates ourselves.
	// A runner that skipped Run or returned nil has approved nothing.
	_, err = c.ArmSecrets(owned, id, true)
	if err != nil {
		return attachArmQuestionRefusedV1(c, id, err)
	}
	return c.CheckSavedSource(owned)
}

func warnAttachConfigReadOnlyV1(ctx context.Context, err error) {
	if shown, _ := ctx.Value(attachConfigWarningKeyV1{}).(bool); shown {
		return
	}
	warn(ctx, "config_ignored", "The local policy cannot be checked; attaching without sending secrets or policy layers.", "Fix config.toml with "+brand.Name+" config check before arming secrets.")
	DebugLog().Debug("read-only attach after policy configuration failure", "err", err)
}

type attachArmRefusalV1 struct {
	*UserError
	cause error
}

func (e *attachArmRefusalV1) Unwrap() error { return e.cause }
func attachArmQuestionRefusedV1(c *Conn, id string, err error) error {
	var question *ConfirmError
	if !errors.As(err, &question) {
		return err
	}
	return &attachArmRefusalV1{UserError: &UserError{Msg: "this connection needs a fresh terminal answer before arming the session's secrets", Hint: "Reattach in an interactive terminal: " + brand.Name + " attach " + ShellWord(c.Host.Name+"/"+id), Code: "arm_confirmation", Kind: KindRefused}, cause: err}
}
