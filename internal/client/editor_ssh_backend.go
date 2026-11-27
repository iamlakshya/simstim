package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client/sshserve"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

// editorSSHInvocationV1 can be issued only from the actual accepted parent.
// Its original stamp, Source and local creation are privately retained; no
// public session row or serialised expected generation can construct it.
type editorSSHInvocationV1 struct {
	parent, raw  *Conn
	stamp        *hostTransportStamp
	source       hostStateSourceWitness
	creation     *SandboxPolicy
	ctx          context.Context
	cancel       context.CancelFunc
	carrier      *link.Session
	closeCarrier func()
	watchDone    chan struct{}
	workers      sync.WaitGroup
	once         sync.Once
	closeErr     error
	leaseHeld    bool
}

// ServeEditorSSH runs the local stdio server after the normal same-connection
// wake workflow has finished. It selects no secrets or agent keys itself.
func (c *Conn) ServeEditorSSH(ctx context.Context, session string, input, output *os.File, key *SSHHostKey) (returned error) {
	if err := c.CheckSavedSource(ctx); err != nil {
		return err
	}
	if err := checkSessionID(c.Host.Name, session); err != nil {
		return err
	}
	if !c.HasFeature(proto.FeatureEditorSSHV1) {
		return editorSSHUpgradeV1()
	}
	// This is a new value-free lifetime after any closed resume request. It
	// retains the SAME actual accepted Conn, never an Arm/new/attach DTO.
	request := NewGrantRequest(false, false)
	defer func() { returned = errors.Join(returned, request.Close()) }()
	owned, cancel := context.WithCancel(WithGrantRequest(ctx, request))
	invocation := &editorSSHInvocationV1{parent: c, stamp: c.hostTransport, ctx: owned, cancel: cancel, watchDone: make(chan struct{})}
	defer func() {
		cleanup, end := context.WithTimeout(context.Background(), 30*time.Second)
		defer end()
		returned = errors.Join(returned, invocation.Close(cleanup))
	}()
	creation, err := getSandboxReceiptPolicyContext(owned, c, session)
	if err != nil {
		return err
	}
	if creation == nil || creation.CreationWall == "" || creation.CreationWall == "none" || creation.FutureLineage || creation.FutureDelivery {
		return sandboxReceiptError()
	}
	invocation.creation = creation
	err = withSandboxReceiptObservation(owned, c, func(observed context.Context) error {
		var err error
		invocation.source, err = captureHostStateSourceWitness(observed, c)
		return err
	})
	if err != nil {
		return err
	}
	// SSH input belongs to this server. All host transport startup is direct
	// and non-interactive; its stdout remains the one dedicated raw reader.
	start, end := context.WithTimeout(context.WithValue(owned, execDialKey{}, true), 60*time.Second)
	raw, err := dialHostContinuation(WithoutWake(start), c)
	end()
	if err != nil {
		return err
	}
	invocation.raw = raw
	defer raw.execReadFinished()
	if !raw.HasFeature(proto.FeatureEditorSSHV1) {
		return editorSSHUpgradeV1()
	}
	if err = checkInputContinuationPair(c, raw); err != nil {
		return err
	}
	guarded, release, err := invocation.sourceTurn(owned)
	if err != nil {
		return err
	}
	var result proto.EditorSSHOpenResultV1
	args := proto.EditorSSHOpenArgsV1{Version: 1, Session: session}
	id, _, err := raw.startCall(guarded, proto.EditorSSHOpenV1, args)
	release()
	if err != nil {
		return err
	}
	defer raw.drop(id)
	stop := context.AfterFunc(owned, raw.kill)
	frame, err := proto.Read(raw.execReader)
	stop()
	if err != nil {
		return err
	}
	var response proto.Response
	if frame.Type != proto.TResponse || !api.ExactJSONFields(frame.Payload, response) || json.Unmarshal(frame.Payload, &response) != nil || response.ID != id {
		return sandboxReceiptError()
	}
	if !response.OK {
		if response.Error != nil {
			return hostErrorOn(c.Host.Name, response.Error)
		}
		return sandboxReceiptError()
	}
	if !api.ExactJSONFields(response.Result, result) || json.Unmarshal(response.Result, &result) != nil || result.Version != 1 || result.Session != session || result.Root != localSandboxRoot(creation) {
		return sandboxReceiptError()
	}
	connection, reader, closeCarrier := raw.execCarrier()
	invocation.closeCarrier = closeCarrier
	invocation.carrier = link.ContinueEditorSSH(connection, reader, link.Guest)
	c.execBusy.Add(1)
	invocation.leaseHeld = true
	c.renewExecLease(owned)
	go func() {
		defer close(invocation.watchDone)
		select {
		case <-c.Done():
		case <-invocation.carrier.Done():
		case <-owned.Done():
		}
		cancel()
		closeCarrier()
	}()
	if err := invocation.startHeartbeat(); err != nil {
		return err
	}
	return sshserve.Serve(owned, input, output, key, invocation)
}

