package chv

import "github.com/iamlakshya/simstim/internal/vmneed"

const mebibyte = vmneed.Mebibyte

// MemoryBudget is the canonical guest RAM, reserve and unit-cap tuple.
type MemoryBudget = vmneed.MemoryBudget

// BudgetFor delegates to the one canonical VM memory calculation.
func BudgetFor(guest int64) (MemoryBudget, error) { return vmneed.BudgetFor(guest) }
