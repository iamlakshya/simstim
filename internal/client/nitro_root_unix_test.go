//go:build !windows

package client

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
)

func TestNitroSlotUndoPreservesLaterUserRules(t *testing.T) {
	data, err := rootTemplates.ReadFile("rootstep/undo-slots.sh")
	if err != nil {
		t.Fatal(err)
	}
	_, tail, ok := strings.Cut(string(data), "            awk '\n")
	if !ok {
		t.Fatal("missing native rule-preserving undo")
	}
	script, _, ok := strings.Cut(tail, "\n            ' \"$m/nitro-rules.before\"")
	if !ok {
		t.Fatal("unknown rule-preserving undo boundary")
	}
	script = strings.ReplaceAll(script, "@BRAND@", brand.Name)
	rule := func(user string) string {
		return `ACTION=="add|change", SUBSYSTEM=="misc", KERNEL=="nitro_enclaves", RUN+="/usr/bin/setfacl -m u:` + user + `:rw /dev/nitro_enclaves"`
	}
	original := rule("dev") + "\n" + rule(brand.Name+"-s00") + "\n"
	later := original + rule("user") + "\n" + rule(brand.Name+"-s01") + "\n"
	dir := t.TempDir()
	before, current := filepath.Join(dir, "before"), filepath.Join(dir, "current")
	if err := os.WriteFile(before, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(current, []byte(later), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := exec.Command("awk", script, before, current).CombinedOutput()
	if err != nil {
		t.Fatalf("rule filter: %v %s", err, result)
	}
	expected := original + rule("user") + "\n"
	if string(result) != expected {
		t.Fatalf("undo changed another account's rule: %q", result)
	}
}
