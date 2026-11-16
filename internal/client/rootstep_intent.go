package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
)

type rootStepIntent struct {
	conn   *Conn
	source hostStateSourceWitness
	script RootScript
	before api.CapProbe
	mode   RootStepMode
	yes    bool
}

func (*rootStepIntent) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "<root-step intent>")
}

func cloneRootScript(script RootScript) RootScript {
	script.Steps = slices.Clone(script.Steps)
	return script
}

func canonicalRootScript(probe api.CapProbe, script RootScript) (RootScript, error) {
	if probe.OS != "linux" || probe.User != script.User {
		return RootScript{}, rootStepError("the root plan does not match the actual host account")
	}
	ids := make([]RootStepID, 0, len(script.Steps))
	for _, step := range script.Steps {
		ids = append(ids, step.ID)
	}
	canonical, err := BuildRootScript(probe.User, probe.UID, ids, 0)
	if err != nil {
		return RootScript{}, err
	}
	if !reflect.DeepEqual(canonical, script) {
		return RootScript{}, rootStepError("the root script differs from the fixed plan shown for this host")
	}
	return canonical, nil
}

// CheckRootStepGrant seals the exact canonical plan and original local mode
// before a terminal question. Host data, a hash or --yes alone grants nothing.
func (c *Conn) CheckRootStepGrant(ctx context.Context, script RootScript, originalMode RootStepMode, originalYes bool) error {
	if err := requireLocalOperation(ctx, grantLocalRoot, grantOpRootStep); err != nil {
		return err
	}
	if _, err := ParseRootStepMode(string(originalMode)); err != nil {
		return err
	}
	if c == nil || c.agentOnly || c.hostTransport == nil || c.hostTransport.kind != hostTransportSaved || !ValidHostID(c.HostID()) {
		return rootStepError("the root step needs its original accepted saved-host connection")
	}
	if err := bindRequestTransportSource(ctx, c); err != nil {
		return err
	}
	r := grantRequestOf(ctx)
	operation, err := r.borrowNewSettings(ctx)
	if err != nil {
		return err
	}
	metadata, err := newOrdinaryLocalGitMetadataSource(ctx, operation)
	if err != nil {
		return err
	}
	check := metadata.CheckKeys(ctx, gitMetadataCheckKeys(localWindows()))
	if err := errors.Join(check, metadata.Close()); err != nil {
		return err
	}
	probe, err := c.CapProbe(ctx, "")
	if err != nil {
		return err
	}
	if err := bindRequestTransportSource(ctx, c); err != nil {
		return err
	}
	canonical, err := canonicalRootScript(probe, script)
	if err != nil {
		return err
	}
	owned, release, err := c.beginLocalGrant(ctx, grantLocalRoot, grantOpRootStep, "seal original root-step plan")
	if err != nil {
		return err
	}
	source, err := captureHostStateSourceWitness(owned, c)
	release()
	if err != nil {
		return err
	}
	r.mu.Lock()
	if before := r.root; before != nil {
		if before.conn != c || before.mode != originalMode || before.yes != originalYes || before.before.User != probe.User || before.before.UID != probe.UID ||
			!reflect.DeepEqual(before.script, canonical) || !sameReceiptSource(before.source, source) {
			r.mu.Unlock()
			return grantOperationError()
		}
	} else {
		r.root = &rootStepIntent{conn: c, source: source, script: cloneRootScript(canonical), before: probe, mode: originalMode, yes: originalYes}
	}
	r.mu.Unlock()
	if originalMode != RootRun {
		return operation.Revalidate(ctx)
	}
	settings, err := operation.Settings(ctx)
	if err != nil {
		return err
	}
	tier, hostID, err := grantHostTier(ctx, c.Host)
	if err != nil {
		return err
	}
	if hostID != c.HostID() {
		return grantIdentityError(c.Host)
	}
	tier = min(tier, requestCeiling(ctx, tier))
	return allowRequestGrant(ctx, c.Host, hostID, tier, Outbound{Kind: OutboundRootRun, Name: canonical.SHA256}, settings)
}

func requireRootOutbound(ctx context.Context, r *GrantRequest, outbound Outbound) error {
	if err := requireLocalOperation(ctx, grantLocalRoot, grantOpRootStep); err != nil {
		return err
	}
	r.mu.Lock()
	valid := r.root != nil && r.root.mode == RootRun && outbound.Kind == OutboundRootRun && outbound.Name == r.root.script.SHA256
	r.mu.Unlock()
	if !valid {
		return grantPurposeError()
	}
	return nil
}

func requireRootIntent(ctx context.Context, c *Conn, script RootScript, mode RootStepMode) (*rootStepIntent, error) {
	if err := requireLocalOperation(ctx, grantLocalRoot, grantOpRootStep); err != nil {
		return nil, err
	}
	r := grantRequestOf(ctx)
	r.mu.Lock()
	intent := r.root
	valid := intent != nil && intent.conn == c && intent.mode == mode && reflect.DeepEqual(intent.script, script)
	r.mu.Unlock()
	if !valid {
		return nil, grantPurposeError()
	}
	if err := bindRequestTransportSource(ctx, c); err != nil {
		return nil, err
	}
	return intent, nil
}
