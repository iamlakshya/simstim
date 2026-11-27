package client

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/grant"
)

// This private immutable carrier is constructed from the LOCAL command flag.
// Mode in a host's manifest, a returned RuleID or metadata is never an input
// which can assert public acknowledgement. H5 binds this exact intent to R.
type pushGrantIntent struct {
	owner   *Conn
	host    string
	hostID  string
	session string
	repo    string
	mode    string
	once    bool
	until   time.Time
}

func (intent *pushGrantIntent) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "<push grant intent>")
}

func newPushGrantIntent(c *Conn, args api.PolicyAddArgs, publicFlag bool) (*pushGrantIntent, error) {
	if !args.Allow {
		return nil, &UserError{Msg: "a push allowance needs its original positive local request", Hint: "Use policy allow push for this request.", Code: "bad_push", Kind: KindUsage}
	}
	if err := c.needPolicy(args.ID); err != nil {
		return nil, err
	}
	if !c.HasFeature(api.FeaturePolicyPush) {
		return nil, c.grantHelperOld("preserve exact repository push grants")
	}
	if args.Door != "" && args.Door != api.DoorPush || args.Methods != nil || len(args.Tools) != 0 {
		return nil, &UserError{Msg: "push grants cannot carry network methods or model tools", Hint: "Use policy allow push <repository> --in <host>/<session>.", Code: "bad_push", Kind: KindUsage}
	}
	repo, err := NormalizeRepo(args.Target) // SAME existing H5 repository grammar.
	if err != nil {
		return nil, err
	}
	repo, err = grant.CheckPushTarget(repo)
	if err != nil {
		return nil, PolicyError(err)
	}
	mode := ""
	if publicFlag {
		mode = api.PushModePublic
	}
	if args.Mode != "" && args.Mode != mode {
		return nil, &UserError{Msg: "the push mode does not match the original public flag", Hint: "Pass --public explicitly to acknowledge publishing.", Code: "grant_public", Kind: KindRefused}
	}
	if !ValidHostID(c.HostID()) {
		return nil, grantIdentityError(c.Host)
	}
	return &pushGrantIntent{owner: c, host: c.Host.Name, hostID: c.HostID(), session: args.ID, repo: repo, mode: mode, once: args.Once, until: args.Until.Round(0).UTC()}, nil
}

func (intent *pushGrantIntent) args() api.PolicyAddArgs {
	return api.PolicyAddArgs{ID: intent.session, Allow: true, Door: api.DoorPush, Target: intent.repo, Mode: intent.mode, Once: intent.once, Until: intent.until}
}

func (intent *pushGrantIntent) matches(c *Conn, args api.PolicyAddArgs) bool {
	return intent != nil && intent.owner == c && intent.host == c.Host.Name && intent.hostID == c.HostID() && args.ID == intent.session && args.Allow && args.Door == api.DoorPush && args.Target == intent.repo && args.Mode == intent.mode && args.Methods == nil && len(args.Tools) == 0 && args.Once == intent.once && args.Until.Round(0).UTC().Equal(intent.until)
}

// The closed forge capability obtains the tiny actual ACK facts and applies
// the push gates before any application write. No full sandbox/history read
// precedes that same-source producer or supplies a session ceiling.
func (c *Conn) checkPushGrantIntent(ctx context.Context, intent *pushGrantIntent) error {
	if err := bindRequestPushIntent(ctx, c, intent); err != nil {
		return err
	}
	request, err := pushForgeRequestFor(intent.repo)
	if err != nil {
		return err
	}
	visibility, err := withLocalForgePushVisibility(ctx, c, intent, request)
	if err != nil {
		return err
	}
	if visibility.repo != intent.repo {
		return &UserError{Msg: "the forge's repository identity changed", Hint: "Check the remote, then grant the intended repository again.", Code: "grant_public", Kind: KindRefused}
	}
	if visibility.public && intent.mode != api.PushModePublic {
		return &UserError{Msg: "this repository is public, so a push publishes", Hint: "Repeat the push grant with --public if you intend to publish.", Code: "grant_public", Kind: KindRefused}
	}
	return nil
}
