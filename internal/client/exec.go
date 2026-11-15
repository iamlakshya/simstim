package client

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

type execDialKey struct{}

func execDial(ctx context.Context) bool { v, _ := ctx.Value(execDialKey{}).(bool); return v }

// RunExec runs a value-free command inside the named wall. c remains the
// ordinary policy connection; a dedicated, independently verified bridge owns
// the streaming reader. It never converts a wall refusal into a host command.
func (c *Conn) RunExec(ctx context.Context, a api.ExecArgs, in *os.File, stdout, stderr io.Writer) (int, error) {
	if err := a.Normalise(); err != nil {
		return 125, err
	}
	if !c.HasFeature(api.FeatureExecStream) {
		return 125, execUpgrade()
	}
	ctx, _ = ensureGrantRequest(ctx)
	// The continuation helper checks this accepted original Source before any
	// network start. No host publication OP crosses the network handshake.
	start, cancel := context.WithTimeout(context.WithValue(ctx, execDialKey{}, true), 60*time.Second)
	raw, err := dialHostContinuation(start, c)
	cancel()
	if err != nil {
		return 125, err
	}
	defer raw.Close()
	if !ValidHostID(c.HostID()) || raw.HostID() != c.HostID() || !sameExecTransportSource(c, raw) {
		return 125, &UserError{Msg: "the host identity changed before exec", Hint: "Check the saved host identity and reconnect.", Code: "host_id_changed", Kind: KindRefused}
	}
	if !raw.HasFeature(api.FeatureExecStream) {
		return 125, execUpgrade()
	}
	locked, release, err := c.beginGrant(ctx, "exec", false)
	if err != nil {
		return 125, err
	}
	defer release()
	if err := checkTransportBeforeTierMigration(locked, c); err != nil {
		return 125, err
	}
	if err := checkTransportBeforeTierMigration(locked, raw); err != nil {
		return 125, err
	}
	if err := c.checkGrantHost(locked, false); err != nil {
		return 125, err
	}
	c.execBusy.Add(1)
	defer c.execBusy.Add(-1)
	// Cancel owns this dedicated transport. It cannot cancel another caller's
	// stdin or alter flags on a borrowed terminal descriptor.
	stop := context.AfterFunc(ctx, raw.kill)
	defer stop()
	args, err := json.Marshal(a)
	if err != nil {
		return 125, err
	}
	if err = raw.w.WriteJSON(proto.TRequest, proto.Request{ID: 1, Op: "exec", Args: args}); err != nil {
		return 125, err
	}
	// No response, terminal wait or value fetch holds the publication OP.
	release()
	c.renewExecLease(ctx)
	frame, err := proto.Read(raw.execReader)
	if err != nil {
		return 125, err
	}
	var resp proto.Response
	if frame.Type != proto.TResponse || json.Unmarshal(frame.Payload, &resp) != nil || resp.ID != 1 {
		return 125, fmt.Errorf("invalid exec response. Try: update the client and helper")
	}
	if !resp.OK {
		if resp.Error != nil {
			return 125, resp.Error
		}
		return 125, fmt.Errorf("exec was refused. Try: check the sandbox and run it again")
	}
	stop()
	conn, reader, closeCarrier := raw.execCarrier()
	defer closeCarrier()
	carrier := link.ContinueExec(conn, reader, link.Guest)
	defer carrier.Close()
	stream, err := carrier.Accept(ctx)
	if err != nil {
		return 125, err
	}
	if stream.Kind() != link.KindExec {
		return 125, fmt.Errorf("unexpected exec stream. Try: update the client and helper")
	}
	defer stream.Close()
	return pumpExec(ctx, stream, in, stdout, stderr, raw.execAlive)
}

func sameExecTransportSource(c, raw *Conn) bool {
	return checkAcceptedHostTransport(c) == nil && checkAcceptedHostTransport(raw) == nil &&
		c.hostTransport.kind == hostTransportSaved && raw.hostTransport.kind == hostTransportSaved &&
		sameHostTransportSource(c.hostTransport.source, raw.hostTransport.source)
}

