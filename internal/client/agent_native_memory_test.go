//go:build darwin || linux

package client

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
)

// This native-image shim exercises the production process, collector, log
// and transport resource paths. It proves no native OpenSSH provenance.
func runAgentMemoryNative(cfg agentMemoryConfig) error {
	for _, arg := range os.Args[1:] {
		if arg == "-G" {
			_, err := io.WriteString(os.Stdout, "hostname memory.invalid\nuser memory\nport 22\n")
			return err
		}
	}
	var logPath, collector string
	for i, arg := range os.Args[1:] {
		if arg == "-E" && i+2 < len(os.Args) {
			logPath = os.Args[i+2]
		}
		if value, ok := strings.CutPrefix(arg, "ForwardAgent="); ok {
			collector = value
		}
	}
	if logPath == "" || collector == "" || collector == "no" {
		return errors.New("native resource fixture lacks private log or collector")
	}
	// A killed native-image fixture cannot run listener cleanup. Each
	// owned process therefore receives its own generated socket path.
	cfg.Forward = fmt.Sprintf("%s-%d", cfg.Forward, os.Getpid())
	control, err := agentMemoryConnect(cfg.NativeControl)
	if err != nil {
		return err
	}
	defer control.conn.Close()
	if err := control.send(agentMemoryEvent{Role: "native", Event: "hello"}); err != nil {
		return err
	}
	var begin agentMemoryCommand
	if err := control.dec.Decode(&begin); err != nil || begin.Command != "begin" {
		return errors.New("native fixture lacks begin")
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: cfg.Forward, Net: "unix"})
	if err != nil {
		return err
	}
	defer listener.Close()
	if err := os.Chmod(cfg.Forward, 0600); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, cfg.Binary)
	cmd.Env = agentMemoryReplaceEnv(os.Environ(), map[string]string{"AGENT_MODE_MEMORY_ROLE": "host", "SSH_AUTH_SOCK": cfg.Forward, "AGENT_MODE_MEMORY_FORWARD": cfg.Forward}, false)
	input, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	var workers sync.WaitGroup
	h2c, c2h := newAgentMemoryGate(), newAgentMemoryGate()
	workers.Go(func() { _ = agentMemoryFrameCopy(os.Stdout, output, h2c, control, "ingress", 64<<10) })
	workers.Go(func() { _ = agentMemoryFrameCopy(input, os.Stdin, c2h, control, "egress", (256+32)<<10) })
	workers.Go(func() {
		err := agentMemoryNativeCapture(ctx, cfg, listener, collector, logPath)
		e := agentMemoryEvent{Role: "native", Event: "capture"}
		if err != nil {
			e.Error = err.Error()
		}
		_ = control.send(e)
	})
	defer func() {
		cancel()
		h2c.set(false)
		c2h.set(false)
		_ = listener.Close()
		_ = input.Close()
		_ = output.Close()
		_ = os.Stdin.Close()
		workers.Wait()
		_ = cmd.Wait()
	}()
	for {
		var c agentMemoryCommand
		if err := control.dec.Decode(&c); err != nil {
			return err
		}
		switch c.Command {
		case "gate":
			gate := h2c
			if c.Gate == "c2h" {
				gate = c2h
			} else if c.Gate != "h2c" {
				return errors.New("unknown memory direction")
			}
			gate.set(c.Phase == "closed")
			if err := control.send(agentMemoryEvent{Role: "native", Event: "gate", Phase: c.Gate + ":" + c.Phase}); err != nil {
				return err
			}
		case "stop":
			return nil
		default:
			return errors.New("unknown native resource command")
		}
	}
}

