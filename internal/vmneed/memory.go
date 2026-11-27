// Package vmneed holds the canonical typed VM Need and its strict codec.
package vmneed

import (
	"fmt"
	"math"
)

// Mebibyte is the byte unit of the canonical VM memory calculation.
const Mebibyte int64 = 1 << 20

// MemoryBudget separates the guest's requested RAM from the host unit's cap.
type MemoryBudget struct {
	Guest   int64 `json:"guest"`
	Reserve int64 `json:"reserve"`
	Limit   int64 `json:"limit"`
}

// BudgetFor returns the exact reserve of isolation spec 20.10. It never lowers
// guest RAM to pay for the VMM, page tables, sealed copies or host processes.
func BudgetFor(guest int64) (MemoryBudget, error) {
	if guest <= 0 || guest%Mebibyte != 0 {
		return MemoryBudget{}, fmt.Errorf("guest memory must be a positive whole MiB")
	}
	pages := guest / (256 * Mebibyte)
	if guest%(256*Mebibyte) != 0 {
		pages++
	}
	reserve := (128 + pages) * Mebibyte
	if guest > math.MaxInt64-reserve {
		return MemoryBudget{}, fmt.Errorf("guest memory and its reserve overflow the unit cap")
	}
	return MemoryBudget{Guest: guest, Reserve: reserve, Limit: guest + reserve}, nil
}
