// Package guestpty frames one init-owned PTY's exact bytes and control on a
// dedicated inherited channel. It never starts, signals or waits for a PID.
package guestpty

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"reflect"
	"strings"
	"sync"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

// Role fixes the direction of this endpoint's closed frame set.
type Role uint8

// Init owns the child/PTY; Holder owns its render/ring/data plane.
const (
	Init Role = iota
	Holder
)

// MaxData bounds one raw data frame, without changing any attached byte.
const MaxData = 32 << 10
const maxControl = 1024

// Channel is one session/run capability on one inherited connection. A new
// run needs a new channel; no identifier is looked up to retarget an old one.
type Channel struct {
	conn              net.Conn
	role              Role
	session, token    string
	writer            *proto.Writer
	mu                sync.Mutex
	wroteEnd, readEnd bool
}

// New binds a fixed expected session/run. Values must come from init's actual
// member owner, never from an agent request, PID or caller-selected path.
func New(conn net.Conn, role Role, session, token string) (*Channel, error) {
	if conn == nil || role != Init && role != Holder || !api.ValidSessionID(session) || len(token) == 0 || len(token) > 128 {
		return nil, fmt.Errorf("invalid inherited PTY identity")
	}
	for _, b := range []byte(token) {
		if b < 0x21 || b > 0x7e {
			return nil, fmt.Errorf("invalid inherited PTY run token")
		}
	}
	return &Channel{conn: conn, role: role, session: session, token: token, writer: proto.NewWriter(conn)}, nil
}

// Close releases only this channel, including blocked owned reads/writes.
func (c *Channel) Close() error { return c.conn.Close() }

// Conn exposes deadline ownership for bounded stop/drain, not raw fd rights.
func (c *Channel) Conn() net.Conn { return c.conn }

func (c *Channel) prefix(body []byte) []byte {
	p := make([]byte, 3+len(c.session)+len(c.token)+len(body))
	p[0], p[1], p[2] = 2, byte(len(c.session)), byte(len(c.token))
	at := 3
	at += copy(p[at:], c.session)
	at += copy(p[at:], c.token)
	copy(p[at:], body)
	return p
}

func (c *Channel) unprefix(data []byte) ([]byte, error) {
	if len(data) < 3 || data[0] != 2 {
		return nil, fmt.Errorf("inherited PTY version is invalid")
	}
	size, tokenSize := int(data[1]), int(data[2])
	end := 3 + size + tokenSize
	if size != len(c.session) || tokenSize != len(c.token) || end > len(data) || string(data[3:3+size]) != c.session || string(data[3+size:end]) != c.token {
		return nil, fmt.Errorf("inherited PTY frame belongs to another session or run")
	}
	return data[end:], nil
}

func allowed(role Role, t proto.Type) bool {
	if role == Init {
		return t == proto.TOutput || t == proto.TEnded
	}
	return t == proto.TInput || t == proto.TResize || t == proto.TRequest
}

func (c *Channel) send(t proto.Type, body []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !allowed(c.role, t) || c.wroteEnd {
		return fmt.Errorf("inherited PTY frame is out of direction or follows its terminal result")
	}
	limit := maxControl
	if t == proto.TInput || t == proto.TOutput {
		limit = MaxData
	}
	if len(body) > limit {
		return fmt.Errorf("inherited PTY frame exceeds its bound")
	}
	if err := c.writer.Write(t, c.prefix(body)); err != nil {
		return err
	}
	if t == proto.TEnded {
		c.wroteEnd = true
	}
	return nil
}

// Output forwards bytes exactly, in bounded chunks, from init only.
func (c *Channel) Output(data []byte) error {
	for len(data) > 0 {
		n := min(len(data), MaxData)
		if err := c.send(proto.TOutput, data[:n]); err != nil {
			return err
		}
		data = data[n:]
	}
	return nil
}

// Input forwards terminal input exactly, from the holder only.
func (c *Channel) Input(data []byte) error {
	for len(data) > 0 {
		n := min(len(data), MaxData)
		if err := c.send(proto.TInput, data[:n]); err != nil {
			return err
		}
		data = data[n:]
	}
	return nil
}

