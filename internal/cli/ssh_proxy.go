package cli

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

func sshProxyCmd() *cobra.Command {
	return &cobra.Command{Use: "ssh-proxy <session>.<host>." + brand.Name, Short: "Serve one sandbox SSH connection on standard input and output", Example: "  " + brand.Name + " ssh-proxy auth-fix.my-vps." + brand.Name, Hidden: true, Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) (returned error) {
		session, host, err := client.ParseEditorSSHAlias(args[0])
		if err != nil {
			return err
		}
		if _, err = client.ReadPolicySettings(); err != nil {
			return err
		}
		// The stdio stream is already SSH protocol. Loading the exact saved
		// route and dialing it never asks a question or reads these bytes.
		address, err := resolveAddress(cmd, host+"/"+session, false)
		if err != nil {
			return err
		}
		key, err := client.LoadSSHHostKey(cmd.Context())
		if err != nil {
			return err
		}
		defer func() { returned = errors.Join(returned, key.Close()) }()
		start, end := context.WithTimeout(cmd.Context(), 60*time.Second)
		connection, err := client.Dial(start, address.host, false)
		end()
		if err != nil {
			return err
		}
		defer connection.Close()
		rows, err := listSessions(cmd.Context(), connection)
		if err != nil {
			return err
		}
		selected, err := findEditorSessionV1(rows, session, host)
		if err != nil {
			return err
		}
		if err = connection.CheckSavedSource(cmd.Context()); err != nil {
			return err
		}
		if client.Ended(*selected) || selected.State == api.StateStopped {
			// This is the SAME real resume workflow and accepted connection.
			// No grant question is answered from SSH bytes; terminal=false also
			// prevents a value command from reading the protocol descriptor.
			request := client.NewGrantRequest(false, false)
			wake := client.WithGrantRequest(cmd.Context(), request)
			_, _, wakeErr := resumeRacing(wake, found{host: address.host, conn: connection, s: selected}, client.ResumeArgs{}, nil, false)
			closeErr := request.Close()
			if err = errors.Join(wakeErr, closeErr); err != nil {
				return &client.UserError{Msg: "the sandbox could not wake without interactive input: " + client.CleanLine(err.Error()), Hint: "Run " + brand.Name + " resume " + client.ShellWord(host+"/"+selected.ID) + " in a terminal, then reconnect with SSH.", Code: "ssh_wake", Kind: client.KindRefused}
			}
		}
		return connection.ServeEditorSSH(cmd.Context(), selected.ID, os.Stdin, os.Stdout, key)
	}}
}

func findEditorSessionV1(rows []client.Session, reference, host string) (*client.Session, error) {
	var selected *client.Session
	for at := range rows {
		row := &rows[at]
		if row.ID != reference && row.Name != reference {
			continue
		}
		if selected != nil {
			return nil, &client.UserError{Msg: "the SSH session alias is ambiguous", Hint: "Use the complete session ID in its SSH alias.", Code: "ambiguous_session", Kind: client.KindUsage}
		}
		selected = row
	}
	if selected == nil {
		return nil, &client.UserError{Msg: "the SSH alias does not name an exact session on the saved host", Hint: brand.Name + " ls " + client.ShellWord(host), Code: "session_not_found", Kind: client.KindNotFound}
	}
	return selected, nil
}
