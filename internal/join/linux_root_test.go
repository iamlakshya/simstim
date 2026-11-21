//go:build linux

package join

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/client"
)

func TestSelectedRootUndoRetainsOriginalProducerAndOnlyOwnJournals(t *testing.T) {
	script, err := client.BuildRootScript("dev", 1000, []client.RootStepID{client.RootKVMACL, client.RootKVMGroup}, 0)
	if err != nil {
		t.Fatal(err)
	}
	text, err := selectRootUndo(script.Undo, []journalIdentity{{Name: "00000003", ID: "kvm-acl"}, {Name: "00000004", ID: "kvm-group"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, `case "${j##*/}" in 00000003|00000004) ;; *) continue;; esac`) {
		t.Fatal("undo did not select original journals")
	}
	if !strings.Contains(text, "find \"$d/journal\" -mindepth 1 -maxdepth 1 -print -quit") {
		t.Fatal("undo would remove an account's other root journal")
	}
	if !strings.Contains(text, "undo_linger_origin") || !strings.Contains(text, "undo_machine") {
		t.Fatal("selection discarded native producer guards")
	}
	// Parse only. This never executes a root command or touches a native path.
	command := exec.Command("/bin/sh", "-n")
	command.Stdin = strings.NewReader(text)
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("selected undo syntax: %v: %s", err, out)
	}
	if _, err = selectRootUndo(script.Undo, []journalIdentity{{Name: "00000003;bad", ID: "kvm-acl"}}); err == nil {
		t.Fatal("accepted shell syntax as a journal name")
	}
	if _, err = selectRootUndo("changed", nil); err == nil {
		t.Fatal("accepted a changed root producer template")
	}
}
func TestWritableAncestorCannotBecomeJoinLedger(t *testing.T) {
	if err := protectedPath(t.TempDir(), true); err == nil {
		t.Fatal("a test-owned temporary directory became a root-only ledger")
	}
}
