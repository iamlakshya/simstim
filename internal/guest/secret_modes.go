package guest

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func memberSecretModesPresent(data []byte) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil {
		return false
	}
	for name := range fields {
		if strings.EqualFold(name, "secret_modes") {
			return true
		}
	}
	return false
}

// validateMemberSecretModeWire changes only explicit new-mode forms. Legacy
// decoding remains with its existing ordinary/VM decoder. New forms are closed
// before any Store or holder side effect, including null/case aliases.
func validateMemberSecretModeWire(data []byte, args proto.CtlStartArgs) error {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(data, &probe); err != nil {
		return guestSecretModeError()
	}
	present := false
	for name := range probe {
		present = present || strings.EqualFold(name, "secret_modes")
	}
	if !present {
		return nil
	}
	kind := reflect.TypeFor[proto.CtlStartArgs]()
	allowed := make([]string, 0, kind.NumField())
	allowedSet := make(map[string]bool, kind.NumField())
	for index := range kind.NumField() {
		name := strings.Split(kind.Field(index).Tag.Get("json"), ",")[0]
		if name != "" && name != "-" {
			allowed = append(allowed, name)
			allowedSet[name] = true
		}
	}
	fields, err := api.JSONObjectFields(data, allowed...)
	if err != nil || len(fields["secret_modes"]) == 0 || bytes.Equal(bytes.TrimSpace(fields["secret_modes"]), []byte("null")) {
		return guestSecretModeError()
	}
	for name := range fields {
		if !allowedSet[name] {
			return guestSecretModeError()
		}
	}
	// Dynamic environment names are case-sensitive data, not field aliases.
	// The common barrier checks duplicate map keys without case-folding them.
	if raw, exists := fields["secrets"]; exists && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		if _, err := api.JSONObjectFields(raw); err != nil {
			return guestSecretModeError()
		}
	}
	modes, err := api.JSONObjectFields(fields["secret_modes"])
	if err != nil || len(modes) != len(args.Secrets) || args.SecretModes == nil {
		return guestSecretModeError()
	}
	for name := range modes {
		if _, exists := args.Secrets[name]; !exists {
			return guestSecretModeError()
		}
	}
	_, err = memberBoundSentinels(args)
	return err
}

// memberBoundSentinels never projects exposed values into a Store environment.
// A cold bound sentinel can be present alongside its independent Missing label.
func memberBoundSentinels(args proto.CtlStartArgs) (map[string]api.Secret, error) {
	if args.SecretModes == nil {
		return nil, nil
	}
	if len(args.SecretModes) != len(args.Secrets) {
		return nil, guestSecretModeError()
	}
	bound := make(map[string]api.Secret)
	names := make(map[string]bool, len(args.Secrets)+len(args.Missing))
	for name, value := range args.Secrets {
		mode, ok := args.SecretModes[name]
		if !ok || !mode.Valid() || !secretStoreEnvName(name) || value == "" || strings.ContainsRune(value, 0) {
			return nil, guestSecretModeError()
		}
		names[name] = true
		if mode == api.SecretModeBound {
			bound[name] = api.Secret(value)
		}
	}
	for _, name := range args.Missing {
		if !secretStoreEnvName(name) {
			return nil, guestSecretModeError()
		}
		names[name] = true
	}
	for _, entry := range args.Env {
		name, _, _ := strings.Cut(entry, "=")
		if names[name] {
			return nil, &proto.Error{Code: "env_is_secret", Message: "environment variable " + name + " is a configured secret", Hint: "Remove that environment override. Use the approved secret delivery mode."}
		}
	}
	return bound, nil
}

func guestSecretModeError() error {
	return &proto.Error{Code: "bad_secret", Message: "the guest secret delivery modes are invalid or incomplete", Hint: "Update both helpers and start with one explicit supported mode per supplied secret."}
}
