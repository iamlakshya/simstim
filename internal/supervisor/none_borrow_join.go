package supervisor

import "errors"

// RuntimeCleaned releases these original borrowers first. The accepted serve
// stacks then join, and this checks actual incoming-OP closure before a durable
// None marker or manager escrow can certify this producer's final state.
func (s *Supervisor) joinNoneBorrowersV1() error {
	s.mu.Lock()
	proofs := make([]*noneStopCompletionV1, 0, len(s.noneStops))
	for proof := range s.noneStops {
		proofs = append(proofs, proof)
	}
	s.mu.Unlock()
	var failure error
	for _, proof := range proofs {
		proof.mu.Lock()
		committed := proof.committed
		proof.mu.Unlock()
		if committed {
			failure = errors.Join(failure, proof.closeBorrowV1())
		}
	}
	return failure
}
