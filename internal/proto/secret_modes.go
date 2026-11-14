package proto

import (
	"encoding/json"

	"github.com/iamlakshya/simstim/internal/secretmode"
)

// MarshalJSON preserves explicit empty new-form metadata. Omitting {} would
// erase the capability barrier and make it indistinguishable from legacy.
func (args CtlStartArgs) MarshalJSON() ([]byte, error) {
	type plain CtlStartArgs
	if args.SecretModes == nil {
		return json.Marshal(plain(args))
	}
	return json.Marshal(struct {
		plain
		SecretModes map[string]secretmode.SecretMode `json:"secret_modes"`
	}{plain: plain(args), SecretModes: args.SecretModes})
}
