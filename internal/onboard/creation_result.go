package onboard

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/provider"
)

func validateCreationResult(p provider.Provider, req provider.Request, actual client.Host) error {
	valid := actual.Name == req.Name && actual.Machine != nil
	if valid {
		machine := actual.Machine
		valid = machine.Provider == p.Name() && machine.ID != "" && strings.TrimSpace(machine.ID) == machine.ID && !strings.ContainsFunc(machine.ID, unicode.IsControl)
		if named[p.Name()] {
			valid = valid && machine.ID == provider.MachineName(req.Name)
		}
	}
	if valid {
		return nil
	}
	reported := "no machine identity"
	if actual.Machine != nil {
		reported = client.CleanLine(actual.Machine.Provider) + "/" + client.CleanLine(actual.Machine.ID)
	}
	return &client.UserError{Msg: fmt.Sprintf("the %s creation reply named host %q and %q instead of the requested host %q and its exact machine, so it was not saved", client.CleanLine(p.Name()), client.CleanLine(actual.Name), reported, req.Name),
		Hint: "Inspect the actual provider operation and any created resource before retrying. The unverified reply did not authorise automatic deletion.", Code: "provider_identity", Kind: client.KindRefused}
}
