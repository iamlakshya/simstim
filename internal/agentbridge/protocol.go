// Package agentbridge defines the private, bounded agent channel between
// an attached client and one host session. It carries no trust policy.
package agentbridge

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

const (
	// ProcessExitRefused belongs only to the fixed agent helper process.
	// A normal detach exits0. This status prevents a resource refusal from
	// being mistaken for an ordinary connection that should reconnect.
	ProcessExitRefused = 74
	// ProcessExitCleanup is an owned resource/cleanup refusal, never reconnect.
	ProcessExitCleanup = 75
	// Feature is offered only by helpers with the complete agent channel.
	Feature = "agent.bridge"
	// Role identifies the agent-only hello on the entry chosen before byte1.
	Role = "agent-client"
	// MaxIdentityHello bounds the initial identity frame before allocation.
	MaxIdentityHello = 16 << 10
	// MaxStreams includes open and pending agent connections.
	MaxStreams = 16
	// MaxHeader bounds both control and agent stream headers.
	MaxHeader = 256
	// MaxMessage bounds each control request or response, without its length.
	MaxMessage = 512
	// Heartbeat is the interval of the attached client's policy checks.
	Heartbeat = 5 * time.Second
	// LeaseTimeout bounds a dead client's host endpoint lifetime.
	LeaseTimeout = 15 * time.Second
	// PreparedTimeout permits the existing terminal attach deadline while
	// a bound endpoint remains unpublished.
	PreparedTimeout = 30 * time.Second
	// HandshakeTimeout bounds each link, bind or activation exchange.
	HandshakeTimeout = 10 * time.Second
)

// Scope names exactly one session and one fresh attachment lease.
type Scope struct {
	Session string `json:"session"`
	Lease   string `json:"lease"`
}

// Check validates the canonical ID and the cryptographic lease encoding.
func (s Scope) Check() error {
	if !api.ValidSessionID(s.Session) || len(s.Lease) != 32 {
		return errors.New("invalid agent channel scope")
	}
	for _, c := range s.Lease {
		if c < '0' || c > '9' {
			if c < 'a' || c > 'f' {
				return errors.New("invalid agent channel lease")
			}
		}
	}
	return nil
}

// NewLease makes a 128-bit lease token. Tokens never repeat by intent.
func NewLease() (string, error) {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", fmt.Errorf("make an agent lease: %w", err)
	}
	return hex.EncodeToString(token[:]), nil
}

// Header is the strict header of a ctl or agent stream.
type Header struct {
	Scope
	Kind string `json:"kind"`
}

// UnmarshalJSON rejects extra fields before either side opens an agent.
func (h *Header) UnmarshalJSON(b []byte) error {
	type plain Header
	if err := decode(b, (*plain)(h)); err != nil {
		return err
	}
	return h.Scope.Check()
}

// ControlHeader selects the metadata-only native binding probe for a
// client that owns a private binding collector. Agent headers stay Scope-only.
type ControlHeader struct {
	Scope
	Kind           string `json:"kind"`
	CollectBinding bool   `json:"collect_binding,omitempty"`
}

// UnmarshalJSON applies the same strict field and scope checks to control.
func (h *ControlHeader) UnmarshalJSON(b []byte) error {
	type plain ControlHeader
	if err := decode(b, (*plain)(h)); err != nil {
		return err
	}
	return h.Scope.Check()
}

// Request is one sequenced control action. Bind is sequence one.
type Request struct {
	Scope
	Action string `json:"action"`
	Seq    uint64 `json:"seq"`
}

// Reply echoes the entire scope, action and sequence that it answers.
type Reply struct {
	Scope
	Action string       `json:"action"`
	Seq    uint64       `json:"seq"`
	OK     bool         `json:"ok"`
	Error  *proto.Error `json:"error,omitempty"`
}

// Options uses the audited link's bounds without changing terminal traffic.
func Options() link.Options {
	return link.Options{Window: 64 << 10, MaxGuestStreams: MaxStreams, MaxSupervisorStreams: 1, MaxHeader: MaxHeader,
		HelloTimeout: HandshakeTimeout, Opener: map[link.Kind]link.Side{link.KindCtl: link.Supervisor, link.KindAgent: link.Guest}}
}

// WriteMessage queues one whole bounded control message in one Write.
// A client publishing a capability must then Flush before releasing policy.
func WriteMessage(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("encode agent control: %w", err)
	}
	if len(b) == 0 || len(b) > MaxMessage {
		return errors.New("agent control message is too large")
	}
	out := binary.LittleEndian.AppendUint32(nil, uint32(len(b))) //nolint:gosec // G115: the control message is bounded to 512 bytes above
	out = append(out, b...)
	n, err := w.Write(out)
	if err != nil {
		return fmt.Errorf("write agent control: %w", err)
	}
	if n != len(out) {
		return fmt.Errorf("write agent control: %w", io.ErrShortWrite)
	}
	return nil
}

// ReadMessage reads exactly one control message without unbounded allocation.
func ReadMessage(r io.Reader, v any) error {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return fmt.Errorf("read agent control length: %w", err)
	}
	n := binary.LittleEndian.Uint32(header[:])
	if n == 0 || n > MaxMessage {
		return errors.New("invalid agent control length")
	}
	b := make([]byte, int(n))
	if _, err := io.ReadFull(r, b); err != nil {
		return fmt.Errorf("read agent control: %w", err)
	}
	return decode(b, v)
}

func decode(b []byte, v any) error {
	if err := uniqueJSON(json.NewDecoder(bytes.NewReader(b)), 0); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return fmt.Errorf("decode agent control: %w", err)
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return errors.New("agent control has trailing data")
	}
	return nil
}

// uniqueJSON rejects duplicate names instead of letting a later field
// silently replace the scope or action that a reader already checked.
func uniqueJSON(d *json.Decoder, depth int) error {
	if depth > 16 {
		return errors.New("agent control nesting is too deep")
	}
	token, err := d.Token()
	if err != nil {
		return fmt.Errorf("read agent control token: %w", err)
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		return uniqueObject(d, depth)
	case '[':
		for d.More() {
			if err := uniqueJSON(d, depth+1); err != nil {
				return err
			}
		}
	default:
		return errors.New("invalid agent control delimiter")
	}
	return controlEnding(d)
}

func uniqueObject(d *json.Decoder, depth int) error {
	seen := map[string]bool{}
	for d.More() {
		key, err := d.Token()
		if err != nil {
			return fmt.Errorf("read agent control name: %w", err)
		}
		name, ok := key.(string)
		if !ok || !canonicalFieldName(name) || seen[name] {
			return errors.New("agent control has ambiguous field names")
		}
		seen[name] = true
		if err := uniqueJSON(d, depth+1); err != nil {
			return err
		}
	}
	return controlEnding(d)
}

func canonicalFieldName(name string) bool {
	for _, c := range name {
		if c > 127 {
			return false
		}
	}
	return name == strings.ToLower(name)
}

func controlEnding(d *json.Decoder) error {
	if _, err := d.Token(); err != nil {
		return fmt.Errorf("read agent control ending: %w", err)
	}
	return nil
}

// DecodeHello decodes the bounded agent-only identity hello with canonical
// field names. Ordinary bridge hellos retain their existing parser.
func DecodeHello(payload []byte) (proto.Hello, error) {
	var hello proto.Hello
	if len(payload) == 0 || len(payload) > MaxIdentityHello {
		return hello, errors.New("invalid agent identity hello length")
	}
	if err := decode(payload, &hello); err != nil {
		return hello, err
	}
	return hello, nil
}