func agentMemoryNativeCapture(ctx context.Context, cfg agentMemoryConfig, listener *net.UnixListener, collector, logPath string) error {
	probe, err := listener.AcceptUnix()
	if err != nil {
		return err
	}
	defer probe.Close()
	// Keep this owned FIFO writer open during residency measurements. Its
	// closing on process exit makes the real final scan observe EOF.
	log, err := os.OpenFile(logPath, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer log.Close()
	transcript, err := os.Open(cfg.Transcript)
	if err != nil {
		return err
	}
	_, err = io.Copy(log, transcript)
	_ = transcript.Close()
	if err != nil {
		return err
	}
	conn, err := net.DialTimeout("unix", collector, 10*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	binding, err := os.Open(cfg.Binding)
	if err != nil {
		return err
	}
	_, err = io.Copy(conn, binding)
	_ = binding.Close()
	if err != nil {
		return err
	}
	var ack [5]byte
	if _, err := io.ReadFull(conn, ack[:]); err != nil {
		return err
	}
	if binary.BigEndian.Uint32(ack[:4]) != 1 || ack[4] != agentFWSuccess {
		return errors.New("collector refused maximum signed resource record")
	}
	_ = probe.Close()
	// The process owns this writer until its framed connection is closed.
	_, err = io.Copy(io.Discard, conn)
	if err != nil {
		return err
	}
	// The collector closes its capture socket after prepare. Do not close
	// the native log then: retain its live pump until the process ends.
	<-ctx.Done()
	return nil
}

type agentMemoryGate struct {
	mu      sync.Mutex
	changed *sync.Cond
	closed  bool
}

func newAgentMemoryGate() *agentMemoryGate {
	g := &agentMemoryGate{}
	g.changed = sync.NewCond(&g.mu)
	return g
}
func (g *agentMemoryGate) set(closed bool) {
	g.mu.Lock()
	g.closed = closed
	g.changed.Broadcast()
	g.mu.Unlock()
}
func (g *agentMemoryGate) wait() {
	g.mu.Lock()
	for g.closed {
		g.changed.Wait()
	}
	g.mu.Unlock()
}

// Frame boundaries give the load driver a byte-count barrier without any
// hook or allocation in the measured actors. Every byte is forwarded intact.
func agentMemoryFrameCopy(dst io.Writer, src io.Reader, gate *agentMemoryGate, control *agentMemoryControl, event string, threshold uint64) error {
	var head [9]byte
	var buffer [16 << 10]byte
	counts := map[uint32]uint64{}
	reported := false
	for {
		if _, err := io.ReadFull(src, head[:4]); err != nil {
			return err
		}
		n := binary.LittleEndian.Uint32(head[:4])
		if n == 0 || n > proto.MaxFrame {
			return errors.New("resource fixture received invalid frame")
		}
		prefix := min(int(n), 5)
		if _, err := io.ReadFull(src, head[4:4+prefix]); err != nil {
			return err
		}
		gate.wait()
		if err := agentMemoryWrite(dst, head[:4+prefix]); err != nil {
			return err
		}
		remaining := int(n) - prefix
		for remaining > 0 {
			k := min(remaining, len(buffer))
			if _, err := io.ReadFull(src, buffer[:k]); err != nil {
				return err
			}
			gate.wait()
			if err := agentMemoryWrite(dst, buffer[:k]); err != nil {
				return err
			}
			remaining -= k
		}
		if n >= 5 && proto.Type(head[4]) == proto.TStreamData {
			id := binary.LittleEndian.Uint32(head[5:9])
			if id%2 == 0 {
				counts[id] += uint64(n - 5)
			}
			ready := len(counts) == 16
			for _, n := range counts {
				ready = ready && n >= threshold
			}
			if ready && !reported {
				reported = true
				if err := control.send(agentMemoryEvent{Role: "native", Event: event}); err != nil {
					return err
				}
			}
		}
	}
}

func agentMemoryWrite(w io.Writer, p []byte) error {
	for len(p) > 0 {
		n, err := w.Write(p)
		if err != nil {
			return err
		}
		if n <= 0 || n > len(p) {
			return fmt.Errorf("resource fixture write: %w", io.ErrShortWrite)
		}
		p = p[n:]
	}
	return nil
}
