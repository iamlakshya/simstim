package client

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// The inner agent length is big-endian and includes the opcode. It is
// distinct from the bridge link's little-endian frame encoding.
func readAgentFirewallHead(r io.Reader) (head [5]byte, length uint32, opcode byte, err error) {
	n, err := io.ReadFull(r, head[:4])
	if err != nil {
		if n == 0 && err == io.EOF {
			return head, 0, 0, io.EOF
		}
		return head, 0, 0, fmt.Errorf("read agent length: %w", err)
	}
	length = agentFirewallUint32(head[:4])
	if length == 0 || length > agentFWMaxBody {
		return head, 0, 0, errors.New("invalid agent packet length")
	}
	if _, err := io.ReadFull(r, head[4:]); err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return head, 0, 0, fmt.Errorf("read agent opcode: %w", err)
	}
	return head, length, head[4], nil
}

func agentFirewallUint32(b []byte) uint32 { return binary.BigEndian.Uint32(b) }

func writeAgentFirewallBytes(w io.Writer, p []byte) error {
	for len(p) > 0 {
		n, err := w.Write(p)
		if err != nil {
			return fmt.Errorf("write agent bytes: %w", err)
		}
		if n <= 0 || n > len(p) {
			return io.ErrShortWrite
		}
		p = p[n:]
	}
	return nil
}

// Two errors keep a malformed remote packet separate from a failed source
// connection, which revokes the whole capability rather than one stream.
func copyAgentFirewallBody(dst io.Writer, src io.Reader, remaining uint32, buffer []byte) (readErr, writeErr error) {
	if len(buffer) == 0 {
		return errors.New("empty agent transfer buffer"), nil
	}
	for remaining > 0 {
		n := min(int(remaining), len(buffer))
		if _, err := io.ReadFull(src, buffer[:n]); err != nil {
			return fmt.Errorf("read agent body: %w", err), nil
		}
		if err := writeAgentFirewallBytes(dst, buffer[:n]); err != nil {
			return nil, err
		}
		remaining -= uint32(n) //nolint:gosec // G115: n is at most the 32 KiB fixed buffer and remaining
	}
	return nil, nil
}

func drainAgentFirewallBody(src io.Reader, remaining uint32, buffer []byte) error {
	if len(buffer) == 0 {
		return errors.New("empty agent drain buffer")
	}
	for remaining > 0 {
		n := min(int(remaining), len(buffer))
		if _, err := io.ReadFull(src, buffer[:n]); err != nil {
			return fmt.Errorf("read denied agent body: %w", err)
		}
		remaining -= uint32(n) //nolint:gosec // G115: n is at most the 32 KiB fixed buffer and remaining
	}
	return nil
}
