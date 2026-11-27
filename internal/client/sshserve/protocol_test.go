package sshserve

import (
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestDirectTCPRequiresOnlyGuestLoopbackAndNumericPort(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "::1", "localhost"} {
		port, err := decodeDirectTCP(ssh.Marshal(directTCP{Host: host, Port: 8080, Origin: "127.0.0.1", OriginPort: 1}))
		if err != nil || port != 8080 {
			t.Fatal("guest loopback declaration was rejected", host, err)
		}
	}
	for _, target := range []directTCP{{Host: "192.0.2.1", Port: 8080}, {Host: "service.example", Port: 8080}, {Host: "/run/service.sock", Port: 8080}, {Host: "127.0.0.1", Port: 0}, {Host: "::1", Port: 65536}} {
		if _, err := decodeDirectTCP(ssh.Marshal(target)); err == nil {
			t.Fatal("a non-guest destination or unsupported port reached admission", target.Host)
		}
	}
}

func TestRollingSSHOpenLimitHasNoDoubleWindowBoundary(t *testing.T) {
	var rate openRate
	start := time.Now()
	for index := range 20 {
		if !rate.admit(start.Add(time.Duration(index) * time.Millisecond)) {
			t.Fatal("the specified first twenty opens did not fit")
		}
	}
	if rate.admit(start.Add(999 * time.Millisecond)) {
		t.Fatal("an extra channel crossed a fixed-window boundary")
	}
	if !rate.admit(start.Add(time.Second)) {
		t.Fatal("the expired original slot was not released")
	}
	if rate.admit(start.Add(time.Second)) {
		t.Fatal("one expired slot became another twenty-open burst")
	}
}

func TestSSHPTYRejectsUnboundedAndTruncatedControlData(t *testing.T) {
	valid := Terminal{Term: "xterm-256color", Columns: 80, Rows: 24, Modes: "\x00"}
	if _, err := decodeTerminal(ssh.Marshal(valid)); err != nil {
		t.Fatal(err)
	}
	for _, variant := range []string{"columns", "rows", "term", "mode-argument", "mode-tail", "missing-end"} {
		t.Run(variant, func(t *testing.T) {
			changed := valid
			switch variant {
			case "columns":
				changed.Columns = ^uint32(0)
			case "rows":
				changed.Rows = 0
			case "term":
				changed.Term = "xterm\nTERM=other"
			case "mode-argument":
				changed.Modes = "\x01\x00"
			case "mode-tail":
				changed.Modes = "\x00ignored"
			case "missing-end":
				changed.Modes = "\x01\x00\x00\x00\x00"
			}
			if _, err := decodeTerminal(ssh.Marshal(changed)); err == nil {
				t.Fatal("invalid terminal control reached the guest")
			}
		})
	}
}

func TestSSHSignalsRemainAClosedOwnedProcessOperation(t *testing.T) {
	for _, name := range []string{"INT", "TERM", "HUP"} {
		if got, err := decodeSignal(ssh.Marshal(struct{ Name string }{name})); err != nil || got != name {
			t.Fatal("supported SSH signal was rejected", err)
		}
	}
	for _, name := range []string{"SIGTERM", "9", "TERM; command", "", "unknown"} {
		if _, err := decodeSignal(ssh.Marshal(struct{ Name string }{name})); err == nil {
			t.Fatal("an arbitrary signal declaration reached an owned process")
		}
	}
}