func execUpgrade() error {
	return &UserError{Msg: "the helper does not offer sandbox exec", Hint: "Upgrade the host helper, then stop and resume the sandbox.", Code: "upgrade_required"}
}

// execCarrier adapts only owned transport streams. Pumps retain buffered bytes
// from the response and net.Pipe supplies real cancellation/deadline semantics.
func (c *Conn) execCarrier() (net.Conn, *bufio.Reader, func()) {
	local, peer := net.Pipe()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); defer c.execReadFinished(); _, _ = io.Copy(peer, c.execReader); peer.Close() }()
	go func() { defer wg.Done(); _, _ = io.Copy(c.stdin, peer); c.stdin.Close() }()
	var closeOnce sync.Once
	closeAll := func() {
		closeOnce.Do(func() {
			_ = local.Close()
			_ = peer.Close()
			c.kill()
			if output, ok := c.proc.Stdout().(io.Closer); ok {
				_ = output.Close()
			}
			_ = c.stdin.Close()
			// Only the read pump's actual exit marks execReadFinished. Closing
			// the carrier cannot manufacture completion before either pump joins.
			wg.Wait()
		})
	}
	return local, bufio.NewReaderSize(local, 64<<10), closeAll
}

func pumpExec(ctx context.Context, st *link.Stream, in *os.File, stdout, stderr io.Writer, alive time.Duration) (int, error) {
	records := link.NewExecRecords(st)
	cancelWrite := context.AfterFunc(ctx, func() { _ = st.SetWriteDeadline(time.Now().Add(holderInterruptWait)) })
	defer cancelWrite()
	var input *attachInput
	var inputCh chan []byte
	if in != nil {
		inputCh = make(chan []byte, 1)
		var err error
		input, err = startAttachInput(in, inputCh, nil)
		if err != nil {
			return 125, err
		}
		defer input.finish()
		input.ask()
	} else if err := records.CloseInput(); err != nil {
		return 125, err
	}
	type result struct {
		code int
		err  error
	}
	done := make(chan result, 1)
	go func() { code, err := link.ReadExecOutput(st, stdout, stderr); done <- result{code, err} }()
	var tick <-chan time.Time
	if alive > 0 {
		t := time.NewTicker(alive)
		defer t.Stop()
		tick = t.C
	}
	for {
		select {
		case r := <-done:
			return r.code, r.err
		case p, ok := <-inputCh:
			if !ok {
				inputCh = nil
				if err := records.CloseInput(); err != nil {
					st.Close()
					<-done
					return 125, err
				}
				continue
			}
			if _, err := records.Input(p); err != nil {
				st.Close()
				<-done
				return 125, err
			}
			input.ask()
		case <-tick:
			if err := records.KeepAlive(); err != nil {
				st.Close()
				<-done
				return 125, err
			}
		case <-ctx.Done():
			// A first interrupt reaches only this owned group. A bounded wait lets
			// its real signal status arrive; a second stage kills and releases it.
			_ = records.Signal(uint32(syscall.SIGINT))
			timer := time.NewTimer(holderInterruptWait)
			select {
			case r := <-done:
				timer.Stop()
				return r.code, r.err
			case <-timer.C:
			}
			_ = records.Signal(uint32(syscall.SIGKILL))
			_ = st.SetReadDeadline(time.Now().Add(holderInterruptWait))
			r := <-done
			if r.err != nil {
				return 125, ctx.Err()
			}
			return r.code, nil
		}
	}
}

const holderInterruptWait = 3 * time.Second

func (c *Conn) execReadFinished() {
	if c.execReadDone != nil {
		c.execReadOnce.Do(func() { close(c.execReadDone) })
	}
}

func (c *Conn) renewExecLease(ctx context.Context) {
	if c.leaser == nil {
		return
	}
	renew, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	busy, _ := c.leaser.Leases()
	_ = c.renewActiveLease(renew, c.leaser, busy, busy)
}
