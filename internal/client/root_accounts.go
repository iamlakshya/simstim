package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client/helpers"
	"github.com/iamlakshya/simstim/internal/supply"
)

// rootAccountPreparation is issued only by the original LOCALROOT factory's
// actual accepted connection and joined native public-artifact staging. It
// cannot be reconstructed from a RootScript, digest, release item or receipt.
type rootAccountPreparation struct {
	mu                       sync.Mutex
	conn                     *Conn
	request                  *GrantRequest
	source                   hostStateSourceWitness
	user, arch               string
	uid                      int
	nitroSlots               int
	item                     supply.Item
	data                     []byte
	ids                      []RootStepID
	receipt                  rootAccountStageReceipt
	staged, starting, closed bool
	installed                bool
	process                  *rootAccountProcess
}

func (*rootAccountPreparation) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "<root-account preparation>")
}

// PrepareAccountRootScript is the explicit opt-in consumer of the SAME root
// request. It stages only the client's verified public launcher. RootRun's
// existing independent script seal, consent and ledger remain mandatory.
func (c *Conn) PrepareAccountRootScript(ctx context.Context, ids []RootStepID, originalSeparate bool, slots ...int) (RootScript, error) {
	nitroSlots := 0
	if len(slots) == 1 {
		nitroSlots = slots[0]
	}
	if len(slots) > 1 || nitroSlots < 0 || nitroSlots > 1<<20 || slices.Contains(ids, RootNitroSlots) != (nitroSlots > 0) {
		return RootScript{}, rootStepError("the account root step has an invalid Nitro slot target")
	}
	if !originalSeparate {
		return RootScript{}, rootStepError("separate accounts need the original explicit preparation option")
	}
	if err := requireLocalOperation(ctx, grantLocalRoot, grantOpRootStep); err != nil {
		return RootScript{}, err
	}
	if c == nil || c.agentOnly || c.hostTransport == nil || c.hostTransport.kind != hostTransportSaved || !ValidHostID(c.HostID()) {
		return RootScript{}, rootStepError("separate accounts need the original accepted saved-host connection")
	}
	if err := bindRequestTransportSource(ctx, c); err != nil {
		return RootScript{}, err
	}
	r := grantRequestOf(ctx)
	operation, err := r.borrowNewSettings(ctx)
	if err != nil {
		return RootScript{}, err
	}
	metadata, err := newOrdinaryLocalGitMetadataSource(ctx, operation)
	if err != nil {
		return RootScript{}, err
	}
	check := metadata.CheckKeys(ctx, gitMetadataCheckKeys(localWindows()))
	if err := errors.Join(check, metadata.Close()); err != nil {
		return RootScript{}, err
	}
	probe, err := c.CapProbe(ctx, "")
	if err != nil {
		return RootScript{}, err
	}
	if probe.OS != "linux" || probe.UID <= 0 || !rootUserRE.MatchString(probe.User) || probe.User == "root" || probe.Arch != "amd64" && probe.Arch != "arm64" {
		return RootScript{}, rootStepError("separate accounts need a supported Linux architecture and non-root local account")
	}
	if err := bindRequestTransportSource(ctx, c); err != nil {
		return RootScript{}, err
	}
	// This rejects the development fallback and test override. The actual
	// embedded/fetched helper manifest is the signed client's trust source.
	if _, present := helpers.Manifest(); !present {
		return RootScript{}, rootStepError("this client has no actual release manifest for its root launcher")
	}
	manifest, err := loadManifest()
	if err != nil {
		return RootScript{}, err
	}
	item, ok := manifest.Find(supply.KindLaunch, "launch", probe.Arch)
	if !ok || !supply.IsHex64(item.SHA256) || item.Size <= 0 || item.Size > 256<<20 {
		return RootScript{}, rootStepError("this release has no bounded launcher for the host architecture")
	}
	data, present := helpers.File("launch-" + probe.Arch)
	if !present {
		return RootScript{}, rootStepError("the actual signed release bundle carries no launcher executable")
	}
	if err := supply.Check(item, "launch-"+probe.Arch, data); err != nil {
		return RootScript{}, rootStepError("the bundled root launcher differs from its original release item")
	}
	all := slices.Clone(ids)
	if slices.Contains(all, RootLauncher) || slices.Contains(all, RootSlots) {
		return RootScript{}, rootStepError("the optional account steps must be selected by their original preparation")
	}
	all = append(all, RootLauncher, RootSlots)
	p := &rootAccountPreparation{conn: c, request: r, user: probe.User, uid: probe.UID, arch: probe.Arch, item: item, data: slices.Clone(data), ids: all, nitroSlots: nitroSlots}
	r.mu.Lock()
	if err := r.lifetimeErrorLocked(); err != nil {
		r.mu.Unlock()
		return RootScript{}, err
	}
	if r.rootAccounts != nil {
		r.mu.Unlock()
		return RootScript{}, grantOperationError()
	}
	r.rootAccounts = p
	r.mu.Unlock()
	if err := p.runStage(ctx, false); err != nil {
		return RootScript{}, err
	}
	if err := p.check(ctx, c, probe); err != nil {
		return RootScript{}, err
	}
	return p.build()
}

func (p *rootAccountPreparation) check(ctx context.Context, c *Conn, probe api.CapProbe) error {
	if p == nil || c == nil || p.conn != c || p.request != grantRequestOf(ctx) || probe.User != p.user || probe.UID != p.uid || probe.Arch != p.arch || probe.OS != "linux" {
		return grantPurposeError()
	}
	if err := requireLocalOperation(ctx, grantLocalRoot, grantOpRootStep); err != nil {
		return err
	}
	if err := bindRequestTransportSource(ctx, c); err != nil {
		return err
	}
	p.mu.Lock()
	valid := p.staged && !p.closed && !p.starting && p.process == nil
	p.mu.Unlock()
	if !valid {
		return grantOperationError()
	}
	return p.runStage(ctx, true)
}

func (p *rootAccountPreparation) close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	if p.starting || p.process != nil {
		return agentModeResourceError()
	}
	p.data = nil
	return nil
}
