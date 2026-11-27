package client

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeAgentPolicyMaximumFixture is shared test source for the actual actor
// memory owner. It writes the body with a fixed 32 KiB buffer, before the
// actor's first read. It never changes the metric or resets a baseline.
func writeAgentPolicyMaximumFixture(t *testing.T, host Host, id, hostID string, opaqueBytes, otherRows int) string {
	return writeAgentPolicyMaximumFixtureAt(t, host, id, hostID, opaqueBytes, otherRows, "selected")
}

func writeAgentPolicyMaximumFixtureAt(t *testing.T, host Host, id, hostID string, opaqueBytes, otherRows int, placement string) string {
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

func TestAgentPolicyMaximumMetadataRepeatedReads(t *testing.T) {
	if os.Getenv("H5_POLICY_READ_MAX") != "1" {
		t.Skip("set H5_POLICY_READ_MAX=1 in the admitted maximum-metadata proof job")
	}
	for _, placement := range []string{"selected", "other", "root"} {
		t.Run(placement, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			host := Host{Name: "box"}
			id := "abcd2345"
			hostID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			writeAgentPolicyMaximumFixtureAt(t, host, id, hostID, 4<<20, 128, placement)
			for range 32 {
				// Keep the actual active-agent policy check timeout. This is
				// a read proof, not a claim about the full actor budget.
				ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
				policy, err := readAgentSandboxPolicy(ctx, host, id)
				cancel()
				if err != nil || policy == nil || policy.HostID != hostID || policy.Ceiling != TierOwn || !policy.SSHAgent || policy.CreationWall != "none" {
					t.Fatalf("maximum metadata changed agent facts: %+v, %v", policy, err)
				}
			}
		})
	}
}
