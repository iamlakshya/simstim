package api

import (
	"fmt"
	"io"
	"slices"
)

// DecoyTripwire is a trusted start's fake credential, never a value which may
// be substituted, armed, granted or stored in the session's secret ledger.
type DecoyTripwire struct {
	Name   string   `json:"name"`
	Values []Secret `json:"values"`
	Hosts  []string `json:"hosts"`
}

// Format keeps generated values out of every formatted representation.
func (DecoyTripwire) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, Redacted) }

// CloneDecoyTripwires owns the values and host classes of a start generation.
func CloneDecoyTripwires(input []DecoyTripwire) []DecoyTripwire {
	output := slices.Clone(input)
	for index := range output {
		output[index].Values = slices.Clone(input[index].Values)
		output[index].Hosts = slices.Clone(input[index].Hosts)
	}
	return output
}
