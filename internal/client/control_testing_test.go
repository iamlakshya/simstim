//go:build !windows

package client

import (
	"os"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/testhome"
)

func TestTestControlFallbackKeepsTheFullProductionGuards(t *testing.T) {
	d, err := testhome.NewSocketDir(15)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	old := controlFallbackDir(1)
	restore, err := ConfigureControlFallbackForTesting(d.Path())
	if err != nil {
		t.Fatal(err)
	}
	if got := controlFallbackDir(1); got != d.Path() {
		t.Errorf("the harness fallback is %q", got)
	}
	held := d.Path() + "-held"
	if err := os.Rename(d.Path(), held); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(d.Path(), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := controlFallbackDir(1); got != "" {
		t.Errorf("the hook adopted a replacement inode: %q", got)
	}
	if err := os.Remove(d.Path()); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(held, d.Path()); err != nil {
		t.Fatal(err)
	}
	restore()
	if got := controlFallbackDir(1); got != old {
		t.Errorf("the harness did not restore the previous factory: %q", got)
	}
	if _, err := ConfigureControlFallbackForTesting(strings.Repeat("a", 16)); err == nil {
		t.Error("the hook accepted a root over the complete bind budget")
	}
	if err := os.Chmod(d.Path(), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := ConfigureControlFallbackForTesting(d.Path()); err == nil {
		t.Error("the hook accepted a public control root")
	}
	if err := os.Chmod(d.Path(), 0o700); err != nil {
		t.Fatal(err)
	}
}
