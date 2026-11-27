package microvm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/wall"
)

type runnerRequest struct {
	ID         uint64                    `json:"id"`
	Op         string                    `json:"op"`
	Checkpoint *checkpointActorRequestV3 `json:"checkpoint,omitempty"`
}
type runnerExit struct {
	Code   int    `json:"code"`
	Signal string `json:"signal,omitempty"`
	Error  string `json:"error,omitempty"`
}
type runnerMessage struct {
	Kind       string                  `json:"kind"`
	ID         uint64                  `json:"id,omitempty"`
	State      string                  `json:"state,omitempty"`
	Error      string                  `json:"error,omitempty"`
	Exit       *runnerExit             `json:"exit,omitempty"`
	Session    string                  `json:"session,omitempty"`
	Driver     string                  `json:"driver,omitempty"`
	Checkpoint *checkpointActorReplyV3 `json:"checkpoint,omitempty"`
}

type runnerAncillaryReader interface {
	io.Reader
	takeControl() proto.UnixControlV1
	Close() error
}

type runnerNativeProof interface {
	acceptReady(runnerMessage, *proto.UnixControlV1) error
	Close() error
}

// Only the private concrete native mode constructor registers this consumer.
// There is no public callback/DTO or mode-phase registry.
type runnerModePhase interface {
	acceptExecChild(runnerMessage, *proto.UnixControlV1) error
}

// Registered only by the same concrete native phase; wire data never issues it.
type runnerReadonlyPhase interface {
	acceptReadonlyDiskV2(runnerMessage, *proto.UnixControlV1) error
	acceptReadonlyReplyV2(runnerMessage, *proto.UnixControlV1) error
	isReadonlyV1() bool
	readonlyControlEndedV1()
	readonlyExitV1() (int, string, error)
}

type runnerControl struct {
	conn                 net.Conn
	encoder              *json.Encoder
	callGate             chan struct{}
	mu                   sync.Mutex
	next, pending        uint64
	reply                chan runnerMessage
	ready, done          chan struct{}
	readyOnce, closeOnce sync.Once
	exit                 wall.Exit
	gotReady, gotExit    bool
	input                io.Reader
	ancillary            runnerAncillaryReader
	native               runnerNativeProof
	mode                 runnerModePhase
}

func newRunnerControl(conn net.Conn) *runnerControl {
	return startRunnerControl(conn, conn, nil, nil)
}

func startRunnerControl(conn net.Conn, input io.Reader, ancillary runnerAncillaryReader, native runnerNativeProof) *runnerControl {
	c := &runnerControl{conn: conn, input: input, ancillary: ancillary, native: native, encoder: json.NewEncoder(conn), callGate: make(chan struct{}, 1), reply: make(chan runnerMessage, 1), ready: make(chan struct{}), done: make(chan struct{}), exit: wall.Exit{Code: -1}}
	go c.read()
	return c
}

func (c *runnerControl) read() {
	defer close(c.done)
	if c.ancillary != nil {
		defer c.ancillary.Close()
	}
	if c.native != nil {
		defer c.native.Close()
	}
	scanner := bufio.NewScanner(c.input)
	scanner.Buffer(make([]byte, 4096), checkpointActorFrameV3)
	for scanner.Scan() {
		var control proto.UnixControlV1
		if c.ancillary != nil {
			control = c.ancillary.takeControl()
		}
		if err := c.acceptMessage(scanner.Bytes(), &control); err != nil {
			control.Close()
			c.fail(err)
			return
		}
		control.Close()
	}
	if err := scanner.Err(); err != nil {
		c.fail(fmt.Errorf("read private VMM control: %w", err))
	} else if !c.gotExit {
		c.mu.Lock()
		phase, readonly := c.mode.(runnerReadonlyPhase)
		c.mu.Unlock()
		if readonly && phase.isReadonlyV1() {
			phase.readonlyControlEndedV1()
		} else {
			c.fail(fmt.Errorf("the VMM runner ended without its owned process result"))
		}
	}
}

