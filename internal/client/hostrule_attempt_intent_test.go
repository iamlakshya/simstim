package client

import (
	"context"
	"errors"
	"testing"
)

func TestHostAttemptFinalNewKeepsOriginalIntentAndAllowsActualSourceDefaults(t *testing.T) {
	hostSelectionConfig(t, "[agents]\ninline=true\n[sessions]\nmemory_max='2G'\n")
	selection, err := PrepareHostSelection(t.Context(), "box", NewInputOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer selection.Close()
	owner, cancelOwner := context.WithCancel(t.Context())
	defer cancelOwner()
	work, cancelWork := context.WithCancel(owner)
	defer cancelWork()
	r := NewGrantRequest(false, false)
	defer r.Close()
	bound, err := selection.BindNewRequest(work, r, "box")
	if err != nil {
		t.Fatal(err)
	}
	host, err := FindHost("box")
	if err != nil {
		t.Fatal(err)
	}
	// An invalid session name makes the real Conn.NewSession stop before
	// transport/source use. No fake connection mints successful native proof.
	original := NewArgs{Name: "abcd2345", Agent: "shell", Cwd: "~", Task: "original", Wall: "none"}
	p := &HostAttemptPlanV1{ctx: owner, cancel: cancelOwner, selection: selection, args: original}
	a := &HostAttemptV1{plan: p, host: host, target: host.Name, request: r, ctx: bound,
		cancel: cancelWork, conn: &Conn{Host: host}, ready: true}
	r.hostAttempt = a
	p.active = a
	prepared, err := PrepareNewSessionArgs(a.Context(), a.Args(), 80, 24)
	if err != nil || !prepared.Inline || prepared.MemoryMax != 2<<30 || prepared.Cols != 80 {
		t.Fatal("actual held-source preparation did not apply defaults", prepared, err)
	}
	changes := []struct {
		name   string
		change func(*NewArgs)
	}{
		{"agent", func(a *NewArgs) { a.Agent = "codex" }},
		{"argv", func(a *NewArgs) { a.Argv = []string{"replacement"} }},
		{"folder", func(a *NewArgs) { a.Cwd = "/other" }},
		{"name", func(a *NewArgs) { a.Name = "other" }},
		{"base", func(a *NewArgs) { a.Base = "other" }},
		{"worktree", func(a *NewArgs) { a.Worktree = true }},
		{"task", func(a *NewArgs) { a.Task = "replacement" }},
		{"wall", func(a *NewArgs) { a.Wall = "process" }},
		{"size", func(a *NewArgs) { a.MemoryMax = 4 << 30 }},
		{"inline", func(a *NewArgs) { a.Inline = false }},
		{"ssh agent", func(a *NewArgs) { a.SSHAgent = true }},
	}
	for _, change := range changes {
		actual := prepared
		change.change(&actual)
		if _, err := a.NewSession(t.Context(), actual); err == nil {
			t.Fatal(change.name, "replaced the original invocation at final New")
		}
		if a.newCalled || a.failure == nil || a.failure.phase != hostAttemptLocalV1 {
			t.Fatal(change.name, "escaped the local original-intent guard")
		}
	}
	_, returned := a.NewSession(t.Context(), prepared)
	var user *UserError
	if !errors.As(returned, &user) || user.Code != "bad_name" || !a.newCalled ||
		a.failure == nil || a.failure.phase != hostAttemptNewV1 {
		t.Fatal("actual source defaults did not reach canonical New validation", returned)
	}
}

func TestHostAttemptSourceIntakeRejectsChangedIntentBeforePreparation(t *testing.T) {
	hostSelectionConfig(t, "")
	originalOptions := NewInputOptions{OriginalCWD: t.TempDir(), HostClone: true}
	selection, err := PrepareHostSelection(t.Context(), "box", originalOptions)
	if err != nil {
		t.Fatal(err)
	}
	defer selection.Close()
	owner, cancel := context.WithCancel(t.Context())
	defer cancel()
	r := NewGrantRequest(false, false)
	defer r.Close()
	original := NewArgs{Agent: "shell", Cwd: "~", Name: "original", Wall: "none"}
	p := &HostAttemptPlanV1{ctx: owner, cancel: cancel, selection: selection, args: original}
	a := &HostAttemptV1{plan: p, request: r, ctx: owner, cancel: cancel, target: "box", ready: true}
	r.hostAttempt, p.active = a, a
	bound, err := selection.BindNewRequest(owner, r, "box")
	if err != nil {
		t.Fatal(err)
	}
	host, err := FindHost("box")
	if err != nil {
		t.Fatal(err)
	}
	a.ctx, a.host, a.conn = bound, host, &Conn{Host: host}
	changes := []struct {
		name   string
		change func(*NewArgs, *NewInputOptions)
	}{
		{"agent", func(a *NewArgs, _ *NewInputOptions) { a.Agent = "codex" }},
		{"argv", func(a *NewArgs, _ *NewInputOptions) { a.Argv = []string{"replacement"} }},
		{"folder", func(a *NewArgs, _ *NewInputOptions) { a.Cwd = "/other" }},
		{"seeds", func(_ *NewArgs, o *NewInputOptions) { o.Seeds = []string{"replacement"} }},
		{"profile", func(_ *NewArgs, o *NewInputOptions) { o.Profile = "replacement" }},
		{"environment", func(_ *NewArgs, o *NewInputOptions) {
			o.Environment = []EnvironmentOption{{Value: "PUBLIC=replacement"}}
		}},
		{"primary", func(_ *NewArgs, o *NewInputOptions) { o.HostClone = false }},
		{"local folder", func(_ *NewArgs, o *NewInputOptions) { o.OriginalCWD = "/replacement" }},
		{"session values", func(_ *NewArgs, o *NewInputOptions) { value := "open"; o.Session.Sandbox.Preset = &value }},
	}
	for _, change := range changes {
		args, options := original, cloneNewInputOptions(originalOptions)
		change.change(&args, &options)
		_, returned := a.conn.PrepareNewInputPolicy(bound, args, options)
		var user *UserError
		if !errors.As(returned, &user) || user.Code != "grant_scope" ||
			r.preparationSettings != nil || r.newInputs != nil || r.newSecrets != nil {
			t.Fatal(change.name, "reached source preparation before original-intent rejection", returned)
		}
	}
	changedAgent := original
	changedAgent.Agent = "codex"
	if _, returned := a.conn.PrepareNewSecrets(bound, changedAgent); returned == nil || r.preparationSettings != nil {
		t.Fatal("secret preparation selected an altered agent before the intake guard", returned)
	}
	// Omission is enriched from the already-owned CWD, without Getwd or a
	// fresh metadata source. All other option fields remain byte-for-byte intent.
	options := cloneNewInputOptions(originalOptions)
	options.OriginalCWD = ""
	if err := checkHostAttemptIntakeV1(bound, original, &options); err != nil || options.OriginalCWD != originalOptions.OriginalCWD {
		t.Fatal("the original owned input options could not supply their CWD", err)
	}
}
