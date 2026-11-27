package supervisor

import (
	"context"
	"net"
	"testing"
	"time"

	"golang.org/x/crypto/ssh/agent"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/link"
)

func TestGuestStreamsServeNamedAgentBeforeBootCompletesAndJoin(t *testing.T) {
	value, _ := namedSSHFixture(t)
	keys, err := newNamedSSHAgent(t.Context(), []string{"DEPLOY_KEY"}, map[string]api.Secret{"DEPLOY_KEY": value}, nil)
	if err != nil {
		t.Fatal(err)
	}
	left, right := net.Pipe()
	parent := link.New(left, link.Supervisor, link.Options{})
	guest := link.New(right, link.Guest, link.Options{})
	unusedLeft, unusedRight := net.Pipe()
	egressSession := link.New(unusedLeft, link.Supervisor, link.Options{})
	egressPeer := link.New(unusedRight, link.Guest, link.Options{})
	done := make(chan struct{})
	close(done)
	s := &Supervisor{link: parent, ctl: &ctlConn{done: done}, eg: &egressProc{sess: egressSession, ctl: &egressCtl{}}, sshAgent: keys, rec: record{phase: host.LifeBooting}}
	t.Cleanup(func() {
		s.stopNamedSSHAgent()
		s.closeGuestStreamReaderV1()
		guest.Close()
		egressSession.Close()
		egressPeer.Close()
	})
	if err := s.startGuestStreamReaderV1(); err != nil {
		t.Fatal(err)
	}
	if err := s.startGuestStreamReaderV1(); err == nil {
		t.Fatal("duplicate reader started")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	stream, err := guest.Open(ctx, link.KindAgent, struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	_ = stream.SetDeadline(time.Now().Add(5 * time.Second))
	client := agent.NewClient(stream)
	listed, err := client.List()
	if err != nil || len(listed) != 1 {
		t.Fatal("setup-phase named agent blocked behind boot completion", err)
	}
	if s.rec.phase != host.LifeBooting {
		t.Fatal("serving the key pretended that boot completed")
	}
	s.stopNamedSSHAgent()
	joined := make(chan struct{})
	go func() { s.closeGuestStreamReaderV1(); close(joined) }()
	select {
	case <-joined:
	case <-time.After(2 * time.Second):
		t.Fatal("original guest reader or agent worker did not join")
	}
	if _, err := client.List(); err == nil {
		t.Fatal("closed original stream still signed or listed")
	}
}