func (c *runnerControl) acceptMessage(data []byte, control *proto.UnixControlV1) error {
	var message runnerMessage
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&message); err != nil {
		return fmt.Errorf("invalid private VMM control reply: %w", err)
	}
	var more any
	if err := decoder.Decode(&more); err != io.EOF {
		return fmt.Errorf("private VMM control has trailing data")
	}
	if message.Kind != "ready" && message.Kind != "mode_exec_child" && message.Kind != "mode_init_disk_v2" && message.Kind != "mode_disk_reply_v2" && len(control.Files) != 0 {
		return fmt.Errorf("private VMM descriptors arrived outside their ready frame")
	}
	switch message.Kind {
	case "mode_init_disk_v2":
		c.mu.Lock()
		phase, ok := c.mode.(runnerReadonlyPhase)
		c.mu.Unlock()
		if !ok || !phase.isReadonlyV1() || c.gotReady || c.gotExit {
			return fmt.Errorf("the native disk has no original read-only phase")
		}
		if err := phase.acceptReadonlyDiskV2(message, control); err != nil {
			return err
		}
	case "mode_exec_child":
		c.mu.Lock()
		mode := c.mode
		c.mu.Unlock()
		if mode == nil || c.gotReady || c.gotExit || len(control.Files) != 1 {
			return fmt.Errorf("private VMM final child has no original native mode consumer")
		}
		if err := mode.acceptExecChild(message, control); err != nil {
			return err
		}
	case "ready":
		if c.gotReady {
			return fmt.Errorf("duplicate private VMM ready reply")
		}
		if c.native != nil {
			if err := c.native.acceptReady(message, control); err != nil {
				return err
			}
		} else if len(control.Files) != 0 {
			return fmt.Errorf("private VMM ready has no native descriptor consumer")
		}
		c.gotReady = true
		c.readyOnce.Do(func() { close(c.ready) })
	case "reply", "mode_disk_reply_v2":
		c.mu.Lock()
		phase, readonly := c.mode.(runnerReadonlyPhase)
		wanted := c.pending
		c.mu.Unlock()
		if message.Kind == "mode_disk_reply_v2" {
			if !readonly || !phase.isReadonlyV1() || !c.gotReady || c.gotExit || wanted == 0 || message.ID != wanted {
				return fmt.Errorf("the current disk reply has no original pending native request")
			}
			if err := phase.acceptReadonlyReplyV2(message, control); err != nil {
				return err
			}
		} else if readonly && phase.isReadonlyV1() {
			return fmt.Errorf("the original read-only request reply omitted its native disk roles")
		}
		c.mu.Lock()
		wanted = c.pending
		c.mu.Unlock()
		if message.ID == wanted && wanted != 0 {
			select {
			case c.reply <- message:
			default:
				return fmt.Errorf("duplicate private VMM control reply")
			}
		}
	case "exit":
		if message.Exit == nil || c.gotExit {
			return fmt.Errorf("private VMM exit reply is missing its result")
		}
		c.gotExit = true
		exit := wall.Exit{Code: message.Exit.Code, Signal: message.Exit.Signal}
		if message.Exit.Error != "" {
			exit.Err = fmt.Errorf("VMM exited: %s", message.Exit.Error)
		}
		c.mu.Lock()
		c.exit = exit
		c.mu.Unlock()
	default:
		return fmt.Errorf("unknown private VMM control reply")
	}
	return nil
}

func (c *runnerControl) fail(err error) {
	c.mu.Lock()
	if c.exit.Err == nil {
		c.exit.Err = err
	}
	c.mu.Unlock()
	c.conn.Close()
}
func (c *runnerControl) close() { c.closeOnce.Do(func() { c.conn.Close() }) }
func (c *runnerControl) finish() {
	defer c.recordReadonlyPhysicalExitV1()
	select {
	case <-c.done:
	case <-time.After(time.Second):
		c.close()
		<-c.done
	}
}
func (c *runnerControl) processExit() wall.Exit { c.mu.Lock(); defer c.mu.Unlock(); return c.exit }
func (c *runnerControl) waitReady(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return fmt.Errorf("the VMM runner ended before the guest transport was ready")
	case <-c.ready:
		return ctx.Err()
	}
}

func (c *runnerControl) call(ctx context.Context, op string) ([]byte, error) {
	if op != "state" && op != "power-button" {
		return nil, fmt.Errorf("private VMM control operation is not allowed")
	}
	reply, err := c.callRequestV3(ctx, runnerRequest{Op: op})
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		State string `json:"state,omitempty"`
	}{reply.State})
}

func (c *runnerControl) callRequestV3(ctx context.Context, request runnerRequest) (runnerMessage, error) {
	if request.Op != "state" && request.Op != "power-button" && request.Op != "checkpoint_v3" || (request.Op == "checkpoint_v3") != (request.Checkpoint != nil) {
		return runnerMessage{}, fmt.Errorf("private VMM request changed its fixed purpose")
	}
	select {
	case c.callGate <- struct{}{}:
		defer func() { <-c.callGate }()
	case <-ctx.Done():
		return runnerMessage{}, ctx.Err()
	}
	if ctx.Err() != nil {
		return runnerMessage{}, ctx.Err()
	}
	c.mu.Lock()
	c.next++
	id := c.next
	c.pending = id
	c.mu.Unlock()
	defer func() { c.mu.Lock(); c.pending = 0; c.mu.Unlock() }()
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(5 * time.Second)
	}
	if err := c.conn.SetWriteDeadline(deadline); err != nil {
		return runnerMessage{}, fmt.Errorf("set private VMM control deadline: %w", err)
	}
	interrupted := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { _ = c.conn.SetWriteDeadline(time.Now()); close(interrupted) })
	defer func() {
		if !stop() {
			<-interrupted
		}
		_ = c.conn.SetWriteDeadline(time.Time{})
	}()
	request.ID = id
	if err := c.encoder.Encode(request); err != nil {
		if ctx.Err() != nil {
			return runnerMessage{}, ctx.Err()
		}
		return runnerMessage{}, fmt.Errorf("send private VMM control: %w", err)
	}
	for {
		select {
		case <-ctx.Done():
			return runnerMessage{}, ctx.Err()
		case <-c.done:
			return runnerMessage{}, fmt.Errorf("the private VMM control channel ended")
		case reply := <-c.reply:
			if ctx.Err() != nil {
				return runnerMessage{}, ctx.Err()
			}
			if reply.ID != id {
				continue
			}
			if reply.Error != "" {
				return runnerMessage{}, fmt.Errorf("VMM control failed: %s", reply.Error)
			}
			return reply, nil
		}
	}
}

func (c *runnerControl) recordReadonlyPhysicalExitV1() {
	c.mu.Lock()
	phase, ok := c.mode.(runnerReadonlyPhase)
	c.mu.Unlock()
	if !ok || !phase.isReadonlyV1() {
		return
	}
	code, signal, err := phase.readonlyExitV1()
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		c.exit.Err = errors.Join(c.exit.Err, err)
		return
	}
	c.exit.Code, c.exit.Signal, c.gotExit = code, signal, true
}
