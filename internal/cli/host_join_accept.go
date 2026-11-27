package cli

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/join/code"
)

// acceptJoinedHost reads the reply only from standard input. Every check and
// explicit answer precedes generated config, pin, connection or host writes.
func acceptJoinedHost(cmd *cobra.Command, name, accept, address string, hostKeys []string, tier client.Tier, yes bool, supplied ...joinedAcceptOptionsV1) (returned error) {
	if accept != "-" {
		return &client.UserError{Msg: "--accept reads the reply from standard input", Hint: "Use --accept -, then paste or pipe the reply code.", Code: "bad_value", Kind: client.KindUsage}
	}
	p := joinPrompts(cmd)
	raw, err := readJoinCode(cmd, p, false)
	if err != nil {
		return err
	}
	reply, err := code.DecodeReply(raw)
	if err != nil {
		return client.JoinError(err)
	}
	lease, err := client.BeginInviteAcceptance(cmd.Context(), name, reply, time.Now())
	if err != nil {
		return err
	}
	defer lease.Close()
	selected, err := client.InviteAddress(reply, address)
	if err != nil {
		return err
	}
	all := slices.Clone(reply.Fingerprints)
	for _, hop := range reply.Hops {
		all = append(all, hop.Fingerprints...)
	}
	for _, fp := range all {
		fmt.Fprintln(p.out, fp)
	}
	if len(hostKeys) > 0 {
		given := slices.Clone(hostKeys)
		expected := slices.Clone(all)
		slices.Sort(given)
		slices.Sort(expected)
		if !slices.Equal(given, expected) {
			return &client.UserError{Msg: "--host-key must name every reply key exactly once", Hint: "Compare both screens, then pass --host-key once for each SHA256 fingerprint in the reply.", Code: "host_key_changed", Kind: client.KindRefused}
		}
	} else {
		ok, err := p.confirm(qHostKey, "Do these host keys match the machine's screen?", "every host-key fingerprint from the machine's screen")
		if err != nil {
			return err
		}
		if !ok {
			return p.cancel()
		}
	}
	if !tier.Valid() {
		if yes {
			return missingTierWithYes(p, qHostTier, name)
		}
		if !p.on {
			return p.needs(qHostTier, "the joined host's tier (own, throwaway or shared)", "--tier", "<tier>")
		}
		if reply.SuggestedTier != "" {
			fmt.Fprintf(p.out, "The machine suggests %s. Choose its tier yourself.\n", reply.SuggestedTier)
		}
		tier, err = p.askTier(name)
		if err != nil {
			return err
		}
	}
	if err = cmd.Context().Err(); err != nil {
		return err
	}
	options := joinedAcceptOptionsV1{prepare: hostPrepareFlowOpts{yes: yes}}
	if len(supplied) > 1 {
		return &client.UserError{Msg: "the original acceptance has more than one option set", Hint: "Run host add --accept once.", Code: "bad_value", Kind: client.KindUsage}
	}
	if len(supplied) == 1 {
		options = supplied[0]
	}
	var h client.Host
	var stage *client.InviteSSHStage
	if options.sshConfig != "" {
		h, stage, err = lease.StageSSHWithExplicitConfigV1(lease.Context(), reply, selected, options.sshConfig)
	} else {
		h, stage, err = lease.StageSSHV1(lease.Context(), reply, selected)
	}
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, stage.Close()) }()
	if err = lease.BindSSH(h, stage); err != nil {
		return err
	}
	if err = registerAndPrepareJoinedHostV1(cmd, p, lease, h, reply.Fingerprints, tier, yes, options); err != nil {
		return err
	}
	if !lease.Registered() {
		return &client.UserError{Msg: "the accepted reply has no original registration result", Hint: "Keep the reply and retry with a matching current client.", Code: "invite_registration", Kind: client.KindFailed}
	}
	return nil
}

// readAcceptedReply is useful to callers with a bounded input stream.
func readAcceptedReply(in io.Reader) (code.Reply, error) {
	b, err := io.ReadAll(io.LimitReader(in, code.MaxCode+1))
	if err != nil {
		return code.Reply{}, err
	}
	return code.DecodeReply(string(b))
}
