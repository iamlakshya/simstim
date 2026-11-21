package host

import (
	"fmt"
	"io"
	"sync"

	"github.com/iamlakshya/simstim/internal/api"
)

// Each carrier belongs to one actual Cold attempt of the original accepted
// rewind. A retry retains its previous carrier and genuine native receipt;
// no ordinary one-use secret carrier is reset or reused.
type rewindStartupDeliveryV1 struct {
	mu          sync.Mutex
	preparation *bridgeRewindPreparationV1
	cold        *ColdResumeV1
	previous    *rewindStartupDeliveryV1
	retry       rewindRetryCustodyV1
	named       *api.NamedSSHKeysV1
	primary     protectedTrustedProcessV1
	members     map[string]protectedTrustedProcessV1
	consumed    bool
	closed      bool
}

func (*rewindStartupDeliveryV1) Format(f fmt.State, _ rune) {
	_, _ = io.WriteString(f, api.Redacted)
}

func (delivery *rewindStartupDeliveryV1) clearV1() {
	if delivery == nil {
		return
	}
	delivery.mu.Lock()
	defer delivery.mu.Unlock()
	if delivery.named != nil {
		clear(delivery.named.Values)
	}
	clear(delivery.primary.Values)
	for _, member := range delivery.members {
		clear(member.Values)
	}
	delivery.closed = true
	if delivery.previous != nil {
		delivery.previous.clearV1()
	}
}
