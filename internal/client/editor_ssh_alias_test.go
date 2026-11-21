package client

import (
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
)

func TestEditorSSHAliasRejectsShellSyntaxBeforeAnyLookup(t *testing.T) {
	valid := "auth-fix.box." + brand.Name
	if session, host, err := ParseEditorSSHAlias(valid); err != nil || session != "auth-fix" || host != "box" {
		t.Fatal(session, host, err)
	}
	for _, value := range []string{"", "auth-fix.box", "auth-fix.box.other", "auth-fix.box." + brand.Name + ".", "auth-fix.box.extra." + brand.Name, "Auth-fix.box." + brand.Name, "auth-fix.BOX." + brand.Name, "auth-fix.box." + brand.Name + ";command", "auth-fix.$(command)." + brand.Name, "auth-fix.box." + brand.Name + "\n", "auth_fix.box." + brand.Name, "auth-fix./box." + brand.Name} {
		if _, _, err := ParseEditorSSHAlias(value); err == nil {
			t.Fatal("unsafe or inexact alias reached lookup", value)
		}
	}
}

func TestEditorSSHRefusesDTOAndMissingSourceBeforeTransport(t *testing.T) {
	connection := &Conn{Host: Host{Name: "box"}, hostID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Features: []string{"editor-ssh-v1"}}
	if err := connection.ServeEditorSSH(t.Context(), "abcd2345", nil, nil, nil); err == nil {
		t.Fatal("a public connection description fabricated an original SSH invocation")
	}
	invocation := &editorSSHInvocationV1{parent: connection, creation: &SandboxPolicy{SessionID: "abcd2345", HostID: connection.HostID(), CreationWall: "process", Ceiling: TierOwn}}
	if _, release, err := invocation.sourceTurn(t.Context()); err == nil || release != nil {
		t.Fatal("creation/source metadata fabricated a held physical writer source")
	}
}
