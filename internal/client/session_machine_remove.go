package client

import (
	"context"
	"errors"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
)

// SessionMachineRemovalV1 owns one explicit outside rm operation. Saved
// metadata restricts its target; only the actual Source/OP, accepted connection
// and positive delete/absence or provider status authorise its final steps.
type SessionMachineRemovalV1 struct {
	ctx           context.Context
	removal       *SavedHostRemovalV1
	lifecycle     Lifecycle
	gone, deleted bool
	conn          *Conn
}

// OpenSessionMachineRemovalV1 selects the saved original Source before opening
// one lifecycle. It never obtains destruction authority from a cached row or
// removal receipt and retains its actual host OP until Close.
func OpenSessionMachineRemovalV1(ctx context.Context, hostName string) (*SessionMachineRemovalV1, error) {
	removal, err := OpenSavedHostRemovalV1(ctx, hostName)
	if err != nil {
		return nil, err
	}
	host := removal.Host()
	if !IsSessionMachineV1(host) || !api.ValidSessionID(host.Machine.SessionID) {
		return nil, errors.Join(sessionMachineErrorV1("the saved host has no acknowledged machine-session ownership"), removal.Close())
	}
	lifecycle, err := OpenMachine(host)
	if err != nil {
		return nil, errors.Join(err, removal.Close())
	}
	op := &SessionMachineRemovalV1{ctx: removal.Context(), removal: removal, lifecycle: lifecycle}
	return op, nil
}

// Context retains the original physical host operation for removal's calls.
func (o *SessionMachineRemovalV1) Context() context.Context { return o.ctx }

// Host returns the saved entry bound to this removal's original Source.
func (o *SessionMachineRemovalV1) Host() Host { return o.removal.Host() }

// Close releases the original operation without granting another removal.
func (o *SessionMachineRemovalV1) Close() error {
	if o == nil {
		return nil
	}
	return o.removal.Close()
}

func (o *SessionMachineRemovalV1) check(ctx context.Context) error {
	if o == nil || o.removal == nil || o.lifecycle == nil {
		return sessionMachineErrorV1("the machine removal owner is closed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return o.removal.Check(ctx)
}

// Status asks the same provider lifecycle and revalidates the original Source.
func (o *SessionMachineRemovalV1) Status() (string, error) {
	if err := o.check(o.ctx); err != nil {
		return "", err
	}
	state, err := o.lifecycle.Status(o.ctx)
	if err != nil {
		return "", err
	}
	if err := o.check(o.ctx); err != nil {
		return "", err
	}
	o.gone = state == MachineMissing
	return state, nil
}

func (o *SessionMachineRemovalV1) checkConn(ctx context.Context, c *Conn) error {
	if err := o.check(ctx); err != nil {
		return err
	}
	return o.removal.CheckConnection(ctx, c)
}

// DeleteRoot owns the actual host delete result and post-delete empty list.
func (o *SessionMachineRemovalV1) DeleteRoot(ctx context.Context, c *Conn, id, name string) (DeleteResult, error) {
	if err := o.checkConn(ctx, c); err != nil {
		return DeleteResult{}, err
	}
	if id != o.removal.host.Machine.SessionID {
		return DeleteResult{}, sessionMachineErrorV1("the selected session does not own this provider machine")
	}
	result, err := c.DeleteNamed(ctx, id, name)
	if err != nil {
		return result, err
	}
	if err := o.ConfirmAbsentRoot(ctx, c); err != nil {
		return result, err
	}
	return result, nil
}

// ConfirmAbsentRoot reads the accepted connection itself; a caller's list or
// session_not_found DTO cannot claim that another session's machine is empty.
func (o *SessionMachineRemovalV1) ConfirmAbsentRoot(ctx context.Context, c *Conn) error {
	if err := o.checkConn(ctx, c); err != nil {
		return err
	}
	var rows []Session
	err := c.Call(ctx, "ls", nil, &rows)
	if err != nil {
		return err
	}
	if err := o.checkConn(ctx, c); err != nil {
		return err
	}
	if len(rows) != 0 {
		return &UserError{Msg: "the provider machine still has sessions, so it was kept", Hint: brand.Name + " ls " + ShellQuote(o.removal.host.Name) + ", then remove those sessions first.", Code: "machine_sessions", Kind: KindRefused}
	}
	o.deleted = true
	o.conn = c
	return nil
}

// DestroyAndForget reconciles the same lifecycle, checks Source before every
// destructive step and forgets only after the provider confirms absence.
func (o *SessionMachineRemovalV1) DestroyAndForget() error {
	if o != nil && o.removal != nil && o.removal.attempt != nil && o.removal.attempt.committed {
		return o.removal.Retry(o.ctx)
	}
	if err := o.check(o.ctx); err != nil {
		return err
	}
	if !o.gone && !o.deleted {
		return sessionMachineErrorV1("the original session delete has not completed")
	}
	state, err := o.Status()
	if err != nil {
		return err
	}
	if state != MachineMissing {
		if !o.deleted {
			return sessionMachineErrorV1("the provider machine reappeared before its original session delete was verified")
		}
		if err := o.check(o.ctx); err != nil {
			return err
		}
		if err := o.lifecycle.Destroy(o.ctx); err != nil {
			return err
		}
		state, err = o.Status()
		if err != nil {
			return err
		}
		if state != MachineMissing {
			return &UserError{Msg: "the provider has not confirmed that the session's machine is gone", Hint: brand.Name + " host rm " + ShellQuote(o.removal.host.Name) + " --destroy --confirm " + ShellQuote(o.removal.host.Name), Code: "destroy_unconfirmed", Kind: KindRefused}
		}
	}
	if o.conn != nil {
		if err := joinSessionMachineConnV1(o.ctx, o.conn); err != nil {
			return err
		}
	}
	return o.removal.Forget(o.ctx)
}
