package egress

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"

	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

// Client is the supervisor's handle to a running egress process over one end
// of their socket pair. It sends init and live policy updates on ctl, receives
// audit events, and opens egress and dns streams for guest flows.
type Client struct {
	sess   *link.Session
	ctl    *link.Stream
	writer *proto.Writer
	audit  AuditSink

	nextID  atomic.Uint64
	mu      sync.Mutex
	pending map[uint64]chan proto.Response
	closed  bool
	readErr error
	done    chan struct{}
}

// NewClient attaches to conn (the supervisor's end of the egress socket pair),
// opens the ctl stream, runs the init handshake, and starts dispatching audit
// events to auditSink.
func NewClient(ctx context.Context, conn net.Conn, args StartArgs, auditSink AuditSink) (*Client, error) {
	sess := link.New(conn, link.Supervisor, link.Options{Opener: EgressOpener})
	ctl, err := sess.Open(ctx, link.KindCtl, nil)
	if err != nil {
		_ = sess.Close()
		return nil, fmt.Errorf("egress client: open ctl stream: %w. Try: verify the egress process started", err)
	}
	c := &Client{
		sess:    sess,
		ctl:     ctl,
		writer:  proto.NewWriter(ctl),
		audit:   auditSink,
		pending: map[uint64]chan proto.Response{},
		done:    make(chan struct{}),
	}
	go c.readLoop(bufio.NewReader(ctl))

	if _, err := c.call(ctx, "init", args); err != nil {
		_ = c.Close()
		return nil, err
	}
	return c, nil
}

func (c *Client) readLoop(r *bufio.Reader) {
	defer close(c.done)
	for {
		frame, err := proto.Read(r)
		if err != nil {
			c.failPending(err)
			return
		}
		switch frame.Type {
		case proto.TResponse:
			var resp proto.Response
			if err := json.Unmarshal(frame.Payload, &resp); err != nil {
				continue
			}
			c.mu.Lock()
			ch, ok := c.pending[resp.ID]
			if ok {
				delete(c.pending, resp.ID)
			}
			c.mu.Unlock()
			if ok {
				ch <- resp
			}
		case proto.TEvent:
			if c.audit == nil {
				continue
			}
			var ev AuditEvent
			if err := json.Unmarshal(frame.Payload, &ev); err == nil {
				c.audit(ev)
			}
		default:
		}
	}
}

func (c *Client) failPending(err error) {
	c.mu.Lock()
	c.closed = true
	c.readErr = err
	for id, ch := range c.pending {
		delete(c.pending, id)
		close(ch)
	}
	c.mu.Unlock()
}

func (c *Client) call(ctx context.Context, op string, args any) (json.RawMessage, error) {
	var raw json.RawMessage
	if args != nil {
		b, err := json.Marshal(args)
		if err != nil {
			return nil, fmt.Errorf("egress client: marshal %s args: %w", op, err)
		}
		raw = b
	}
	id := c.nextID.Add(1)
	ch := make(chan proto.Response, 1)
	c.mu.Lock()
	if c.closed {
		err := c.readErr
		c.mu.Unlock()
		return nil, fmt.Errorf("egress client: %s after egress stopped: %w. Try: restart the session", op, err)
	}
	c.pending[id] = ch
	c.mu.Unlock()

	if err := c.writer.WriteJSONCtx(ctx, proto.TRequest, proto.Request{ID: id, Op: op, Args: raw}); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, fmt.Errorf("egress client: send %s: %w. Try: check that the egress process is running", op, err)
	}

	select {
	case resp, ok := <-ch:
		if !ok {
			return nil, errors.New("egress client: egress process closed before answering. Try: check the session log")
		}
		if !resp.OK {
			if resp.Error != nil {
				return nil, resp.Error
			}
			return nil, fmt.Errorf("egress client: %s failed. Try: check the session policy", op)
		}
		return resp.Result, nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, ctx.Err()
	}
}

// Allow adds a live allow rule on the running egress process.
func (c *Client) Allow(ctx context.Context, target string) error {
	_, err := c.call(ctx, "policy.allow", TargetArgs{Target: target})
	return err
}

// Deny adds a live deny rule on the running egress process.
func (c *Client) Deny(ctx context.Context, target string) error {
	_, err := c.call(ctx, "policy.deny", TargetArgs{Target: target})
	return err
}

// Reload replaces the policy TOML on the running egress process atomically.
func (c *Client) Reload(ctx context.Context, policyTOML string) error {
	_, err := c.call(ctx, "policy.reload", ReloadArgs{PolicyTOML: policyTOML})
	return err
}

// AllowTools grants model API tool names (such as web_fetch or mcp) for host
// on the running egress process.
func (c *Client) AllowTools(ctx context.Context, host string, tools []string) error {
	_, err := c.call(ctx, "policy.allow_tools", AllowToolsArgs{Host: host, Tools: tools})
	return err
}

// ArmSecrets installs or updates bound credentials in the egress process's
// memory (decision 0032).
func (c *Client) ArmSecrets(ctx context.Context, creds []BoundCredential) error {
	_, err := c.call(ctx, "secrets.arm", ArmSecretsArgs{Secrets: creds})
	return err
}

// Usage queries the running egress process for cumulative model token usage.
func (c *Client) Usage(ctx context.Context) (UsageSummary, error) {
	raw, err := c.call(ctx, "usage", nil)
	if err != nil {
		return UsageSummary{}, err
	}
	var res UsageSummary
	if err := json.Unmarshal(raw, &res); err != nil {
		return UsageSummary{}, fmt.Errorf("egress client: decode usage: %w", err)
	}
	return res, nil
}

// Status queries the running egress process for its status and dumpable flag.
func (c *Client) Status(ctx context.Context) (StatusResult, error) {
	raw, err := c.call(ctx, "status", nil)
	if err != nil {
		return StatusResult{}, err
	}
	var res StatusResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return StatusResult{}, fmt.Errorf("egress client: decode status: %w", err)
	}
	return res, nil
}

// OpenEgress opens a new network flow stream to the egress process.
func (c *Client) OpenEgress(ctx context.Context, hdr StreamHeader) (*link.Stream, error) {
	return c.sess.Open(ctx, link.KindEgress, hdr)
}

// OpenDNS opens a new DNS query stream to the egress process.
func (c *Client) OpenDNS(ctx context.Context) (*link.Stream, error) {
	return c.sess.Open(ctx, link.KindDNS, nil)
}

// Done closes when the egress process link ends.
func (c *Client) Done() <-chan struct{} { return c.sess.Done() }

// Close shuts down the link to the egress process.
func (c *Client) Close() error {
	_ = c.ctl.Close()
	err := c.sess.Close()
	<-c.done
	return err
}
