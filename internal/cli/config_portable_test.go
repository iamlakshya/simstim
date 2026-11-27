package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

// Both home APIs resolve the fixture, on Unix and Windows. Report paths
// use forward slashes after ~, while real file operations use native paths.
func TestConfigFixtureAndReportPaths(t *testing.T) {
	outer := t.TempDir()
	t.Setenv("HOME", outer)
	t.Setenv("USERPROFILE", outer)
	write := configHome(t)
	home, err := os.UserHomeDir()
	if err != nil || home == outer || home != os.Getenv("HOME") || home != os.Getenv("USERPROFILE") {
		t.Fatalf("fixture homes = %q, HOME %q, USERPROFILE %q, error %v", home, os.Getenv("HOME"), os.Getenv("USERPROFILE"), err)
	}
	write("config.toml", "[ui]\nmotoin = 'off'\n")
	want := "~/.config/" + brand.Name + "/config.toml"
	actual := filepath.Join(client.ConfigDir(), "config.toml")
	if got := tildeHome(actual); got != want || strings.Contains(got, `\`) {
		t.Fatalf("report path = %q, want %q", got, want)
	}
	if r := checkConfig(); len(r.Findings) != 1 || r.Findings[0].Path != want || r.Findings[0].Line != 2 {
		t.Fatalf("portable report = %+v", r)
	}
	outside := filepath.Join(outer, "untouched")
	if got := tildeHome(outside); got != outside {
		t.Fatalf("a path outside the home changed from %q to %q", outside, got)
	}
	if files, err := os.ReadDir(outer); err != nil || len(files) != 0 {
		t.Fatalf("the fixture touched the outer home: %v, %v", files, err)
	}
}
