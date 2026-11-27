package client

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/proto"
)

func TestSavedSourceAndFactsRefuseHandEditedRowBeforeAnyRPC(t *testing.T) {
	hosts := placementHostsFixture(t)
	h := hosts[0]
	c := finishGeneratedTransportFixture(t, h, strings.Repeat("a", 32), "facts")
	c.pending = map[uint64]chan proto.Response{}
	calls := 0
	sharedGrantWire(t, c, func(proto.Request) any { calls++; return hostFactsFixtureValue() })
	if err := c.CheckSavedSource(t.Context()); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(hostsPath())
	if err != nil {
		t.Fatal(err)
	}
	after := bytes.Replace(before, []byte("box.example"), []byte("changed.example"), 1)
	if bytes.Equal(before, after) {
		t.Fatal("fixture lost its selected saved row")
	}
	// A user can hand-edit the canonical document without changing its
	// generation comment. The exact original row must still restrict reads.
	if err := os.WriteFile(hostsPath(), after, 0600); err != nil {
		t.Fatal(err)
	}
	if err := c.CheckSavedSource(t.Context()); err == nil {
		t.Fatal("an unchanged marker renewed a changed original saved row")
	}
	if _, err := c.HostFacts(t.Context()); err == nil || calls != 0 {
		t.Fatal("changed original row entered a facts RPC", calls, err)
	}
}