// Resize requests the existing terminal dimensions, with no process handle.
func (c *Channel) Resize(size proto.Resize) error {
	data, err := json.Marshal(size)
	if err != nil {
		return err
	}
	return c.send(proto.TResize, data)
}

// Signal requests one explicitly allowed signal for this still-owned run.
func (c *Channel) Signal(number uint32) error {
	if !AllowedSignal(number) {
		return fmt.Errorf("inherited PTY signal is not allowed")
	}
	data, err := json.Marshal(struct {
		Signal uint32 `json:"signal"`
	}{number})
	if err != nil {
		return err
	}
	return c.send(proto.TRequest, data)
}

// End is called only after the owning waiter and final output reader joined.
func (c *Channel) End(result proto.Ended) error {
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return c.send(proto.TEnded, data)
}

// Read accepts one correctly scoped frame from the opposite role. One caller
// owns reads; its output loop delivers the terminal result after final bytes.
func (c *Channel) Read() (proto.Frame, error) {
	// Check this channel's bound BEFORE allocation. The outer proto cap is
	// larger and must not become a per-member untrusted allocation budget.
	var header [4]byte
	if _, err := io.ReadFull(c.conn, header[:]); err != nil {
		return proto.Frame{}, err
	}
	size := binary.LittleEndian.Uint32(header[:])
	bound := 1 + 3 + len(c.session) + len(c.token) + MaxData
	if size == 0 || uint64(size) > uint64(bound) {
		c.Close()
		return proto.Frame{}, fmt.Errorf("inherited PTY frame exceeds its bound")
	}
	body := make([]byte, int(size))
	if _, err := io.ReadFull(c.conn, body); err != nil {
		return proto.Frame{}, err
	}
	frame := proto.Frame{Type: proto.Type(body[0]), Payload: body[1:]}
	other := Init
	if c.role == Init {
		other = Holder
	}
	if !allowed(other, frame.Type) || c.readEnd {
		c.Close()
		return proto.Frame{}, fmt.Errorf("unexpected inherited PTY frame direction or terminal ordering")
	}
	payload, err := c.unprefix(frame.Payload)
	if err != nil {
		c.Close()
		return proto.Frame{}, err
	}
	limit := maxControl
	if frame.Type == proto.TInput || frame.Type == proto.TOutput {
		limit = MaxData
	}
	if len(payload) > limit {
		c.Close()
		return proto.Frame{}, fmt.Errorf("inherited PTY body exceeds its bound")
	}
	frame.Payload = payload
	if frame.Type == proto.TEnded {
		c.readEnd = true
	}
	return frame, nil
}

// DecodeControl accepts one complete closed JSON control body.
func DecodeControl(data []byte, value any) error {
	if len(data) > maxControl {
		return fmt.Errorf("inherited PTY control exceeds its bound")
	}
	kind := reflect.TypeOf(value)
	if kind == nil || kind.Kind() != reflect.Pointer || kind.Elem().Kind() != reflect.Struct {
		return fmt.Errorf("inherited PTY control needs one closed struct")
	}
	kind = kind.Elem()
	allowed := map[string]bool{}
	for i := 0; i < kind.NumField(); i++ {
		name := strings.Split(kind.Field(i).Tag.Get("json"), ",")[0]
		if name != "" && name != "-" {
			allowed[name] = true
		}
	}
	keys := make([]string, 0, len(allowed))
	for key := range allowed {
		keys = append(keys, key)
	}
	fields, err := api.JSONObjectFields(data, keys...)
	if err != nil {
		return err
	}
	for key := range fields {
		if !allowed[key] {
			return fmt.Errorf("inherited PTY control has unknown fields")
		}
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("inherited PTY control has trailing content")
	}
	return nil
}

// AllowedSignal is the closed guest-only terminal control set.
func AllowedSignal(number uint32) bool {
	return link.ValidExecSignal(number) || number == 28 // SIGWINCH for owned PTY repaint.
}