func (i *editorSSHInvocationV1) sourceTurn(ctx context.Context) (context.Context, func(), error) {
	if ctx == nil || i == nil || i.parent == nil || i.stamp == nil || i.parent.hostTransport != i.stamp || i.creation == nil || i.ctx == nil || i.ctx.Err() != nil {
		return ctx, nil, sandboxReceiptError()
	}
	owned, release, err := i.parent.beginGrant(ctx, "editor SSH channel", true)
	if err != nil {
		return ctx, nil, err
	}
	check := func() error {
		if err := i.stamp.checkConn(i.parent); err != nil {
			return err
		}
		if err := i.source.check(owned, i.parent); err != nil {
			return err
		}
		current, err := getSandboxReceiptPolicyContext(owned, i.parent, i.creation.SessionID)
		if err != nil {
			return err
		}
		if current == nil || !sameSandboxCreation(current, i.creation) || current.Ceiling != i.creation.Ceiling {
			return sandboxReceiptError()
		}
		if i.raw != nil {
			return checkInputContinuationPair(i.parent, i.raw)
		}
		return nil
	}
	if err = check(); err != nil {
		release()
		return ctx, nil, err
	}
	return owned, release, nil
}

func (i *editorSSHInvocationV1) open(ctx context.Context, kind link.Kind, header proto.EditorChannelV1) (*link.Stream, error) {
	if i == nil || i.carrier == nil {
		return nil, sandboxReceiptError()
	}
	return i.carrier.OpenGuarded(ctx, kind, header, func(turn context.Context) (func(), error) { _, release, err := i.sourceTurn(turn); return release, err })
}

type editorExecInputV1 struct{ records *link.ExecRecords }

func (w editorExecInputV1) Write(data []byte) (int, error) { return w.records.Input(data) }

func (i *editorSSHInvocationV1) Exec(ctx context.Context, command string, input io.Reader, stdout, stderr io.Writer) (int, error) {
	stream, err := i.open(ctx, link.KindExec, proto.EditorChannelV1{Version: 1, Command: command})
	if err != nil {
		return 125, err
	}
	defer stream.Close()
	stop := context.AfterFunc(ctx, func() { _ = stream.SetDeadline(time.Now()); _ = stream.Close() })
	defer stop()
	records := link.NewExecRecords(stream)
	// The SSH session owns its input descriptor. The server closes it after
	// this output result; Close joins every such actual input pump afterwards.
	i.workers.Add(1)
	go func() {
		defer i.workers.Done()
		if input != nil {
			_, _ = io.Copy(editorExecInputV1{records}, input)
		}
		_ = records.CloseInput()
	}()
	return link.ReadExecOutput(stream, stdout, stderr)
}

func (i *editorSSHInvocationV1) SFTP(ctx context.Context) (io.ReadWriteCloser, error) {
	stream, err := i.open(ctx, link.KindSFTP, proto.EditorChannelV1{Version: 1})
	if err != nil {
		return nil, err
	}
	if err = readEditorReadyContextV1(ctx, stream); err != nil {
		_ = stream.Close()
		return nil, err
	}
	return stream, nil
}

