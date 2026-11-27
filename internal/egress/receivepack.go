package egress

import (
	"bytes"
	"errors"
	"io"
	"strings"
)

// These wire limits and states follow the public gitprotocol-common and
// gitprotocol-pack contracts. This is an authored parser, not Git source.
// https://www.kernel.org/pub/software/scm/git/docs/gitprotocol-common.html
// https://www.kernel.org/pub/software/scm/git/docs/gitprotocol-pack.html
const maxGitPacket = 65520

var errReceivePack = errors.New("invalid or forbidden Git receive-pack command. Try: push only to the session's branch prefix")

type receivePackCheck struct {
	prefix              string
	commands            int
	width               int
	shallowWidth        int
	signed              bool
	certificateField    int
	certificateCommands bool
	signature           bool
	signatureEnd        bool
	certificateEnd      bool
	pushOptions         bool
}

// readReceivePackPrefix reads only the command section. No pack byte is
// interpreted, copied wholesale or released before every update is checked.
// The same existing 64 MiB inspection budget bounds this retained prefix.
func readReceivePackPrefix(input io.Reader, allowedPrefix string) (wire []byte, returned error) {
	defer func() {
		if returned != nil {
			clear(wire)
			wire = nil
		}
	}()
	if !strings.HasPrefix(allowedPrefix, "refs/heads/") || !strings.HasSuffix(allowedPrefix, "/") || len(strings.Split(strings.TrimSuffix(allowedPrefix, "/"), "/")) < 4 || !sessionGitRef([]byte(allowedPrefix+"check"), allowedPrefix) {
		return nil, errReceivePack
	}
	if input == nil {
		return nil, errReceivePack
	}
	check := receivePackCheck{prefix: allowedPrefix, width: 40}
	for {
		var header [4]byte
		if _, err := io.ReadFull(input, header[:]); err != nil {
			return wire, errReceivePack
		}
		if len(wire) > maxDecodedHTTPBody-len(header) {
			return wire, errReceivePack
		}
		wire = append(wire, header[:]...)
		// The signed grammar can end directly before PACK. Retain these
		// four bytes for the same replay owner, rather than consuming data.
		if check.certificateEnd && bytes.Equal(header[:], []byte("PACK")) {
			return wire, nil
		}
		length, ok := gitPacketLength(header)
		if !ok || length > maxGitPacket || length != 0 && length <= 4 {
			return wire, errReceivePack
		}
		if length == 0 {
			if check.commands == 0 || check.signed && !check.certificateEnd {
				return wire, errReceivePack
			}
			return wire, nil
		}
		payload := length - 4
		if len(wire) > maxDecodedHTTPBody-payload {
			return wire, errReceivePack
		}
		start := len(wire)
		wire = append(wire, make([]byte, payload)...)
		if _, err := io.ReadFull(input, wire[start:]); err != nil {
			return wire, errReceivePack
		}
		if (check.signed || bytes.HasPrefix(wire[start:], []byte("push-cert\x00"))) && !bytes.HasSuffix(wire[start:], []byte{'\n'}) {
			return wire, errReceivePack
		}
		line := bytes.TrimSuffix(wire[start:], []byte{'\n'})
		if err := check.line(line); err != nil {
			return wire, err
		}
	}
}

func gitPacketLength(header [4]byte) (int, bool) {
	length := 0
	for _, value := range header {
		var digit byte
		switch {
		case value >= '0' && value <= '9':
			digit = value - '0'
		case value >= 'a' && value <= 'f':
			digit = value - 'a' + 10
		default:
			return 0, false
		}
		length = length*16 + int(digit)
	}
	return length, true
}

func (check *receivePackCheck) line(line []byte) error {
	if check.certificateEnd {
		if !check.pushOptions || bytes.IndexByte(line, 0) >= 0 {
			return errReceivePack
		}
		return nil // Negotiated push options cannot add reference commands.
	}
	if check.signed {
		return check.certificate(line)
	}
	if check.commands == 0 && bytes.HasPrefix(line, []byte("shallow ")) {
		width := len(line) - len("shallow ")
		if !gitObjectID(line[len("shallow "):], width) || check.shallowWidth != 0 && check.shallowWidth != width {
			return errReceivePack
		}
		check.shallowWidth = width
		return nil
	}
	if check.commands == 0 {
		command, capabilities, found := bytes.Cut(line, []byte{0})
		if !found || check.capabilities(capabilities) != nil {
			return errReceivePack
		}
		if bytes.Equal(command, []byte("push-cert")) {
			check.signed = true
			return nil
		}
		line = command
	} else if bytes.IndexByte(line, 0) >= 0 {
		return errReceivePack
	}
	return check.command(line)
}

func (check *receivePackCheck) capabilities(raw []byte) error {
	for _, value := range raw {
		if value < 0x20 || value > 0x7e {
			return errReceivePack
		}
	}
	seen := make(map[string]bool)
	for _, capability := range bytes.Fields(raw) {
		for _, value := range capability {
			if value < 0x21 || value > 0x7e {
				return errReceivePack
			}
		}
		key, value, _ := bytes.Cut(capability, []byte{'='})
		name := string(key)
		if seen[name] {
			return errReceivePack
		}
		seen[name] = true
		switch name {
		case "object-format":
			switch string(value) {
			case "sha1":
				check.width = 40
			case "sha256":
				check.width = 64
			default:
				return errReceivePack
			}
		case "push-options":
			check.pushOptions = true
		}
	}
	if check.shallowWidth != 0 && check.shallowWidth != check.width {
		return errReceivePack
	}
	return nil
}

func (check *receivePackCheck) command(line []byte) error {
	parts := bytes.Split(line, []byte{' '})
	if len(parts) != 3 || !gitObjectID(parts[0], check.width) || !gitObjectID(parts[1], check.width) || allGitZero(parts[1]) || !sessionGitRef(parts[2], check.prefix) {
		return errReceivePack
	}
	check.commands++
	return nil
}

func gitObjectID(raw []byte, width int) bool {
	if width != 40 && width != 64 || len(raw) != width {
		return false
	}
	for _, value := range raw {
		if !(value >= '0' && value <= '9' || value >= 'a' && value <= 'f') {
			return false
		}
	}
	return true
}

func allGitZero(raw []byte) bool {
	for _, value := range raw {
		if value != '0' {
			return false
		}
	}
	return true
}

func sessionGitRef(raw []byte, prefix string) bool {
	if !bytes.HasPrefix(raw, []byte(prefix)) || len(raw) <= len(prefix) || raw[len(raw)-1] == '/' || raw[len(raw)-1] == '.' || bytes.Contains(raw, []byte("..")) || bytes.Contains(raw, []byte("@{")) {
		return false
	}
	for _, value := range raw {
		if value <= 0x20 || value == 0x7f || strings.ContainsRune(`~^:?*[\`, rune(value)) {
			return false
		}
	}
	for _, part := range bytes.Split(raw, []byte{'/'}) {
		if len(part) == 0 || part[0] == '.' || bytes.HasSuffix(part, []byte(".lock")) {
			return false
		}
	}
	return true
}
