package client

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/iamlakshya/simstim/internal/join/code"
)

type joinedRouteInvocationV1 struct {
	projection *explicitRouteProjectionV1
	config     string
	last       string
	record     joinedRouteRecordV1
}

func (p *explicitRouteProjectionV1) publishIntoStageV1(ctx context.Context, stage *InviteSSHStage) (*joinedRouteInvocationV1, error) {
	if err := p.check(ctx); err != nil {
		return nil, err
	}
	if len(p.bastions) == 0 {
		return nil, routeSourceErrorV1("the original route has no verified bastion")
	}
	published := append([]explicitBastionV1(nil), p.bastions...)
	for index, bastion := range published {
		var data strings.Builder
		for _, key := range bastion.keys {
			data.WriteString(key.line(bastion.alias))
			data.WriteByte('\n')
		}
		path := filepath.Join(stage.directory, fmt.Sprintf("route-bastion-%d.keys", index+1))
		child, err := stageInviteFile(path, []byte(data.String()))
		if err != nil {
			return nil, err
		}
		stage.children = append(stage.children, child)
		published[index].pinPath = path
	}
	var text strings.Builder
	for _, option := range noForwarding {
		fmt.Fprintf(&text, "%s %s\n", option[0], option[1])
	}
	text.WriteString("ClearAllForwardings yes\nPermitLocalCommand no\nCanonicalizeHostname no\nBatchMode yes\n")
	for index, bastion := range published {
		if err := renderBastionHostV1(&text, bastion.alias, bastion.resolved, bastion.keys, bastion.pinPath, index, published); err != nil {
			return nil, err
		}
	}
	config := filepath.Join(stage.directory, "route.conf")
	child, err := stageInviteFile(config, []byte(text.String()))
	if err != nil {
		return nil, err
	}
	stage.children = append(stage.children, child)
	record, err := p.record()
	if err != nil {
		return nil, err
	}
	if err = p.check(ctx); err != nil {
		return nil, err
	}
	return &joinedRouteInvocationV1{projection: p, config: config, last: published[len(published)-1].alias, record: record}, nil
}
func (i *joinedRouteInvocationV1) proxyCommandV1(address string, port uint16) (string, error) {
	if i == nil || i.projection == nil || i.config == "" || i.last == "" {
		return "", routeSourceErrorV1("the actual explicit route invocation is missing")
	}
	if err := i.projection.check(context.Background()); err != nil {
		return "", err
	}
	binary, err := exec.LookPath("ssh")
	if err != nil {
		return "", err
	}
	binary, err = filepath.Abs(binary)
	if err != nil {
		return "", err
	}
	args := []string{binary, "-F", i.config, "-o", "BatchMode=yes", "-o", "ForwardAgent=no", "-o", "ForwardX11=no", "-o", "PermitLocalCommand=no", "-o", "GSSAPIDelegateCredentials=no", "-o", "ControlMaster=no", "-o", "ControlPath=none", "-o", "ClearAllForwardings=yes", "-T", "-W", net.JoinHostPort(address, strconv.Itoa(int(port))), "--", i.last}
	quoted := make([]string, len(args))
	for index, arg := range args {
		quoted[index] = ShellQuote(strings.ReplaceAll(arg, "%", "%%"))
	}
	return "exec " + strings.Join(quoted, " "), nil
}

// StageSSHWithExplicitConfigV1 consumes an actual original invitation and local
// explicit source. No reply/provider/facts path can request this constructor.
func (a *InviteAcceptance) StageSSHWithExplicitConfigV1(ctx context.Context, reply code.Reply, address, path string) (host Host, stage *InviteSSHStage, failure error) {
	defer func() {
		if failure == nil || errors.Is(failure, context.Canceled) || errors.Is(failure, context.DeadlineExceeded) {
			return
		}
		var existing *UserError
		if !errors.As(failure, &existing) {
			failure = &UserError{Msg: "the explicit joined route failed: " + CleanLine(failure.Error()), Hint: "Keep the original SSH source unchanged, check its existing bastion trust, then retry the same invite.", Code: "joined_route_source", Kind: KindRefused}
		}
	}()
	if err := a.checkOriginalReplyV1(ctx, reply); err != nil {
		return Host{}, nil, err
	}
	source, err := resolveExplicitJoinedRouteV1(ctx, a, path)
	if err != nil {
		return Host{}, nil, err
	}
	defer func() {
		failure = joinRouteErrorsV1(failure, source.close())
		if failure != nil && stage != nil {
			failure = joinRouteErrorsV1(failure, stage.Close())
		}
	}()
	projection, err := captureExplicitBastionsV1(ctx, source)
	if err != nil {
		return Host{}, nil, err
	}
	defer func() { failure = joinRouteErrorsV1(failure, projection.close()) }()
	host, stage, failure = stageInviteSSHWithRouteV1(ctx, a.original, a.reply, address, projection)
	if stage != nil {
		stage.acceptance = a
		stage.originalHost = cloneObservationHost(host)
		a.projection = stage
	}
	return host, stage, failure
}

func (a *InviteAcceptance) checkOriginalReplyV1(ctx context.Context, reply code.Reply) error {
	if err := a.checkOriginalInviteV1(ctx); err != nil {
		return err
	}
	if a.projection != nil || !sameJoinedReplyV1(reply, a.reply) {
		return hostStateRemovalError("the joined reply or its original projection changed")
	}
	return nil
}

// StageSSHV1 stages only the reply checked by the actual original Begin call.
// It grants no host save or arbitrary transport, and the actual writer must
// still pin every endpoint key and bind its returned target transactionally.
func (a *InviteAcceptance) StageSSHV1(ctx context.Context, reply code.Reply, address string) (Host, *InviteSSHStage, error) {
	if err := a.checkOriginalReplyV1(ctx, reply); err != nil {
		return Host{}, nil, err
	}
	host, stage, err := stageInviteSSHWithRouteV1(ctx, a.original, a.reply, address, nil)
	if stage != nil {
		stage.acceptance = a
		stage.originalHost = cloneObservationHost(host)
		a.projection = stage
	}
	return host, stage, err
}
