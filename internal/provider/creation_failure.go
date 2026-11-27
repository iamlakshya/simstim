package provider

import (
	"fmt"

	"github.com/iamlakshya/simstim/internal/client"
)

// CreationFailure retains the actual partial resource and original locator.
// It supplies no birth token, sharing permission or automatic delete recipe.
type CreationFailure struct {
	Host                client.Host
	ResourceID, Locator string
	Failure             *client.UserError
	Cause               error
}

// Error reports the retained or uncertain resource fate without raw metadata.
func (e *CreationFailure) Error() string { return e.Failure.Error() }

// Unwrap preserves both the public retry hint and the original failure.
func (e *CreationFailure) Unwrap() []error { return []error{e.Failure, e.Cause} }

// RetainedCreation preserves the actual partial result and original locator.
// It does not guess a deletion target or turn partial metadata into birth proof.
func RetainedCreation(host client.Host, resource, locator string, cause error) error {
	if host.Machine != nil {
		machine := *host.Machine
		machine.Source = client.CloneMachineSource(machine.Source)
		host.Machine = &machine
	}
	message := "the provider resource is retained, or its final creation state is uncertain"
	if host.Machine != nil {
		message = fmt.Sprintf("the %s resource %s is retained, or its final creation state is uncertain", client.CleanLine(host.Machine.Provider), client.CleanLine(host.Machine.ID))
	}
	hint := "Inspect the original provider resource before retrying. No automatic deletion was attempted after this failure."
	if locator != "" {
		hint = "Inspect the original provider locator " + client.CleanLine(locator) + " and resource before retrying. No automatic deletion was attempted after this failure."
	}
	return &CreationFailure{Host: host, ResourceID: resource, Locator: locator, Cause: cause, Failure: &client.UserError{Code: "provider_partial", Msg: message, Hint: hint, Kind: client.KindFailed}}
}