func (i *editorSSHInvocationV1) Port(ctx context.Context, port uint16) (io.ReadWriteCloser, error) {
	stream, err := i.open(ctx, link.KindPort, proto.EditorChannelV1{Version: 1, Port: uint32(port)})
	if err != nil {
		return nil, err
	}
	if err = readEditorReadyContextV1(ctx, stream); err != nil {
		_ = stream.Close()
		var refused *proto.Error
		if errors.As(err, &refused) && refused.Code == "port_refused" {
			return nil, fmt.Errorf("%s. Try: %s policy allow port %d --in %s", CleanLine(refused.Message), brand.Name, port, ShellWord(i.parent.Host.Name+"/"+i.creation.SessionID))
		}
		return nil, err
	}
	return stream, nil
}

func (i *editorSSHInvocationV1) Close(context.Context) error {
	if i == nil {
		return nil
	}
	i.once.Do(func() {
		if i.cancel != nil {
			i.cancel()
		}
		if i.carrier != nil {
			i.closeErr = errors.Join(i.closeErr, i.carrier.Close())
		}
		if i.closeCarrier != nil {
			i.closeCarrier()
		}
		if i.carrier != nil {
			<-i.watchDone
		}
		i.workers.Wait()
		if i.leaseHeld {
			i.parent.execBusy.Add(-1)
			i.leaseHeld = false
		}
		if i.raw != nil {
			i.raw.Close()
			// Dial/watchProcess owns the one actual process Wait, including
			// private diagnostic cleanup. IO completion alone is not that
			// native child's completion, so retain the original source here.
			if i.raw.procDone == nil {
				i.closeErr = errors.Join(i.closeErr, sandboxReceiptError())
			} else {
				<-i.raw.procDone
			}
		}
	})
	return i.closeErr
}

func (i *editorSSHInvocationV1) startHeartbeat() error {
	if i.raw.execAlive <= 0 {
		return nil
	}
	stream, err := i.open(i.ctx, link.KindCtl, proto.EditorChannelV1{Version: 1})
	if err != nil {
		return err
	}
	i.workers.Add(1)
	go func() {
		defer i.workers.Done()
		defer stream.Close()
		ticker := time.NewTicker(i.raw.execAlive)
		defer ticker.Stop()
		writer := proto.NewWriter(stream)
		for {
			select {
			case <-i.ctx.Done():
				return
			case <-ticker.C:
			}
			_ = stream.SetDeadline(time.Now().Add(3 * i.raw.execAlive))
			if writer.Write(proto.TPing, nil) != nil {
				i.cancel()
				return
			}
			frame, err := link.ReadEditorFrameV1(stream, 16)
			if err != nil || frame.Type != proto.TPong || len(frame.Payload) != 0 {
				i.cancel()
				return
			}
			_ = stream.SetDeadline(time.Time{})
		}
	}()
	return nil
}

func readEditorReadyV1(stream *link.Stream) error {
	frame, err := link.ReadEditorFrameV1(stream, 1024)
	if err != nil {
		return err
	}
	var ready proto.EditorReadyV1
	if frame.Type != proto.TResponse || !api.ExactJSONFields(frame.Payload, ready) || json.Unmarshal(frame.Payload, &ready) != nil || ready.Version != 1 {
		return sandboxReceiptError()
	}
	if !ready.Ready {
		if ready.Error != nil {
			return ready.Error
		}
		return sandboxReceiptError()
	}
	if ready.Error != nil {
		return sandboxReceiptError()
	}
	return nil
}

func readEditorReadyContextV1(ctx context.Context, stream *link.Stream) error {
	ready, end := context.WithTimeout(ctx, 10*time.Second)
	defer end()
	stop := context.AfterFunc(ready, func() { _ = stream.SetReadDeadline(time.Now()) })
	err := readEditorReadyV1(stream)
	if !stop() {
		if ready.Err() != nil {
			return ready.Err()
		}
	}
	_ = stream.SetReadDeadline(time.Time{})
	return err
}

func editorSSHUpgradeV1() error {
	return &UserError{Msg: "the helper does not offer editor SSH", Hint: "Upgrade the host helper, then stop and resume the sandbox.", Code: "upgrade_required", Kind: KindRefused}
}

var _ sshserve.Backend = (*editorSSHInvocationV1)(nil)
