package supervisor

import (
	"bytes"
	"encoding/binary"
	"io"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

const namedSSHMaxPacket = 64 << 10

// Only the three permitted protocol operations reach the library. In
// particular, its legacy remove-all opcode otherwise returns success without
// calling Agent.RemoveAll. Filtering also avoids parsing private key uploads.
func serveNamedSSHProtocol(a *namedSSHConnection, stream io.ReadWriter) error {
	var header [4]byte
	for {
		if _, err := io.ReadFull(stream, header[:]); err != nil {
			return err
		}
		size := binary.BigEndian.Uint32(header[:])
		if size == 0 || size > namedSSHMaxPacket {
			return errNamedSSHRefused
		}
		packet := make([]byte, int(size)+4)
		copy(packet, header[:])
		if _, err := io.ReadFull(stream, packet[4:]); err != nil {
			return err
		}
		if !validNamedSSHOperation(packet[4:]) {
			if _, err := stream.Write([]byte{0, 0, 0, 1, 5}); err != nil {
				return err
			}
			continue
		}
		one := &namedSSHPacket{Reader: bytes.NewReader(packet), Writer: stream}
		if err := agent.ServeAgent(a, one); err != nil && err != io.EOF {
			return err
		}
	}
}

type namedSSHPacket struct {
	io.Reader
	io.Writer
}

func validNamedSSHOperation(packet []byte) bool {
	if len(packet) == 0 {
		return false
	}
	switch packet[0] {
	case 11:
		return len(packet) == 1
	case 13:
		var request struct {
			KeyBlob []byte `sshtype:"13"`
			Data    []byte
			Flags   uint32
		}
		if ssh.Unmarshal(packet, &request) != nil {
			return false
		}
		_, err := ssh.ParsePublicKey(request.KeyBlob)
		return err == nil
	case 27:
		var request struct {
			Name     string `sshtype:"27"`
			Contents []byte `ssh:"rest"`
		}
		return ssh.Unmarshal(packet, &request) == nil
	default:
		return false
	}
}

type namedSSHBinding struct {
	fingerprint string
	sessionID   []byte
	forwarding  bool
}

func (c *namedSSHConnection) Extension(name string, contents []byte) ([]byte, error) {
	if name != "session-bind@openssh.com" {
		return nil, agent.ErrExtensionUnsupported
	}
	keyBlob, rest, ok := namedSSHString(contents)
	if !ok {
		return nil, errNamedSSHRefused
	}
	sessionID, rest, ok := namedSSHString(rest)
	if !ok || len(sessionID) == 0 {
		return nil, errNamedSSHRefused
	}
	signatureBlob, rest, ok := namedSSHString(rest)
	if !ok || len(rest) != 1 || rest[0] > 1 {
		return nil, errNamedSSHRefused
	}
	key, err := ssh.ParsePublicKey(keyBlob)
	if err != nil {
		return nil, errNamedSSHRefused
	}
	var signature ssh.Signature
	if ssh.Unmarshal(signatureBlob, &signature) != nil || key.Verify(sessionID, &signature) != nil {
		return nil, errNamedSSHRefused
	}
	fingerprint := ssh.FingerprintSHA256(key)
	forwarding := rest[0] == 1
	a := c.owner
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed || a.ctx.Err() != nil {
		return nil, errNamedSSHRefused
	}
	for _, existing := range c.bindings {
		if bytes.Equal(existing.sessionID, sessionID) {
			if existing.fingerprint != fingerprint || existing.forwarding != forwarding {
				return nil, errNamedSSHRefused
			}
			return []byte{6}, nil
		}
	}
	if len(c.bindings) >= 16 || len(c.bindings) > 0 && !c.bindings[len(c.bindings)-1].forwarding {
		return nil, errNamedSSHRefused
	}
	c.bindings = append(c.bindings, namedSSHBinding{fingerprint: fingerprint, sessionID: bytes.Clone(sessionID), forwarding: forwarding})
	return []byte{6}, nil
}

func namedSSHString(input []byte) ([]byte, []byte, bool) {
	if len(input) < 4 {
		return nil, nil, false
	}
	size := binary.BigEndian.Uint32(input[:4])
	if uint64(size) > uint64(len(input)-4) {
		return nil, nil, false
	}
	end := 4 + int(size)
	return input[4:end], input[end:], true
}
