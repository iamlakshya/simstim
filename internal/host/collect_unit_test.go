package host

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/alerts"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/paths"
)

// The unit's state names only the properties that a bundle shows, and adds
// its cgroup's memory.events.
func TestTheUnitStateForABundle(t *testing.T) {
	root := t.TempDir()
	old := cgroupRoot
	cgroupRoot = root
	t.Cleanup(func() { cgroupRoot = old })
	cg := "/user.slice/x/app.slice/s.service"
	if err := os.MkdirAll(filepath.Join(root, cg), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, cg, "memory.events"), []byte("max 3\noom_kill 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	calls := fakeSystemctl(t, "ActiveState=failed\nResult=oom-kill\nControlGroup="+cg+"\n", nil)
	st, err := unitState("s.service")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(st, []byte("Result=oom-kill")) || !bytes.Contains(st, []byte("oom_kill 1")) {
		t.Fatalf("unit state = %s", st)
	}
	args := strings.Join((*calls)[0], " ")
	if !strings.HasPrefix(args, "show s.service -p ") || strings.Contains(args, "Environment") || strings.Contains(args, "ExecStart=") {
		t.Fatalf("systemctl %s", args)
	}
}

// The kernel's report keeps the lines that name the unit, and the killed
// process after an oom-kill line.
func TestTheKernelReportNamesTheUnit(t *testing.T) {
	old := journal
	t.Cleanup(func() { journal = old })
	journal = func(context.Context, time.Time) ([]byte, error) {
		return []byte("other line\n" +
			"kernel: node invoked oom-killer: gfp_mask=0xcc0\n" +
			"kernel: oom-kill:constraint=CONSTRAINT_MEMCG,oom_memcg=/user.slice/s.service,task=node,pid=7\n" +
			"kernel: Memory cgroup out of memory: Killed process 7 (node) total-vm:1kB\n" +
			"kernel: Killed process 9 (other) in another unit\n"), nil
	}
	rep, err := kernelReport("s.service", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	text := string(rep)
	if !strings.Contains(text, "oom_memcg=/user.slice/s.service") || !strings.Contains(text, "Killed process 7 (node)") ||
		strings.Contains(text, "other line") || strings.Contains(text, "Killed process 9") {
		t.Fatalf("report = %s", text)
	}
}

// The alerts file's values are replaced, and a binary file that holds one
// is found, gunzipped first when it is a profile.
func TestTheAlertsValuesStayOnTheHost(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	tokenFile := filepath.Join(home, "ntfy-token")
	if err := os.WriteFile(tokenFile, []byte("tk_from_file_1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The fake key is built in parts, so a secret scanner does not take it for a key.
	run := "curl -s -H 'Authorization: " + "Bearer " + "sk_" + "live_abcdefghijkl' https://hooks.example.com/notify"
	cmd := api.AlertDestination{Type: "command", Run: run}
	if err := alerts.Save(paths.AlertsConfig(), api.AlertsConfig{Destinations: []api.AlertDestination{
		{Type: "ntfy", URL: "https://ntfy.example.com/secret-topic?a=1&b=2", TokenFile: tokenFile},
		{Type: "webhook", URL: "https://hooks.example.com/x", Headers: map[string]string{"X-Key": "hdr-value-9", "X-One": "1"}},
		cmd,
	}}); err != nil {
		t.Fatal(err)
	}
	secrets := alertSecrets()
	in := `url=https://ntfy.example.com/secret-topic?a=1&b=2 json="https://ntfy.example.com/secret-topic?a=1\u0026b=2" token=tk_from_file_1 key=hdr-value-9 one=1` + "\n" +
		`level=WARN msg="alert not delivered" target="` + alerts.Target(cmd) + `" error="HTTP 401: {\"error\":\"bad token zz-77\"}" next=1` + "\n" +
		"run=" + run + "\n"
	out, n := replaceSecrets([]byte(in), secrets)
	want := `url=https://ntfy.example.com/… json="https://ntfy.example.com/…" token=(hidden) key=(hidden) one=1` + "\n" +
		`level=WARN msg="alert not delivered" target="(hidden)" error="HTTP 401: (hidden)" next=1` + "\n" +
		"run=(hidden)\n"
	if string(out) != want || n != 7 {
		t.Fatalf("replaced %d:\n got %s\nwant %s", n, out, want)
	}
	var gz bytes.Buffer
	z := gzip.NewWriter(&gz)
	_, _ = z.Write([]byte("\x00hdr-value-9\x00"))
	_ = z.Close()
	prof := filepath.Join(home, "heap.pprof")
	if err := os.WriteFile(prof, gz.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if !holdsSecret(prof, secrets) || holdsSecret(tokenFile+"-none", secrets) {
		t.Fatal("holdsSecret missed the profile's value, or found one in a missing file")
	}
}

// A holder's line goes to holder.log through slog's text handler, and to
// debug.jsonl as that text inside JSON. A command with a quote in it is
// escaped once in the first, and twice in the second. Both lose the token.
func TestAlertValuesEscapedTwiceStayOnTheHost(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	const tok = "sk_live_PLUM42"
	dsts := []api.AlertDestination{
		{Type: "command", Run: `curl -s -H "Authorization: Bearer ` + tok + `" https://hooks.example.com/x`},
		{Type: "command", Run: `notify --token=` + tok + ` --to "ops & dev <on call>"`},
		{Type: "webhook", URL: "https://hooks.example.com/services/T0/B0/" + tok},
	}
	if err := alerts.Save(paths.AlertsConfig(), api.AlertsConfig{Destinations: dsts}); err != nil {
		t.Fatal(err)
	}
	secrets := alertSecrets()
	for _, d := range dsts {
		var text bytes.Buffer
		slog.New(slog.NewTextHandler(&text, nil)).Warn("alert not delivered", "type", d.Type, "target", alerts.Target(d), "run", d.Run, "url", d.URL)
		rec, err := json.Marshal(map[string]string{"text": strings.TrimSpace(text.String())})
		if err != nil {
			t.Fatal(err)
		}
		for name, in := range map[string][]byte{"holder.log": text.Bytes(), "debug.jsonl": rec} {
			out, n := replaceSecrets(bytes.Clone(in), secrets)
			if bytes.Contains(out, []byte("PLUM42")) || n == 0 {
				t.Errorf("%s, %s: %d replaced:\n%s", d.Type, name, n, out)
			}
		}
	}
}
