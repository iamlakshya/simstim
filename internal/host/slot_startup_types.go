package host

import (
	"os"
	"sync"

	"github.com/iamlakshya/simstim/internal/proto"
)

type slotReservedStartupPrepareV2 struct {
	Version int    `json:"v"`
	Primary string `json:"primary"`
	Nonce   string `json:"nonce"`
	Need    string `json:"need"`
	Runtime string `json:"runtime"`
}

type slotReservedStartupLoanV2 struct {
	Version int           `json:"v"`
	Base    startupLoanV1 `json:"base"`
}

type slotReservedStartupPendingV2 struct {
	owner            *OwnedSupervisorLease
	line             *StartupLineV1
	candidate        *pendingStartupV1
	preparationFrame proto.Frame
	prepare          slotReservedStartupPrepareV2
}

// This is a separate incoming producer on the actual new creation's original
// startup lane. Its OP and reserved slot operands are not ordinary OP1 roles.
type slotReservedStartupIncomingV2 struct {
	mu               sync.Mutex
	owner            *OwnedSupervisorLease
	pending          *pendingStartupV1
	line             *StartupLineV1
	preparation      *slotReservedStartupPendingV2
	preparationFrame proto.Frame
	loanFrame        proto.Frame
	sender           startupCredentials
	files            []*os.File
	used             bool
}
