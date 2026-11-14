package helpers

import "github.com/iamlakshya/simstim/internal/supply"

// Manifest returns this build's release manifest, or the fetched helper bundle's.
func Manifest() ([]byte, bool) { return read(supply.FileName) }
