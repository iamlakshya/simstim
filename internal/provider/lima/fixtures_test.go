package lima

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// statusRe is Lima 2.3.0's events.Status as `limactl start` prints it with
// %+v: all nine fields, in order (pkg/hostagent/events/events.go:10-33).
var statusRe = regexp.MustCompile(`status=\{Running:(true|false) Degraded:(true|false) Exiting:(true|false) Errors:\[[^\]]*\] SSHLocalPort:\d+ CloudInitProgress:\S+ PortForward:\S+ Vsock:\S+ RequirementProgress:\S+\}`)

// The written testdata must keep to what Lima 2.3.0's source can print.
// When the host agent's driver.Start fails, it returns before it shuts
// anything down (pkg/hostagent/hostagent.go:462-465), so no "Shutting down"
// line follows the refusal.
func TestFixturesFollowLima(t *testing.T) {
	written, err := filepath.Glob(filepath.Join("testdata", "*"))
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, p := range written {
		name := filepath.Base(p)
		stderr := strings.HasPrefix(name, "start-") && strings.HasSuffix(name, ".stderr")
		haLog := strings.HasPrefix(name, "ha-") && strings.HasSuffix(name, ".log")
		if !stderr && !haLog && name != "loopback.ndjson" {
			continue
		}
		seen++
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
		if !strings.HasPrefix(lines[0], "# written from Lima 2.3.0's source (") || !strings.Contains(lines[0], "), not recorded.") {
			t.Errorf("%s: its first line does not say where it was written from: %q", name, lines[0])
		}
		statuses := 0
		for i, line := range lines[1:] {
			if strings.HasPrefix(line, "#") {
				continue
			}
			if strings.Contains(line, "Shutting down") {
				t.Errorf("%s:%d: Lima prints no shutdown line after a failed start: %q", name, i+2, line)
			}
			if strings.Contains(line, "status={") {
				statuses++
				if !statusRe.MatchString(line) {
					t.Errorf("%s:%d: the status lacks some of its nine fields: %q", name, i+2, line)
				}
			}
			if haLog {
				var j struct{ Level, Msg, Time string }
				if err := json.Unmarshal([]byte(line), &j); err != nil || j.Level == "" || j.Msg == "" || j.Time == "" {
					t.Errorf("%s:%d: not a line of the host agent's JSON log: %q", name, i+2, line)
				}
			}
		}
		if stderr && statuses != 1 {
			t.Errorf("%s: %d exiting lines, want 1", name, statuses)
		}
	}
	if seen != 6 {
		t.Errorf("checked %d written files, want 6", seen)
	}
	// On the usual path, the refusal reaches only ha.stderr.log.
	if b := readData(t, "start-exiting.stderr"); refusal(string(b)) {
		t.Error("start-exiting.stderr holds a refusal")
	}
}
