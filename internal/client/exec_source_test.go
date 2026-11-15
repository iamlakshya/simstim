package client

import (
	"io"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestExecRefusesUnacceptedTransportBeforeDedicatedDial(t *testing.T) {
	h := hostTransportSavedFixture(t)
	c := generatedHostTransportConn(h, strings.Repeat("a", 32), api.FeatureExecStream)
	// A generated private Proc and copied hello are not an accepted Dial.
	code, err := c.RunExec(t.Context(), api.ExecArgs{ID: "abcd2345", Argv: []string{"true"}}, nil, io.Discard, io.Discard)
	if code != 125 || err == nil {
		t.Fatalf("unaccepted command publication: code=%d err=%v", code, err)
	}
}

func TestExecOriginalSourceRefusesIdenticalHostReaddBeforeDedicatedDial(t *testing.T) {
	h := hostTransportSavedFixture(t)
	c := finishGeneratedTransportFixture(t, h, strings.Repeat("a", 32), api.FeatureExecStream)
	if err := SaveHosts(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if err := SaveHosts(t.Context(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	code, err := c.RunExec(t.Context(), api.ExecArgs{ID: "abcd2345", Argv: []string{"true"}}, nil, io.Discard, io.Discard)
	if code != 125 || err == nil {
		t.Fatalf("old Source borrowed replacement membership: code=%d err=%v", code, err)
	}
}

func TestExecDedicatedConnectionRequiresTheSameAcceptedSource(t *testing.T) {
	h := hostTransportSavedFixture(t)
	id := strings.Repeat("a", 32)
	first := finishGeneratedTransportFixture(t, h, id, api.FeatureExecStream)
	same := finishGeneratedTransportFixture(t, h, id, api.FeatureExecStream)
	if !sameExecTransportSource(first, same) {
		t.Fatal("same-generation accepted connections diverged")
	}
	if err := SaveHosts(t.Context(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	replacement := finishGeneratedTransportFixture(t, h, id, api.FeatureExecStream)
	if sameExecTransportSource(first, replacement) {
		t.Fatal("a fresh Dial donated its Source to an old request")
	}
}
