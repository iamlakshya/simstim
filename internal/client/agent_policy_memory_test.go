//go:build darwin || linux

package client

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func writeAgentMemoryMaximumPolicyFixture(t *testing.T, host Host, id, hostID string, opaqueBytes, otherRows int) string {
	return writeAgentMemoryMaximumPolicyFixtureAt(t, host, id, hostID, opaqueBytes, otherRows, "selected")
}

func writeAgentMemoryMaximumPolicyFixtureAt(t *testing.T, host Host, id, hostID string, opaqueBytes, otherRows int, placement string) string {
	t.Helper()
	path := filepath.Join(StateDir(), "hosts", HostFileKey(host.Name), "sandboxes.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	write := func(text string) {
		t.Helper()
		if _, err := io.WriteString(file, text); err != nil {
			t.Fatal(err)
		}
	}
	target := fmt.Sprintf(`%q:{"session_id":%q,"host_id":%q,"ceiling":"own","ssh_agent":true,"created_at":"2026-11-14T12:00:00Z","request":"fixture","wall_intent":{"level":"none"}`, id, id, hostID)
	suffix := `"}}`
	switch placement {
	case "selected":
		write(fmt.Sprintf(`{"schema":2,"host":%q,"sessions":{`, host.Name) + target + `,"policy_sources":{"future":"`)
	case "other":
		write(fmt.Sprintf(`{"schema":2,"host":%q,"sessions":{"aabaaaaa":{"session_id":"aabaaaaa","ceiling":"shared","created_at":"2026-11-14T12:00:00Z","request":"other","wall_intent":{"level":"process"},"future":"`, host.Name))
		suffix = `"},` + target + `}`
	case "root":
		write(fmt.Sprintf(`{"schema":2,"host":%q,"future":"`, host.Name))
		suffix = `","sessions":{` + target + `}`
	default:
		t.Fatal("unknown maximum metadata fixture placement")
	}
	var chunk [32 << 10]byte
	for i := range chunk {
		chunk[i] = 'x'
	}
	for remaining := opaqueBytes; remaining > 0; {
		n := min(remaining, len(chunk))
		if _, err := file.Write(chunk[:n]); err != nil {
			t.Fatal(err)
		}
		remaining -= n
	}
	write(suffix)
	for i := range otherRows {
		// Use the alphabetic subset of canonical base32 IDs. Base26 also
		// keeps this writer-shaped fixture in observed lexical order.
		value := i
		name := []byte("otheraaaa")
		for position := len(name) - 1; position >= 5; position-- {
			name[position] = "abcdefghijklmnopqrstuvwxyz"[value%26]
			value /= 26
		}
		otherID := string(name)
		write(fmt.Sprintf(`,%q:{"session_id":%q,"host_id":%q,"ceiling":"shared","created_at":"2026-11-14T12:00:00Z","request":"other","wall_intent":{"level":"process"},"future":{"duplicate":1,"duplicate":2}}`, otherID, otherID, hostID))
	}
	write(`}}`)
	if err := file.Sync(); err != nil {
		t.Fatal(err)
	}
	return path
}
