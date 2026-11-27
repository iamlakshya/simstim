package egress

import (
	"bufio"
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

type boundControlFixture struct {
	server            *Server
	writer            *proto.Writer
	events            chan MintedStateEvent
	audits            chan AuditEntry
	responses         chan proto.Response
	failures          chan error
	sequence          atomic.Uint64
	eventCount        atomic.Uint64
	acks              chan MintedStateEvent
	readDone, ackDone chan struct{}
	serveDone         chan struct{}
}

func newBoundControlFixture(t *testing.T, autoAck bool) *boundControlFixture {
	return newBoundControlFixtureWithOptions(t, autoAck, Options{})
}

func newBoundControlFixtureWithOptions(t *testing.T, autoAck bool, options Options) *boundControlFixture {
	t.Helper()
	clientConn, serverConn := net.Pipe()
	if options.Dialer == nil {
		options.Dialer = &Dialer{}
	}
	if options.OriginRoots == nil {
		options.OriginRoots = x509.NewCertPool()
	}
	server, err := NewServer(serverConn, options)
	if err != nil {
		t.Fatal(err)
	}
	serveDone := make(chan struct{})
	go func() { server.Serve(); close(serveDone) }()
	peer := link.New(clientConn, link.Supervisor, LinkOptions())
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	stream, err := peer.Open(ctx, link.KindCtl, nil)
	if err != nil {
		t.Fatal(err)
	}
	f := &boundControlFixture{server: server, writer: proto.NewWriter(stream), events: make(chan MintedStateEvent, maxActiveFlows), audits: make(chan AuditEntry, maxActiveFlows), responses: make(chan proto.Response, maxActiveFlows), failures: make(chan error, 1), acks: make(chan MintedStateEvent, maxActiveFlows), readDone: make(chan struct{}), ackDone: make(chan struct{}), serveDone: serveDone}
	f.sequence.Store(10)
	t.Cleanup(func() {
		_ = peer.Close()
		_ = server.Close()
		for _, done := range []<-chan struct{}{f.readDone, f.ackDone, f.serveDone} {
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Error("owned control fixture worker did not finish")
			}
		}
	})
	if autoAck {
		go f.saveAcknowledgements()
	} else {
		close(f.ackDone)
	}
	go f.read(bufio.NewReader(stream), autoAck)
	if err := f.writer.WriteJSON(proto.TRequest, proto.Request{ID: 1, Op: CtlCapabilities}); err != nil {
		t.Fatal(err)
	}
	select {
	case response := <-f.responses:
		if response.ID != 1 || !response.OK {
			t.Fatal("control fixture did not complete the real capability handshake")
		}
	case err := <-f.failures:
		t.Fatal(err)
	case <-ctx.Done():
		t.Fatal("control fixture did not become ready")
	}
	return f
}

func (f *boundControlFixture) read(reader *bufio.Reader, autoAck bool) {
	defer close(f.readDone)
	defer close(f.acks)
	for {
		frame, err := proto.Read(reader)
		if err != nil {
			return
		}
		switch frame.Type {
		case proto.TEvent:
			var event struct {
				Event  string            `json:"event"`
				Minted *MintedStateEvent `json:"minted"`
				Audit  *AuditEntry       `json:"audit"`
			}
			if json.Unmarshal(frame.Payload, &event) != nil {
				f.recordFailure(errors.New("test control event is malformed"))
				return
			}
			if event.Event == EventAudit && event.Audit != nil {
				select {
				case f.audits <- *event.Audit:
				default:
					f.recordFailure(errors.New("test audit observer overflowed"))
					return
				}
				continue
			}
			if event.Event != EventSecretMinted || event.Minted == nil {
				continue
			}
			f.eventCount.Add(1)
			select {
			case f.events <- *event.Minted:
			default:
				// Auto peers keep only a bounded observation mailbox. Their
				// actual receipt worker below still sees every event. Manual
				// persistence oracles must explicitly consume all observations.
				if !autoAck {
					f.recordFailure(errors.New("test public event consumer overflowed"))
					return
				}
			}
			if autoAck {
				select {
				case f.acks <- *event.Minted:
				default:
					f.recordFailure(errors.New("test acknowledgement consumer overflowed"))
					return
				}
			}
		case proto.TResponse:
			var response proto.Response
			if json.Unmarshal(frame.Payload, &response) != nil {
				f.recordFailure(errors.New("test control response is malformed"))
				return
			}
			if autoAck && response.ID > 10 {
				if !response.OK {
					f.recordFailure(errors.New("test durable acknowledgement was refused"))
					return
				}
				continue
			}
			select {
			case f.responses <- response:
			default:
				f.recordFailure(errors.New("test response consumer overflowed"))
				return
			}
		}
	}
}

func (f *boundControlFixture) saveAcknowledgements() {
	defer close(f.ackDone)
	for event := range f.acks {
		// The test peer acknowledges outside the sole reply reader. Actual
		// durable storage is a separate supervisor integration oracle.
		if err := f.acknowledge(event, true); err != nil {
			f.recordFailure(err)
			return
		}
	}
}

func (f *boundControlFixture) recordFailure(err error) {
	select {
	case f.failures <- err:
	default:
	}
}

func (f *boundControlFixture) acknowledge(event MintedStateEvent, durable bool) error {
	args := SecretSavedArgs{Version: event.Version, Generation: event.Generation, Receipt: event.Receipt, Owner: event.Owner, Identity: event.State.Identity, Durable: durable}
	raw, err := json.Marshal(args)
	if err != nil {
		return err
	}
	return f.writer.WriteJSON(proto.TRequest, proto.Request{ID: f.sequence.Add(1), Op: CtlSecretSaved, Args: raw})
}

func (f *boundControlFixture) nextMint(t *testing.T) MintedStateEvent {
	t.Helper()
	select {
	case event := <-f.events:
		return event
	case err := <-f.failures:
		t.Fatal(err)
	case <-time.After(2 * time.Second):
		t.Fatal("mint did not emit its public durable-state reservation")
	}
	return MintedStateEvent{}
}

func (f *boundControlFixture) nextAudit(t *testing.T) AuditEntry {
	t.Helper()
	select {
	case audit := <-f.audits:
		return audit
	case err := <-f.failures:
		t.Fatal(err)
	case <-time.After(2 * time.Second):
		t.Fatal("actual flow did not emit its final audit record")
	}
	return AuditEntry{}
}
