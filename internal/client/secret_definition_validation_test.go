package client

import (
	"strings"
	"testing"
)

func TestSecretDefinitionValueStatesKeepCanonicalScalarSemantics(t *testing.T) {
	for _, name := range []string{"", "A", "_", "KEY_2", "2KEY", "a-b", "é", "SSH_AUTH_SOCK", "ssh_auth_sock", strings.Repeat("A", 1<<20)} {
		var state secretNameValidation
		for _, r := range name {
			state.Rune(r)
		}
		want := secretNameOK
		if !secretNameRE.MatchString(name) {
			want = secretNameInvalid
		} else if name == "SSH_AUTH_SOCK" {
			want = secretNameReserved
		}
		if state.Check(false) != want || checkSecretName(name, false) != want {
			t.Fatal("streamed final name and ordinary canonical name semantics differ")
		}
		if name == "SSH_AUTH_SOCK" && state.Check(true) != secretNameOK {
			t.Fatal("tier name checking inherited the definition-only reserved socket ban")
		}
	}
	for _, command := range []string{"", " \t\n", "\u2003\u00a0", " \u2003echo value\u00a0 ", "echo\x00value", "\ufeff", "\ufffd", strings.Repeat(" ", 1<<20) + "x"} {
		var state secretRunValidation
		for _, r := range command {
			state.Rune(r)
		}
		want := strings.TrimSpace(command) != "" && !strings.ContainsRune(command, 0)
		if state.Valid() != want || usableSecretRun(command) != want {
			t.Fatal("streamed final run and canonical Unicode trim/NUL semantics differ")
		}
	}
	for _, tc := range []struct {
		text string
		kind secretKindClass
	}{{"", secretKindEmpty}, {"ssh-key", secretKindSSHKey}, {"SSH-KEY", secretKindInvalid}, {"ssh-keyx", secretKindInvalid}, {"ssh-", secretKindInvalid}} {
		var state secretKindValidation
		for _, r := range tc.text {
			state.Rune(r)
		}
		if state.Kind() != tc.kind || classifySecretKind(tc.text) != tc.kind {
			t.Fatal("streamed final kind and ordinary canonical classification differ")
		}
	}
}

func TestSecretBindingPresenceKernelPreservesDefaultsAndExplicitRefusals(t *testing.T) {
	base := secretBindingPresence{DefaultHostsPresent: true, DefaultHostsCount: 1, DefaultHeaderNonempty: true, DefaultFormatNonempty: true}
	for _, tc := range []struct {
		change func(*secretBindingPresence)
		issue  secretBindingIssue
		raw    bool
	}{
		{func(*secretBindingPresence) {}, secretBindingOK, false},
		{func(p *secretBindingPresence) { p.DefaultFormatNonempty = false }, secretBindingOK, true},
		{func(p *secretBindingPresence) { p.FormatPresent = true; p.FormatExplicitEmpty = true }, secretBindingEmptyFormat, false},
		{func(p *secretBindingPresence) { p.HostsPresent = true }, secretBindingEmptyHosts, false},
		{func(p *secretBindingPresence) { p.Kind = secretKindSSHKey }, secretBindingSSHKey, false},
		{func(p *secretBindingPresence) { p.FormatPresent = true; p.FormatNonempty = true }, secretBindingOK, false},
	} {
		input := base
		tc.change(&input)
		selected, issue := planSecretBinding(input)
		if issue != tc.issue || issue == secretBindingOK && (!selected.Bound || selected.DefaultRaw != tc.raw) {
			t.Fatal("canonical binding presence/default selection changed")
		}
	}
	selected, issue := planSecretBinding(secretBindingPresence{HostsPresent: true, HostsCount: 1})
	if issue != secretBindingOK || !selected.Bound || selected.DefaultRaw || selected.Header != secretBindingNone {
		t.Fatal("hosts-only metadata inferred a credential header or complete binding")
	}
	selected, issue = planSecretBinding(secretBindingPresence{})
	if issue != secretBindingOK || selected.Bound || selected.DefaultRaw {
		t.Fatal("absent binding metadata invented a binding")
	}
}
