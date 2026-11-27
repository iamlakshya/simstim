package cli

import (
	"context"
	"errors"
	"time"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/client"
)

func removeMachineSessionV1(cmd *cobra.Command, a address, yes bool) (failure error) {
	bounded, cancel := context.WithTimeout(cmd.Context(), 5*time.Minute)
	defer cancel()
	operation, err := client.OpenSessionMachineRemovalV1(bounded, a.host.Name)
	if err != nil {
		return err
	}
	defer func() { failure = errors.Join(failure, operation.Close()) }()
	host := operation.Host()
	if !client.SameEntry(host, a.host) {
		return &client.UserError{Msg: "the session's machine changed before removal", Hint: "List its sessions again, then repeat rm.", Code: "host_changed", Kind: client.KindRefused}
	}
	selected, err := inspectMachineRemovalV1(cmd, a, operation)
	if err != nil {
		return err
	}
	conn, session, absent := selected.conn, selected.session, selected.absent
	if conn != nil {
		defer conn.Close()
	}
	if selected.member {
		conn.Close()
		if err := operation.Close(); err != nil {
			return err
		}
		return removeOrdinarySessionV1(cmd, a, yes)
	}
	ctx := operation.Context()
	wanted := host.Machine.SessionID
	asked, err := confirmMachineRemovalV1(cmd, host, session, yes)
	if err != nil {
		return err
	}
	if conn != nil {
		f := found{host: host, conn: conn, s: &session}
		if asked && !absent {
			afterQuestion(f)
			if err := stillTheSame(ctx, f); err != nil {
				return err
			}
		}
		if absent {
			if err := operation.ConfirmAbsentRoot(ctx, conn); err != nil {
				return err
			}
		} else {
			beforeDelete(f)
			if _, err := operation.DeleteRoot(ctx, conn, session.ID, session.Name); err != nil {
				return err
			}
		}
		conn.Close()
	}
	if err := operation.DestroyAndForget(); err != nil {
		return err
	}
	client.ForgetSession(host.Name, wanted)
	say(cmd, "Deleted %s (%s), its provider machine and its disk.\n", shown(session.Name), shown(wanted))
	return nil
}

// This view carries a selection only. The typed operation owns every actual
// absence, deletion and provider reconciliation check.
type machineRemovalSessionV1 struct {
	conn           *client.Conn
	session        client.Session
	absent, member bool
}

func inspectMachineRemovalV1(cmd *cobra.Command, address address, operation *client.SessionMachineRemovalV1) (selected machineRemovalSessionV1, returned error) {
	host := operation.Host()
	wanted := host.Machine.SessionID
	originalRef := address.ref == wanted || address.ref == host.Machine.Session
	selected.session = client.Session{ID: wanted, Name: host.Machine.Session}
	state, err := operation.Status()
	if err != nil {
		return selected, err
	}
	selected.absent = state == client.MachineMissing
	if selected.absent {
		if !originalRef {
			return selected, &client.UserError{Msg: "the provider machine is gone and that session cannot be checked", Hint: "Remove it using its original session name or ID.", Code: "session_not_found", Kind: client.KindNotFound}
		}
		return selected, nil
	}
	selected.conn, err = client.Dial(operation.Context(), host, true)
	if err != nil {
		return selected, err
	}
	defer func() {
		if returned != nil {
			selected.conn.Close()
		}
	}()
	rows, err := listSessions(operation.Context(), selected.conn)
	if err != nil {
		return selected, err
	}
	session, err := address.find(cmd, rows)
	if err != nil {
		var userErr *client.UserError
		if !originalRef || !errors.As(err, &userErr) || userErr.Code != "session_not_found" {
			return selected, err
		}
		selected.absent = true
		return selected, nil
	}
	selected.session, selected.member = *session, session.ID != wanted
	return selected, nil
}

func confirmMachineRemovalV1(cmd *cobra.Command, host client.Host, session client.Session, yes bool) (bool, error) {
	if yes {
		return false, nil
	}
	p := sessionPrompts(cmd, host.Name, host.Machine.SessionID)
	if !p.on && oldRmWithoutYes != nil {
		warnOld(cmd, oldRmWithoutYes)
		return false, nil
	}
	ok, err := p.confirm(qDelete, "Delete "+shown(nameOf(session))+" on "+shown(host.Name)+"?\nThis also destroys its provider machine and its disk.\nDelete it?", "a yes to delete the session and its machine")
	if err != nil {
		return true, err
	}
	if !ok {
		return true, cancelled()
	}
	return true, nil
}
