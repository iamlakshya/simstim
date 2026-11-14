package tui

import (
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

func setExposedDisplaySummary(session *client.Session, armed bool) {
	session.SecretSummary = &api.SessionSecretSummary{Version: api.SessionSecretSummaryV1, Generation: strings.Repeat("a", 32), Secrets: map[string]api.SecretStatus{}}
	for _, name := range session.Secrets {
		session.SecretSummary.Secrets[name] = api.SecretStatus{Mode: api.SecretModeExposed, Armed: armed}
	}
}

func TestWallMark(t *testing.T) {
	for _, tt := range []struct {
		name string
		wall *api.WallInfo
		want string
	}{
		{"unwalled", nil, " "},
		{"none", &api.WallInfo{Level: "none"}, " "},
		{"process", &api.WallInfo{Level: "process"}, "□"},
		{"gvisor", &api.WallInfo{Level: "gvisor"}, "▤"},
		{"microvm", &api.WallInfo{Level: "microvm"}, "▣"},
		{"downgrade", &api.WallInfo{Level: "process", DowngradedFrom: "microvm"}, "!"},
		{"unknown", &api.WallInfo{Level: "\x1b[31m"}, " "},
	} {
		t.Run(tt.name, func(t *testing.T) {
			mark := wallMark(client.Session{Wall: tt.wall})
			if mark.s != tt.want || cells(mark.s) != 1 {
				t.Fatalf("mark %q (%d cells), want %q in one cell", mark.s, cells(mark.s), tt.want)
			}
		})
	}
}

func TestWallWarningsKeepEverySecretName(t *testing.T) {
	s := client.Session{Wall: &api.WallInfo{Level: "process"}, Secrets: []string{"Z_TOKEN", "A_TOKEN"}, Armed: true}
	setExposedDisplaySummary(&s, s.Armed)
	if got, want := sessionWallWarnings(s), []string{"exposed: A_TOKEN", "exposed: Z_TOKEN"}; !slices.Equal(got, want) {
		t.Fatalf("warnings %q, want %q", got, want)
	}
	if !slices.Equal(s.Secrets, []string{"Z_TOKEN", "A_TOKEN"}) {
		t.Fatal("drawing changed the secret names kept for requests")
	}
	s.Wall = nil
	if got := sessionWallWarnings(s); len(got) != 0 {
		t.Fatalf("unwalled session has wall warnings %q", got)
	}
}

func TestWallWarningsCleanOnlyTheirDrawnCopy(t *testing.T) {
	s := client.Session{Wall: &api.WallInfo{Level: "process", DowngradedFrom: "\x1bprocess"}, Secrets: []string{"KEY\x1b\x07"}}
	setExposedDisplaySummary(&s, s.Armed)
	got := strings.Join(sessionWallWarnings(s), "\n")
	if strings.ContainsAny(got, "\x1b\x07") || !strings.Contains(got, "below the floor unknown") || !strings.Contains(got, "exposed: KEY") {
		t.Fatalf("unclean warning %q", got)
	}
	if s.Secrets[0] != "KEY\x1b\x07" || s.Wall.DowngradedFrom != "\x1bprocess" {
		t.Fatal("drawing changed raw session data")
	}
}

func TestWallDetailKeepsExposureAtNarrowWidths(t *testing.T) {
	s := client.Session{Wall: &api.WallInfo{Level: "process"}, Secrets: []string{"TOKEN"}, Armed: true}
	setExposedDisplaySummary(&s, s.Armed)
	for _, width := range []int{20, 40, 80} {
		got := ansi.Strip(wallDetail(s, []string{"~/src/app", "", "s1"}, width))
		if !strings.Contains(got, "exposed: TOKEN") || cells(got) > width {
			t.Fatalf("width %d: %q", width, got)
		}
	}
}

func TestHostWallFactsDoNotInferRuntimeAvailability(t *testing.T) {
	h := &hostConn{host: client.Host{WallFloor: "gvisor"}}
	if got := hostWallFacts(h); got != "floor gvisor"+dot+"now unknown" {
		t.Fatalf("no report: %q", got)
	}
	h.info = &client.HostInfo{Sandbox: &api.CapProbe{
		OS: "linux", Arch: "amd64", Kernel: "6.12.0", UserNS: true, LandlockABI: 6, Seccomp: true,
		Walls: []api.WallProbe{{Driver: "process", Level: "process", OK: true, Facts: map[string]string{"runtime_available": "false"}}},
	}}
	if got := hostWallNow(h); got != "none" {
		t.Fatalf("missing runtime reported as %q", got)
	}
	h.info.Sandbox.Walls[0].Facts = nil
	if got := hostWallNow(h); got != "process" {
		t.Fatalf("runnable report gives %q", got)
	}
	h.info.Sandbox.Walls = []api.WallProbe{{Driver: "gvisor", Level: "gvisor", OK: true}}
	if got := hostWallNow(h); got != "none" {
		t.Fatalf("driver OK without verified runtime reported as %q", got)
	}
}

func TestWallPresentationGolden(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m := &Model{now: func() time.Time { return at }, traces: map[sessKey][]time.Time{}}
	h := &hostConn{host: client.Host{Name: "box", WallFloor: "process"}, loaded: true, status: stOnline}
	plain := func(s string) string { return strings.Join(strings.Fields(ansi.Strip(s)), " ") }
	var lines []string
	for _, level := range []string{"none", "process", "gvisor", "microvm"} {
		s := client.Session{ID: "s1", Name: "task", Agent: "shell", Cwd: "~", State: api.StateIdle, Since: at, Wall: &api.WallInfo{Level: level}, Armed: true}
		lines = append(lines, plain(m.sessRowView(sessRow{h: h, s: s}, 1, sessCols(100), false, false)))
	}
	s := client.Session{Wall: &api.WallInfo{Level: "none", DowngradedFrom: "process"}, Secrets: []string{"GITHUB_TOKEN", "OPENAI_API_KEY"}}
	setExposedDisplaySummary(&s, s.Armed)
	lines = append(lines, plain(wallDetail(s, nil, 160)))
	h.info = &client.HostInfo{Sandbox: &api.CapProbe{
		OS: "linux", Arch: "amd64", Kernel: "6.12.0", UserNS: true, LandlockABI: 6, Seccomp: true,
		Walls: []api.WallProbe{{Driver: "process", Level: "process", OK: true}},
	}}
	lines = append(lines, plain(m.hostRow(h, 1, false, 100, 8, 6)))
	h.info = nil
	lines = append(lines, hostWallFacts(h))
	got := strings.Join(lines, "\n") + "\n"
	want, err := os.ReadFile("testdata/wall_presentation.golden")
	if err != nil {
		t.Fatalf("read wall golden: %v", err)
	}
	if got != string(want) {
		t.Fatalf("wall presentation:\n%s\nwant:\n%s", got, want)
	}
}

func TestWallWarningsDoNotCallBoundOrUnknownSecretsExposed(t *testing.T) {
	session := client.Session{Wall: &api.WallInfo{Level: "gvisor"}, Secrets: []string{"BOUND"}}
	session.SecretSummary = &api.SessionSecretSummary{Version: api.SessionSecretSummaryV1, Generation: strings.Repeat("a", 32), Secrets: map[string]api.SecretStatus{"BOUND": {Mode: api.SecretModeBound, Armed: true}}}
	if got := sessionWallWarnings(session); len(got) != 0 {
		t.Fatalf("a bound credential was labelled exposed/unarmed: %q", got)
	}
	session.SecretSummary = nil
	if got := sessionWallWarnings(session); !slices.Equal(got, []string{"secret delivery unknown: BOUND"}) {
		t.Fatalf("absent delivery was inferred: %q", got)
	}
}
