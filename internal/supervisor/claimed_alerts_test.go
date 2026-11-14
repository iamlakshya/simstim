package supervisor

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/alerts"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

// A claim of the guest stays a claim in every destination: the summary
// says "(claimed)", the JSON of a webhook and of a command's input says
// claimed, with who wrote the detail and the exit details, and so does a
// command's environment. The trusted side's own alerts say nothing of the
// kind.
func TestAForgedAlertSaysItIsClaimed(t *testing.T) {
	h := hostileStart(t)
	dir := t.TempDir()
	hooks := make(chan alerts.Event, 16)
	hook := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		var ev alerts.Event
		b, _ := io.ReadAll(r.Body)
		if json.Unmarshal(b, &ev) == nil {
			hooks <- ev
		}
	}))
	t.Cleanup(hook.Close)
	texts := make(chan string, 16)
	ntfy := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		texts <- string(b)
	}))
	t.Cleanup(ntfy.Close)
	event := "$" + brand.Env("EVENT")
	run := "cat > " + dir + "/in-" + event + ".json; env > " + dir + "/env-" + event + ".txt"
	cfg := "events = [\"needs_input\", \"failed\", \"started\"]\ndelay = \"0s\"\ndetail = true\n" +
		"[[destination]]\ntype = \"webhook\"\nurl = \"" + hook.URL + "\"\n" +
		"[[destination]]\ntype = \"ntfy\"\nurl = \"" + ntfy.URL + "/topic\"\n" +
		"[[destination]]\ntype = \"command\"\nrun = '" + run + "'\n"
	if err := os.MkdirAll(filepath.Dir(paths.AlertsConfig()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.AlertsConfig(), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	h.answerStart(t)
	h.send(t, proto.CtlEvent{Event: proto.EventAlert, Session: h.id, Kind: alerts.NeedsInput, Detail: "Proceed?"})
	code := 3
	h.send(t, proto.CtlEvent{Event: proto.EventEnded, Session: h.id, ExitCode: &code})

	got := map[string]alerts.Event{}
	for len(got) < 3 {
		select {
		case ev := <-hooks:
			got[ev.Event] = ev
		case <-time.After(10 * time.Second):
			t.Fatalf("the webhook got %v", got)
		}
	}
	if ev := got[alerts.NeedsInput]; !ev.Claimed || ev.DetailFrom != alerts.FromGuest || !strings.HasSuffix(ev.Summary, "(claimed)") {
		t.Errorf("needs_input on the webhook = %+v", ev)
	}
	if ev := got[alerts.Failed]; !ev.Claimed || ev.ExitFrom != alerts.FromGuest || ev.ExitCode == nil || *ev.ExitCode != 3 {
		t.Errorf("failed on the webhook = %+v", ev)
	}
	if ev := got[alerts.Started]; ev.Claimed || ev.ExitFrom != "" || strings.Contains(ev.Summary, "claimed") {
		t.Errorf("the trusted side's started says claimed: %+v", ev)
	}
	for range 3 {
		select {
		case text := <-texts:
			if !strings.Contains(text, "started on") && !strings.Contains(text, "(claimed)") {
				t.Errorf("an ntfy text without the mark: %q", text)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("ntfy got too few alerts")
		}
	}
	for _, kind := range []string{alerts.NeedsInput, alerts.Failed} {
		var env, in []byte
		waitFor(t, "the command's files for "+kind, func() bool {
			env, _ = os.ReadFile(filepath.Join(dir, "env-"+kind+".txt"))
			in, _ = os.ReadFile(filepath.Join(dir, "in-"+kind+".json"))
			return len(env) > 0 && len(in) > 0
		})
		if !strings.Contains(string(env), brand.Env("CLAIMED")+"=1") || !strings.Contains(string(in), `"claimed":true`) {
			t.Errorf("the command's %s does not say claimed:\n%s\n%s", kind, env, in)
		}
	}
}
