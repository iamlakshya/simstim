package supervisor

import (
	"slices"
	"sync"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/proto"
)

// Every admitted receipt has a positive encoded-frame charge. The count
// bound therefore follows the same byte budget, with no smaller field cap.
const maxQueuedMintedReceipts = proto.MaxFrame

type mintedProposalKey struct{ owner, identity string }
type mintedReceiptKey struct{ generation, receipt string }

type mintedProposal struct {
	owner    string
	state    api.PublicMintedSecretState
	receipts map[mintedReceiptKey]struct{}
}

type queuedMintedReceipt struct {
	proposal *mintedProposal
	key      mintedReceiptKey
	bytes    int
}

// mintedQueue is a bounded work queue, not a value registry. The front
// receipt remains charged while its single worker performs disk/RPC work.
type mintedQueue struct {
	mu       sync.Mutex
	wake     chan struct{}
	finished chan struct{}
	groups   map[mintedProposalKey]*mintedProposal
	order    []queuedMintedReceipt
	bytes    int
	closed   bool
}

func newMintedQueue() *mintedQueue {
	return &mintedQueue{wake: make(chan struct{}, 1), finished: make(chan struct{}), groups: make(map[mintedProposalKey]*mintedProposal)}
}

func samePublicMintedState(a, b api.PublicMintedSecretState) bool {
	if a.Sentinel != b.Sentinel || a.Identity != b.Identity || a.Authority != b.Authority || a.Rule != b.Rule {
		return false
	}
	if a.Shape == nil || b.Shape == nil {
		return a.Shape == nil && b.Shape == nil
	}
	return a.Shape.Version == b.Shape.Version && a.Shape.Prefix == b.Shape.Prefix && a.Shape.Rule == b.Shape.Rule && slices.Equal(a.Shape.Slots, b.Shape.Slots)
}

func clonePublicMintedState(owner string, state api.PublicMintedSecretState) api.PublicMintedSecretState {
	return api.ClonePublicMintedStates(map[string][]api.PublicMintedSecretState{owner: {state}})[owner][0]
}

// enqueue drops only a refused receipt. The backend's existing bounded
// control lifetime then fails that response, without a global link failure.
func (q *mintedQueue) enqueue(event egress.MintedStateEvent, wireBytes int) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed || event.Version != egress.MemberRegistryV2 || event.Owner == "" || event.Generation == "" || event.Receipt == "" || event.State.Identity == "" || wireBytes <= 0 || wireBytes > proto.MaxFrame {
		return false
	}
	key := mintedProposalKey{event.Owner, event.State.Identity}
	receipt := mintedReceiptKey{event.Generation, event.Receipt}
	group := q.groups[key]
	if group != nil {
		if !samePublicMintedState(group.state, event.State) {
			return false
		}
		if _, duplicate := group.receipts[receipt]; duplicate {
			return true
		}
	}
	if len(q.order) >= maxQueuedMintedReceipts || wireBytes > proto.MaxFrame-q.bytes {
		return false
	}
	if group == nil {
		group = &mintedProposal{owner: event.Owner, state: clonePublicMintedState(event.Owner, event.State), receipts: make(map[mintedReceiptKey]struct{})}
		q.groups[key] = group
	}
	group.receipts[receipt] = struct{}{}
	q.order = append(q.order, queuedMintedReceipt{group, receipt, wireBytes})
	q.bytes += wireBytes
	select {
	case q.wake <- struct{}{}:
	default:
	}
	return true
}

func (q *mintedQueue) front() (egress.MintedStateEvent, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed || len(q.order) == 0 {
		return egress.MintedStateEvent{}, false
	}
	entry := q.order[0]
	return egress.MintedStateEvent{Version: egress.MemberRegistryV2, Owner: entry.proposal.owner, Generation: entry.key.generation, Receipt: entry.key.receipt, State: entry.proposal.state}, true
}

func (q *mintedQueue) complete() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.order) == 0 {
		return
	}
	entry := q.order[0]
	q.order[0] = queuedMintedReceipt{}
	q.order = q.order[1:]
	q.bytes -= entry.bytes
	delete(entry.proposal.receipts, entry.key)
	if len(entry.proposal.receipts) == 0 {
		delete(q.groups, mintedProposalKey{entry.proposal.owner, entry.proposal.state.Identity})
	}
}

func (q *mintedQueue) close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.closed, q.bytes = true, 0
	q.order, q.groups = nil, nil
}
