package client

import (
	"context"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/supply"
)

func TestAccountPreparationCannotStartWithoutOriginalRootFactory(t *testing.T) {
	for _, optIn := range []bool{false, true} {
		var connection *Conn
		if _, err := connection.PrepareAccountRootScript(context.Background(), nil, optIn); err == nil {
			t.Fatal("bare option data created an account root script")
		}
	}
	if _, err := BuildRootScript("fixture", 1000, []RootStepID{RootLauncher, RootSlots}, 0); err == nil {
		t.Fatal("the public formatter issued account steps without the original private source")
	}
}

func TestAccountFormatterHasFixedNoArgumentSudoRuleAndRejectsTampering(t *testing.T) {
	// This generated private value exercises formatting only. It has no
	// actual saved transport/Source and must fail the production check below.
	request := NewRootStepGrantRequest()
	ctx := WithGrantRequest(t.Context(), request)
	p := &rootAccountPreparation{conn: &Conn{}, request: request, user: "fixture", uid: 1000, arch: "amd64", item: supply.Item{Kind: supply.KindLaunch, Name: "launch", Arch: "amd64", SHA256: strings.Repeat("a", 64), Size: 1234}, ids: []RootStepID{RootLinger, RootLauncher, RootSlots}, staged: true}
	script, err := p.build()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(script.Text, `NOPASSWD: /usr/local/libexec/`+brand.Name+`/launch ""`) {
		t.Fatal("the sudoers rule did not forbid launcher arguments")
	}
	if !strings.Contains(script.Text, "/var/tmp/"+brand.Name+"-launch-1000-") {
		t.Fatal("two users share the staged source filename")
	}
	probe := api.CapProbe{OS: "linux", User: "fixture", UID: 1000, Arch: "amd64"}
	for _, mutate := range []func(*RootScript){func(s *RootScript) { s.Text += "echo changed\n" }, func(s *RootScript) { s.SHA256 = strings.Repeat("b", 64) }, func(s *RootScript) { s.accounts = nil }} {
		changed := cloneRootScript(script)
		mutate(&changed)
		if _, err := p.canonical(probe, changed); err == nil {
			t.Fatal("a changed script passed the fixed private rebuild")
		}
	}
	if err := p.check(ctx, p.conn, probe); err == nil {
		t.Fatal("formatting data issued an actual original Source")
	}
}

func TestEveryUndoKnowsPriorAccountJournalWithoutInstallingAccounts(t *testing.T) {
	undo, err := rootAccountsUndo("fixture", 1000)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"launcher)", "slots)", "restore '/etc/sudoers.d/" + brand.Name + "-fixture'", "flock -n"} {
		if !strings.Contains(undo, value) {
			t.Errorf("fixed account undo lost %q", value)
		}
	}
	if strings.Contains(undo, "useradd ") || strings.Contains(undo, "NOPASSWD: ALL") {
		t.Fatal("undo created an account or allowed an arbitrary privileged command")
	}
}

func TestRootAccountOutputAndLifetimeStayBounded(t *testing.T) {
	output := rootAccountOutput{data: make([]byte, 4096)}
	if _, err := output.Write([]byte{1}); err == nil || len(output.data) != 4096 {
		t.Fatal("overlong staging output was published")
	}
	p := &rootAccountPreparation{starting: true, data: []byte("public payload")}
	if p.close() == nil || len(p.data) == 0 {
		t.Fatal("request closure discarded an unjoined native stage")
	}
	p.starting = false
	if err := p.close(); err != nil || len(p.data) != 0 {
		t.Fatal("joined stage resources were not released", err)
	}
}
