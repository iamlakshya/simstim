package chv

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/wall"
)

type runnerRequest struct {
	ID uint64 `json:"id"`
	Op string `json:"op"`
}
type runnerExit struct {
	Code   int    `json:"code"`
	Signal string `json:"signal,omitempty"`
	Error  string `json:"error,omitempty"`
}
type runnerMessage struct {
	Kind  string      `json:"kind"`
	ID    uint64      `json:"id,omitempty"`
	State string      `json:"state,omitempty"`
	Error string      `json:"error,omitempty"`
	Exit  *runnerExit `json:"exit,omitempty"`
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
}

func newRunnerControl(conn net.Conn) *runnerControl {
	c := &runnerControl{conn: conn, encoder: json.NewEncoder(conn), callGate: make(chan struct{}, 1), reply: make(chan runnerMessage, 1), ready: make(chan struct{}), done: make(chan struct{}), exit: wall.Exit{Code: -1}}
	go c.read()
	return c
}

func (c *runnerControl) read() {
	defer close(c.done)
	scanner := bufio.NewScanner(c.conn)
	scanner.Buffer(make([]byte, 4096), 64<<10)
	for scanner.Scan() {
		var message runnerMessage
		decoder := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&message); err != nil {
			c.fail(fmt.Errorf("invalid private VMM control reply: %w", err))
			return
		}
		switch message.Kind {
		case "ready":
			if c.gotReady {
				c.fail(fmt.Errorf("duplicate private VMM ready reply"))
				return
			}
			c.gotReady = true
			c.readyOnce.Do(func() { close(c.ready) })
		case "reply":
			c.mu.Lock()
			wanted := c.pending
			c.mu.Unlock()
			if message.ID == wanted && wanted != 0 {
				select {
				case c.reply <- message:
				default:
					c.fail(fmt.Errorf("duplicate private VMM control reply"))
					return
				}
			}
		case "exit":
			if message.Exit == nil || c.gotExit {
				c.fail(fmt.Errorf("private VMM exit reply is missing its result"))
				return
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
			c.fail(fmt.Errorf("unknown private VMM control reply"))
			return
		}
	}
	if err := scanner.Err(); err != nil {
		c.fail(fmt.Errorf("read private VMM control: %w", err))
	} else if !c.gotExit {
		c.fail(fmt.Errorf("the VMM runner ended without its owned process result"))
	}
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
	select {
	case c.callGate <- struct{}{}:
		defer func() { <-c.callGate }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
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
		return nil, fmt.Errorf("set private VMM control deadline: %w", err)
	}
	interrupted := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { _ = c.conn.SetWriteDeadline(time.Now()); close(interrupted) })
	defer func() {
		if !stop() {
			<-interrupted
		}
		_ = c.conn.SetWriteDeadline(time.Time{})
	}()
	if err := c.encoder.Encode(runnerRequest{ID: id, Op: op}); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("send private VMM control: %w", err)
	}
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-c.done:
			return nil, fmt.Errorf("the private VMM control channel ended")
		case reply := <-c.reply:
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if reply.ID != id {
				continue
			}
			if reply.Error != "" {
				return nil, fmt.Errorf("VMM control failed: %s", reply.Error)
			}
			return json.Marshal(struct {
				State string `json:"state,omitempty"`
			}{reply.State})
		}
	}
}
