package chv

import (
	"context"
	"net"

	"github.com/iamlakshya/simstim/internal/wall/microvm"
)

// MemoryBudget is the canonical guest RAM, reserve and unit-cap tuple.
type MemoryBudget = microvm.MemoryBudget

// BootTimeout is the shared deadline for the verified guest boot handshake.
const BootTimeout = microvm.BootTimeout

// GuestPort is the guest init's private link port.
const GuestPort = microvm.GuestPort

// BudgetFor delegates to the canonical memory calculation.
func BudgetFor(guest int64) (MemoryBudget, error) { return microvm.BudgetFor(guest) }

// ConnectGuest opens the shared hybrid-vsock link and preserves buffered bytes.
func ConnectGuest(ctx context.Context, socket string) (net.Conn, error) {
	return microvm.ConnectGuest(ctx, socket)
}
