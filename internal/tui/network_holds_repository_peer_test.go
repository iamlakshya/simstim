package tui

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/client/clienttest"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

// This peer supplies framed protocol data to actual client.Dial and policy
// operations. It runs no guest and supplies no native or SSH runtime proof.
type heldRepositoryPeerV1 struct {
	*clienttest.Transport
	mu       sync.Mutex
	features []string
	hostID   string
	session  client.Session
	layers   []api.Layer
	policy   api.Policy
	held     api.PendingNetworkRequest
	requests []proto.Request
	applied  bool
}

func (p *heldRepositoryPeerV1) Start(_ context.Context, _ string) (client.Proc, error) {
	in, input := io.Pipe()
	output, out := io.Pipe()
	process := &heldRepositoryProcV1{input: input, output: output, done: make(chan struct{})}
	go func() {
		defer close(process.done)
		defer in.Close()
		defer out.Close()
		reader := bufio.NewReader(in)
		frame, err := proto.Read(reader)
		if err != nil || frame.Type != proto.THello {
			return
		}
		writer := proto.NewWriter(out)
		if err := writer.WriteJSON(proto.THello, proto.Hello{Magic: proto.Magic, Protocol: version.Protocol, Version: version.Version, Role: "bridge", HostID: p.hostID, Features: slices.Clone(p.features)}); err != nil {
			return
		}
		for {
			frame, err := proto.Read(reader)
			if err != nil {
				return
			}
			if frame.Type == proto.TPing {
				if writer.Write(proto.TPong, nil) != nil {
					return
				}
				continue
			}
			var request proto.Request
			if frame.Type != proto.TRequest || json.Unmarshal(frame.Payload, &request) != nil {
				return
			}
			result, err := p.answer(request)
			response := proto.Response{ID: request.ID, OK: err == nil}
			if err != nil {
				response.Error = &proto.Error{Code: "bad_request", Message: err.Error(), Hint: "Check the repository source regression."}
			} else {
				response.Result, _ = json.Marshal(result)
			}
			if writer.WriteJSON(proto.TResponse, response) != nil {
				return
			}
		}
	}()
	return process, nil
}

func (p *heldRepositoryPeerV1) setCreation(args client.NewArgs) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.session = client.Session{ID: args.ID, Name: "repository-source", Agent: "shell", Alive: true, State: api.StateIdle,
		Wall: &api.WallInfo{Level: args.Wall, Floor: args.Floor}}
	return p.setPolicyLocked(api.CloneLayers(args.Layers))
}

func (p *heldRepositoryPeerV1) setPolicyLocked(layers []api.Layer) error {
	policy, clean, err := egress.MergeLayers(layers)
	if err != nil {
		return err
	}
	policy.Sources = api.CloneLayers(clean)
	policy.LayersHash, err = api.HashPolicyLaptopLayersV1(clean)
	if err != nil {
		return err
	}
	p.layers, p.policy = clean, policy
	return nil
}

func (p *heldRepositoryPeerV1) answer(request proto.Request) (any, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	request.Args = slices.Clone(request.Args)
	p.requests = append(p.requests, request)
	switch request.Op {
	case "sandbox.probe":
		return api.CapProbe{Walls: []api.WallProbe{{Level: "process", OK: true}}}, nil
	case "ls":
		return []client.Session{p.session}, nil
	case "policy.ls":
		return p.policy, nil
	case api.OpPolicyPending:
		requests := []api.PendingNetworkRequest{p.held}
		if p.applied {
			requests = nil
		}
		return api.NetworkHoldSnapshot{Version: 1, Revision: p.held.Revision, Requests: requests}, nil
	case api.OpPolicyDescribe:
		return api.NetworkHoldDescriptor{Version: 1, Request: p.held.ID, Revision: p.held.Revision, Class: api.NetworkHoldPublicTarget, Expires: p.held.Expires}, nil
	case api.OpPolicyResolve:
		var args api.NetworkHoldActionArgs
		if json.Unmarshal(request.Args, &args) != nil || args.Session != p.session.ID || args.Request != p.held.ID || args.Scope != api.NetworkHoldRepo || args.Revision != p.held.Revision {
			return nil, fmt.Errorf("resolution changed the original repository action")
		}
		return api.NetworkHoldResolution{Version: 1, Request: p.held.ID, Revision: p.held.Revision, Scope: args.Scope, Target: "api.example.com:443", Class: api.NetworkHoldPublicTarget, Expires: p.held.Expires}, nil
	case "policy.replace":
		var args api.PolicyReplaceArgsV1
		if json.Unmarshal(request.Args, &args) != nil || args.Validate() != nil || args.Session != p.session.ID || args.PreviousHash != p.policy.LayersHash {
			return nil, fmt.Errorf("replacement lost its original session or hash")
		}
		if err := p.setPolicyLocked(args.Layers); err != nil {
			return nil, err
		}
		p.applied = true
		return api.PolicyReplaceResultV1{Version: 1, Hash: p.policy.LayersHash, State: "applied"}, nil
	default:
		return nil, fmt.Errorf("unexpected repository regression operation %s", request.Op)
	}
}

type heldRepositoryProcV1 struct {
	input  *io.PipeWriter
	output *io.PipeReader
	done   chan struct{}
}

func (p *heldRepositoryProcV1) Stdin() io.WriteCloser { return p.input }
func (p *heldRepositoryProcV1) Stdout() io.Reader     { return p.output }
func (p *heldRepositoryProcV1) Stderr() string        { return "" }
func (p *heldRepositoryProcV1) Wait() error           { <-p.done; return nil }
func (p *heldRepositoryProcV1) Kill() {
	_ = p.input.Close()
	_ = p.output.Close()
}

func (p *heldRepositoryPeerV1) observed() ([]proto.Request, []api.Layer, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.requests), api.CloneLayers(p.layers), p.applied
}

func newHeldRepositoryPeerV1(t *testing.T, features []string) *heldRepositoryPeerV1 {
	t.Helper()
	now := time.Now().UTC()
	return &heldRepositoryPeerV1{Transport: &clienttest.Transport{}, hostID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", features: features,
		held: api.PendingNetworkRequest{Version: 1, ID: "cccccccccccccccccccccccccccccccc", Revision: 7, Host: "api.example.com", Port: 443,
			Opened: now, Expires: now.Add(2 * time.Minute), Waiting: 1, Status: api.NetworkHoldOpen}}
}
