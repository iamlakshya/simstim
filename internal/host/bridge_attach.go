package host

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"time"

	"github.com/iamlakshya/simstim/internal/diag"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

func attachError(err error) proto.Error {
	var pe *proto.Error
	if errors.As(err, &pe) {
		return *pe
	}
	return proto.Error{Code: "attach_failed", Message: err.Error(), Hint: "List sessions. It may have been deleted."}
}

func (b *bridge) attach(a proto.Attach) error {
	b.attachMu.Lock()
	defer b.attachMu.Unlock()
	b.mu.Lock()
	ended := b.ended
	b.mu.Unlock()
	if ended {
		return errors.New("the bridge ended")
	}
	if a.ForwardAgent {
		return UserError("ssh_agent_refused", "this attach uses a retired SSH agent forwarding path",
			"Update the client, then attach again.")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	b.closeHolder()
	b.traced = a.Trace != "" && (len(a.Categories) == 0 || slices.Contains(a.Categories, diag.CatAttach))
	if !validID(a.Session) {
		return fmt.Errorf("no session %s", a.Session)
	}
	d := net.Dialer{Timeout: 3 * time.Second}
	conn, err := d.DialContext(ctx, "unix", paths.Socket(a.Session))
	if err != nil {
		return fmt.Errorf("session %s is not running", a.Session)
	}
	deadline, _ := ctx.Deadline()
	_ = conn.SetDeadline(deadline)
	hw := proto.NewWriter(conn)
	hr := bufio.NewReaderSize(conn, 256<<10)
	if err := hw.WriteJSON(proto.THello, proto.Hello{Magic: proto.Magic, Protocol: version.Protocol, Version: version.Version, Role: "bridge"}); err != nil {
		_ = conn.Close()
		return fmt.Errorf("send the holder hello: %w", err)
	}
	f, err := proto.Read(hr)
	if err != nil || f.Type != proto.THello {
		_ = conn.Close()
		return errors.New("the session's holder did not answer")
	}
	b.mu.Lock()
	if b.ended {
		b.mu.Unlock()
		_ = conn.Close()
		return errors.New("the bridge ended")
	}
	b.holder, b.hw = conn, hw
	b.mu.Unlock()
	if err := hw.WriteJSON(proto.TAttach, a); err != nil {
		b.releaseHolder(conn)
		return fmt.Errorf("attach to the holder: %w", err)
	}
	_ = conn.SetDeadline(time.Time{})
	go b.readHolder(conn, hr)
	return nil
}

func (b *bridge) readHolder(conn net.Conn, r *bufio.Reader) {
	defer b.releaseHolder(conn)
	for {
		f, err := proto.Read(r)
		if err != nil {
			b.mu.Lock()
			mine := b.holder == conn
			b.mu.Unlock()
			if mine {
				_ = b.w.WriteJSON(proto.TError, proto.Error{Code: "session_gone", Message: "the session stopped"})
			}
			return
		}
		switch f.Type {
		case proto.TAttached, proto.TOutput, proto.TSnapshot, proto.TEnded, proto.TError:
			if err := b.w.Write(f.Type, f.Payload); err != nil {
				return
			}
		default:
			// Other frames from the holder are not for the client.
		}
	}
}

func (b *bridge) forward(f proto.Frame) {
	b.mu.Lock()
	hw := b.hw
	b.mu.Unlock()
	if hw != nil {
		_ = hw.Write(f.Type, f.Payload)
	}
}

// An old reader may finish after a replacement has attached and must
// leave that replacement alone.
func (b *bridge) releaseHolder(conn net.Conn) {
	b.mu.Lock()
	if b.holder == conn {
		b.holder, b.hw = nil, nil
	}
	b.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
}

func (b *bridge) closeHolder() {
	b.mu.Lock()
	c := b.holder
	b.holder, b.hw = nil, nil
	b.mu.Unlock()
	if c != nil {
		_ = c.Close()
	}
}

func (b *bridge) endAttach() {
	b.attachMu.Lock()
	defer b.attachMu.Unlock()
	b.mu.Lock()
	b.ended = true
	b.mu.Unlock()
	b.closeHolder()
}

func endBridgeAttachments() {
	liveBridges.Range(func(k, _ any) bool {
		k.(*bridge).endAttach()
		return true
	})
}
