package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

// The questions of host rm (docs/contributing/cli-spec.md 6.2 and 11.3).

// host rm --destroy needs --confirm with input off, and leaves the host
// untouched on Enter or a wrong name at its question. Without --destroy and
// with input off, it keeps the machine, and says so.
func TestHostQuestionsWithInputOff(t *testing.T) {
	m, _ := withFake(t, nil)
	if err := saveHost(client.Host{Name: "box", Machine: &client.Machine{Provider: "fake", ID: "fake-box"}}); err != nil {
		t.Fatal(err)
	}
	err := removeHost(context.Background(), quietPrompts(context.Background()), rmOpts{name: "box", destroy: true})
	var ue *client.UserError
	if !errors.As(err, &ue) || ue.Kind != client.KindNeedsAnswer || ue.Hint != brand.Name+" host rm box --destroy --confirm box" ||
		ue.Msg != "host rm needs the typed name of box to delete the fake machine fake-box, and input is off" {
		t.Fatalf("rm --destroy with input off: %+v", err)
	}
	if _, err := client.FindHost("box"); err != nil {
		t.Fatal("the host was forgotten")
	}
	// Enter keeps the machine that --destroy named, so nothing changes.
	for _, answer := range []string{"\n", "bo\n", "BOX\n"} {
		var out bytes.Buffer
		if err := removeHost(context.Background(), testPrompts(answer, &out), rmOpts{name: "box", destroy: true}); !isCancel(err) {
			t.Fatalf("rm --destroy, %q: %v", answer, err)
		}
		if !strings.Contains(out.String(), "Type box to delete the machine, or press Enter to keep it: ") {
			t.Fatalf("rm --destroy asked %q", out.String())
		}
		if _, err := client.FindHost("box"); err != nil {
			t.Fatalf("%q forgot the host", answer)
		}
	}
	// --confirm with another name is a usage error.
	err = removeHost(context.Background(), quietPrompts(context.Background()), rmOpts{name: "box", destroy: true, confirm: "bx", confirmed: true})
	if !errors.As(err, &ue) || ue.Kind != client.KindUsage || exitCode(ue.Kind) != 2 || ue.Hint != brand.Name+" host rm box --destroy --confirm box" {
		t.Fatalf("--confirm bx: %+v", err)
	}
	// Without --destroy, input off keeps the machine, and says how to
	// delete it too.
	var out bytes.Buffer
	p := quietPrompts(context.Background())
	p.out = &out
	if err := removeHost(context.Background(), p, rmOpts{name: "box"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Input is off, so host rm keeps the fake machine fake-box. With --destroy --confirm box, it deletes the machine too.") ||
		!strings.Contains(out.String(), "Forgot box. The fake machine fake-box stays, with its sessions and the helper: delete it with fake's own tools.") {
		t.Errorf("rm with input off said %q", out.String())
	}
	for _, c := range m.Calls() {
		if c == "destroy" {
			t.Error("the machine was destroyed")
		}
	}
	if _, err := client.FindHost("box"); err == nil {
		t.Error("the host is still saved")
	}
}
