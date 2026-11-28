package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/iamlakshya/simstim/internal/client"
)

// The launcher's host details sheet renders from client.BuildHostDetails,
// matching the fields that `host show` prints on the same fixture (cli-spec 11.2, rule 5).
func TestDetailsSheetUsesSharedHostDetails(t *testing.T) {
	h := client.Host{
		Name:      "box",
		SSH:       "dev@box.example",
		SSHConfig: "/tmp/extra.ssh",
		Trusted:   true,
	}
	info := client.HostInfo{
		Distro:        "Debian GNU/Linux 13",
		OS:            "linux",
		Arch:          "amd64",
		CPUs:          4,
		MemTotal:      8 << 30,
		DiskFree:      20 << 30,
		MemoryMax:     6 << 30,
		Version:       "0.2.0-deadbeef",
		Agents:        map[string]string{"claude": "/usr/local/bin/claude", "shell": "/bin/sh"},
		AgentVersions: map[string]string{"claude": "2.1.3"},
	}
	sessions := []client.Session{{ID: "abcd2345", Name: "one"}, {ID: "bcde3456", Name: "two"}}
	det := client.BuildHostDetails(h, &info, len(sessions))

	be := newFakeBackend()
	be.hosts = []client.Host{h}
	be.state["box"] = &fakeHost{info: info, sessions: sessions}
	hs := newHarness(t, be, 120, 40)

	sheetText := ansi.Strip((&details{host: "box"}).view(hs.m))
	for _, want := range []string{det.Target, det.System, det.Helper, "claude 2.1.3", det.Limits} {
		if !strings.Contains(sheetText, want) {
			t.Errorf("launcher details sheet lacks shared value %q:\n%s", want, sheetText)
		}
	}
}
