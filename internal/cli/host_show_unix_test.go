//go:build unix

package cli

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
)

// host show against the test host: the saved part and the facts. A host
// that does not answer is data: the saved part, then why, and exit code 0.
// It never starts a stopped machine.
func TestHostShow(t *testing.T) {
	th := newTestHost(t)
	out := ok(t, "host", "show", "box")
	for _, want := range []string{"box\n", "  Tier       own\n", "  Machine    hosttest ", "  System     ", "  Helper     " + brand.HostBinary + " ", "  Agents     ", "  Limits     "} {
		if !strings.Contains(out, want) {
			t.Errorf("host show lacks %q:\n%s", want, out)
		}
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(okJSON(t, "host", "show", "box", "--json")), &obj); err != nil || obj["reachable"] != true || obj["info"] == nil ||
		obj["name"] != "box" || obj["error"] != nil {
		t.Errorf("host show --json = %v, %v", obj, err)
	}

	th.tr.SetDown(errors.New("connection refused"))
	out, errOut, err := runApart(t, "host", "show", "box")
	if err != nil || !strings.Contains(out, "box did not answer: ") || !strings.Contains(out, "connection refused") || strings.Contains(out, "System") {
		t.Errorf("host show of a host that does not answer: %v\nstdout %q\nstderr %q", err, out, errOut)
	}
	out = okJSON(t, "host", "show", "box", "--json")
	obj = nil
	if err := json.Unmarshal([]byte(out), &obj); err != nil || obj["reachable"] != false || obj["error"] == nil || obj["info"] != nil {
		t.Errorf("host show --json of a host that does not answer = %s", out)
	}
	checkJSON(t, "host show", out, false)
	fails(t, `no host named "nowhere"`, "host", "show", "nowhere")
}
