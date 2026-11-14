//go:build darwin

package host

import (
	"encoding/binary"
	"strings"
	"testing"
)

func TestDarwinProcessArgvStopsBeforeEnvironment(t *testing.T) {
	raw := make([]byte, 4)
	binary.NativeEndian.PutUint32(raw, 3)
	raw = append(raw, []byte("/bin/sleep\x00\x00\x00/bin/sleep\x00\x00"+"30\x00ENV_CANARY=must-not-return\x00")...)
	argv, err := darwinProcessArgv(raw)
	if err != nil || len(argv) != 3 || argv[0] != "/bin/sleep" || argv[1] != "" || argv[2] != "30" {
		t.Fatalf("parsed argv=%q, err=%v", argv, err)
	}
	if strings.Contains(strings.Join(argv, " "), "ENV_CANARY") {
		t.Fatal("the argv parser returned environment data")
	}
}

func TestDarwinProcessArgvRejectsIncompleteData(t *testing.T) {
	for _, kind := range []string{"short", "negative argc", "excess argc", "exec path", "argument"} {
		t.Run(kind, func(t *testing.T) {
			raw := make([]byte, 4)
			binary.NativeEndian.PutUint32(raw, 1)
			switch kind {
			case "short":
				raw = raw[:3]
			case "negative argc":
				binary.NativeEndian.PutUint32(raw, ^uint32(0))
			case "excess argc":
				binary.NativeEndian.PutUint32(raw, 65537)
			case "exec path":
				raw = append(raw, []byte("no terminator")...)
			case "argument":
				raw = append(raw, []byte("/bin/sleep\x00\x00unterminated")...)
			}
			if argv, err := darwinProcessArgv(raw); err == nil || argv != nil {
				t.Fatalf("invalid data returned arguments: %q, %v", argv, err)
			}
		})
	}
}
