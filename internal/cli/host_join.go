package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/join"
	"github.com/iamlakshya/simstim/internal/join/code"
)

var joinPrompts = prompts

var qJoinChanges = &question{cmd: "host join", flag: "--yes"}
var qLeaveChanges = &question{cmd: "host leave", flag: "--yes"}

func hostInviteCmd() *cobra.Command {
	var key string
	var ttl time.Duration
	var labels []string
	cmd := &cobra.Command{Use: "invite <name>", Example: "  " + brand.Name + " host invite my-vps", ValidArgsFunction: completeFixed(), Short: "Make a public invite for a machine to prepare itself", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		tags := map[string]string{}
		for _, kv := range labels {
			k, v, ok := strings.Cut(kv, "=")
			if !ok || strings.TrimSpace(k) == "" {
				return &client.UserError{Msg: "an invite label needs a key and value", Hint: "Use --label key=value.", Code: "bad_value", Kind: client.KindUsage}
			}
			tags[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
		inv, encoded, err := client.CreateHostInvite(cmd.Context(), client.InviteOptions{Name: args[0], Labels: tags, PublicKeyFile: key, TTL: ttl})
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "Invite for %s; expires %s. Copy it through a channel you trust.\n", inv.Name, inv.Expires.Format(time.RFC3339))
		_, err = fmt.Fprintln(cmd.OutOrStdout(), encoded)
		return err
	}}
	cmd.Flags().StringVar(&key, "key", "", "use this SSH public key file instead of making a host key")
	cmd.Flags().DurationVar(&ttl, "ttl", time.Hour, "how long the invite stays open")
	_ = cmd.RegisterFlagCompletionFunc("key", completeLocalFiles)
	addLabel(cmd, &labels, "labels to save when the reply is accepted, as key=value")
	return cmd
}
func readJoinCode(cmd *cobra.Command, p *prompter, invite bool) (string, error) {
	if !p.on {
		b, err := readBounded(cmd.InOrStdin(), code.MaxCode+1)
		if err != nil {
			return "", err
		}
		if len(b) > code.MaxCode {
			return "", client.JoinError(&code.Error{Code: "code_check", Message: "the code is longer than 4 KiB", Hint: "Copy only the code printed on the other machine."})
		}
		return string(b), nil
	}
	for {
		question := "Paste the reply code"
		if invite {
			question = "Paste the invite code"
		}
		text, err := p.askLine(question)
		if err != nil {
			return "", err
		}
		if !invite {
			return text, nil
		}
		if _, err = code.DecodeInvite(text, time.Now()); err == nil {
			return text, nil
		}
		var ce *code.Error
		if !errors.As(err, &ce) || ce.Code != "code_check" {
			return "", client.JoinError(err)
		}
		fmt.Fprintln(p.out, ce.Message+". Copy the code again.")
	}
}
func hostJoinCmd() *cobra.Command {
	var options localJoinOptionsV1
	var memory sizeValue
	var yes bool
	cmd := &cobra.Command{Use: "join", Example: "  " + brand.Name + " host join", Short: "Prepare this machine from a laptop invite on standard input", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		p := joinPrompts(cmd)
		raw, err := readJoinCode(cmd, p, true)
		if err != nil {
			return err
		}
		invite, err := code.DecodeInvite(raw, time.Now())
		if err != nil {
			return client.JoinError(err)
		}
		if !yes && !p.on {
			return p.needs(qJoinChanges, "a yes to the machine changes", "--yes")
		}
		options.MemoryBytes = memory.bytes
		if cmd.Flags().Changed("cpus") && options.CPUs <= 0 {
			return &client.UserError{Msg: "--cpus needs a positive count", Hint: "Use --cpus 2, or omit it to use the platform's default.", Code: "bad_value", Kind: client.KindUsage}
		}
		if len(options.Addresses) == 0 {
			options.Addresses, err = join.LocalAddresses()
			if err != nil {
				return client.JoinError(err)
			}
		}
		plan, err := newPlatformJoinPlanV1(cmd.Context(), invite, options)
		if err != nil {
			return client.JoinError(err)
		}
		fmt.Fprint(p.out, plan.PlanText())
		if !yes {
			ok, err := p.confirm(qJoinChanges, "Make these changes?", "a yes to the displayed machine changes")
			if err != nil {
				return err
			}
			if !ok {
				return p.cancel()
			}
		}
		if err = cmd.Context().Err(); err != nil {
			return err
		}
		if os.Geteuid() == 0 {
			return runLocalJoinV1(cmd, raw, options)
		}
		payload, err := encodeLocalJoinEnvelopeV1("join", raw, options)
		if err != nil {
			return err
		}
		return localJoinSudo(cmd, payload, []string{"host", "_join-apply"})
	}}
	cmd.Flags().StringVar(&options.Account, "account", brand.Name, "the new or existing non-root login account")
	cmd.Flags().StringSliceVar(&options.Addresses, "address", nil, "an address the laptop can reach (repeat for more than one)")
	cmd.Flags().Uint16Var(&options.Port, "port", 22, "the machine's outer SSH port")
	addCPUs(cmd, &options.CPUs, 0, "on a Mac, the CPUs for its Linux VM")
	addMemory(cmd, &memory, "on a Mac, the memory for its Linux VM, such as 8G")
	cmd.Flags().BoolVar(&options.AllowACSleepChange, "ac-awake", false, "on a Mac, explicitly allow the displayed AC sleep change")
	cmd.Flags().StringVar(&options.StartMode, "start-mode", "", "on a Mac, daemon or user-agent (default: daemon)")
	cmd.Flags().StringVar(&options.AgentUser, "agent-user", "", "on a Mac, the existing account for explicit user-agent mode")
	_ = cmd.RegisterFlagCompletionFunc("start-mode", completeFixed("daemon", "user-agent"))
	addYes(cmd, &yes, "make the displayed machine changes without asking")
	return cmd
}
func runLocalJoinV1(cmd *cobra.Command, raw string, options localJoinOptionsV1) error {
	invite, err := code.DecodeInvite(raw, time.Now())
	if err != nil {
		return client.JoinError(err)
	}
	reply, err := executePlatformJoinV1(cmd.Context(), invite, options, cmd.ErrOrStderr())
	if err != nil {
		return client.JoinError(err)
	}
	encoded, err := code.EncodeReply(reply)
	if err != nil {
		return client.JoinError(err)
	}
	for _, hop := range reply.Hops {
		for _, fingerprint := range hop.Fingerprints {
			fmt.Fprintln(cmd.ErrOrStderr(), fingerprint)
		}
	}
	for _, fingerprint := range reply.Fingerprints {
		fmt.Fprintln(cmd.ErrOrStderr(), fingerprint)
	}
	fmt.Fprintln(cmd.ErrOrStderr(), "Compare every host key on the laptop before accepting the reply.")
	_, err = fmt.Fprintln(cmd.OutOrStdout(), encoded)
	return err
}
func hostJoinApplyCmd() *cobra.Command {
	return &cobra.Command{Use: "_join-apply", Short: "Apply the original checked local join plan", Example: "  " + brand.Name + " host _join-apply", Hidden: true, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if os.Geteuid() != 0 {
			return &client.UserError{Msg: "the join executor needs root", Hint: "Run host join and approve its displayed changes.", Code: "join_root", Kind: client.KindRefused}
		}
		request, err := decodeLocalJoinEnvelopeV1(cmd.InOrStdin(), "join")
		if err != nil {
			return err
		}
		return runLocalJoinV1(cmd, request.Invite, request.Options)
	}}
}
func hostJoinProbeCmd() *cobra.Command {
	return &cobra.Command{Use: "_join-probe", Short: "Read sandbox prerequisites as the login account", Example: "  " + brand.Name + " host _join-probe", Hidden: true, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if os.Geteuid() == 0 {
			return &client.UserError{Msg: "the join probe must run as its non-root account", Hint: "Run host join to use its account-scoped probe.", Code: "join_probe", Kind: client.KindRefused}
		}
		probe, err := join.ProbeCurrentAccount(cmd.Context())
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(probe)
	}}
}
func hostLeaveCmd() *cobra.Command {
	var account string
	var yes bool
	cmd := &cobra.Command{Use: "leave", Example: "  " + brand.Name + " host leave", Short: "Undo this machine's original root-owned join ledger", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if !code.ValidAccount(account) {
			return &client.UserError{Msg: "the leave account is invalid", Hint: "Use --account with the account that host join prepared.", Code: "join_account", Kind: client.KindUsage}
		}
		p := joinPrompts(cmd)
		fmt.Fprintf(p.out, "Undo %s's join in reverse order. ALL processes of the original join-created account will stop, then its account and home will be removed. Changed files and uncertain ownership stop the undo.\n", account)
		if !yes {
			ok, err := p.confirm(qLeaveChanges, "Undo these machine changes?", "a yes to undo the recorded join")
			if err != nil {
				return err
			}
			if !ok {
				return p.cancel()
			}
		}
		if os.Geteuid() == 0 {
			return client.JoinError(leavePlatformJoinV1(cmd.Context(), account, p.out))
		}
		payload, err := encodeLocalJoinEnvelopeV1("leave", "", localJoinOptionsV1{Account: account})
		if err != nil {
			return err
		}
		return localJoinSudo(cmd, payload, []string{"host", "_join-leave"})
	}}
	cmd.Flags().StringVar(&account, "account", brand.Name, "the account that host join prepared")
	addYes(cmd, &yes, "undo the root-owned join ledger without asking")
	return cmd
}
func hostJoinLeaveCmd() *cobra.Command {
	return &cobra.Command{Use: "_join-leave", Short: "Undo the original local join ledger", Example: "  " + brand.Name + " host _join-leave", Hidden: true, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if os.Geteuid() != 0 {
			return &client.UserError{Msg: "the leave executor needs root", Hint: "Run host leave to use its original ledger.", Code: "join_root", Kind: client.KindRefused}
		}
		request, err := decodeLocalJoinEnvelopeV1(cmd.InOrStdin(), "leave")
		if err != nil {
			return err
		}
		if request.Invite != "" || !code.ValidAccount(request.Options.Account) || len(request.Options.Addresses) != 0 || request.Options.Port != 0 || request.Options.CPUs != 0 || request.Options.MemoryBytes != 0 || request.Options.AllowACSleepChange || request.Options.StartMode != "" || request.Options.AgentUser != "" {
			return localJoinPayloadErrorV1()
		}
		return client.JoinError(leavePlatformJoinV1(cmd.Context(), request.Options.Account, cmd.ErrOrStderr()))
	}}
}
func localJoinSudo(cmd *cobra.Command, in io.Reader, args []string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	sudoArgs := []string{"--", exe}
	if !inputOn(cmd) {
		sudoArgs = append([]string{"-n"}, sudoArgs...)
	}
	child := exec.CommandContext(cmd.Context(), "/usr/bin/sudo", append(sudoArgs, args...)...)
	child.Stdin = in
	child.Stdout = cmd.OutOrStdout()
	child.Stderr = cmd.ErrOrStderr()
	configureJoinSudo(child)
	if err = child.Run(); err != nil {
		if cmd.Context().Err() != nil {
			return errors.Join(context.Cause(cmd.Context()), err)
		}
		return &client.UserError{Msg: "the machine operation did not finish", Hint: "Keep the root-owned leave script; earlier displayed changes may have completed.", Code: "join_failed", Kind: client.KindFailed}
	}
	return nil
}
