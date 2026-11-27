// Package sshserve translates the closed local stdio SSH surface. Its backend
// must retain the actual original client/source and owned remote operations;
// SSH request data never supplies a native permission or sandbox constructor.
package sshserve

import (
	"fmt"
	"net"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/iamlakshya/simstim/internal/proto"
	"golang.org/x/crypto/ssh"
)

// Terminal describes only bounded terminal data, never environment grants.
type Terminal struct {
	Term        string
	Columns     uint32
	Rows        uint32
	PixelWidth  uint32
	PixelHeight uint32
	Modes       string
}

type directTCP struct {
	Host       string
	Port       uint32
	Origin     string
	OriginPort uint32
}

func decodeTerminal(payload []byte) (Terminal, error) {
	var terminal Terminal
	if len(payload) > 8<<10 || ssh.Unmarshal(payload, &terminal) != nil || terminal.Term == "" || len(terminal.Term) > 128 || !utf8.ValidString(terminal.Term) || strings.ContainsAny(terminal.Term, "\x00\r\n\x1b") || terminal.Columns == 0 || terminal.Rows == 0 || terminal.Columns > 1000 || terminal.Rows > 1000 || len(terminal.Modes) > 4096 {
		return Terminal{}, fmt.Errorf("invalid SSH terminal declaration")
	}
	// RFC 4254 mode records are one opcode plus uint32, ending with zero.
	for offset := 0; ; {
		if offset >= len(terminal.Modes) {
			return Terminal{}, fmt.Errorf("SSH terminal modes have no terminator")
		}
		opcode := terminal.Modes[offset]
		offset++
		if opcode == 0 {
			if offset != len(terminal.Modes) {
				return Terminal{}, fmt.Errorf("SSH terminal modes have trailing data")
			}
			break
		}
		if opcode >= 160 || offset+4 > len(terminal.Modes) {
			return Terminal{}, fmt.Errorf("unsupported or truncated SSH terminal mode")
		}
		offset += 4
	}
	return terminal, nil
}

func decodeDirectTCP(payload []byte) (uint16, error) {
	var target directTCP
	if len(payload) > 4096 || ssh.Unmarshal(payload, &target) != nil || target.Port == 0 || target.Port > 65535 || len(target.Host) > 253 || len(target.Origin) > 253 || target.OriginPort > 65535 {
		return 0, fmt.Errorf("invalid SSH TCP channel declaration")
	}
	loopback := target.Host == "localhost"
	if address := net.ParseIP(target.Host); address != nil {
		loopback = address.IsLoopback()
	}
	if !loopback {
		return 0, fmt.Errorf("SSH TCP channels require the guest's loopback")
	}
	return uint16(target.Port), nil
}

func decodeSignal(payload []byte) (string, error) {
	var request struct{ Name string }
	if len(payload) > 128 || ssh.Unmarshal(payload, &request) != nil {
		return "", fmt.Errorf("invalid SSH signal request")
	}
	if _, ok := proto.EditorSignalNumberV1(request.Name); ok {
		return request.Name, nil
	}
	return "", fmt.Errorf("unsupported SSH signal")
}

type openRate struct {
	times [20]time.Time
	next  int
	count int
}

// Each accepted or rejected channel attempt occupies one rolling slot. There
// is no fixed-window boundary at which a client may double its open rate.
func (r *openRate) admit(at time.Time) bool {
	if r.count == len(r.times) && at.Sub(r.times[r.next]) < time.Second {
		return false
	}
	r.times[r.next] = at
	r.next = (r.next + 1) % len(r.times)
	if r.count < len(r.times) {
		r.count++
	}
	return true
}
