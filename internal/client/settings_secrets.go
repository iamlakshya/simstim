package client

import (
	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
)

// NormaliseSettingsSecrets validates the complete local secret configuration.
// It preserves read failures and checks every tier and unused definition.
// Its owned view is metadata only. It grants no host access and runs nothing.
func NormaliseSettingsSecrets(settings Settings) (SecretConfigView, error) {
	if settings.loadErr != nil {
		return SecretConfigView{}, settings.loadErr
	}
	if err := validateSecretTiers(settings); err != nil {
		return SecretConfigView{}, err
	}
	defaults, err := agents.GlobalSecretDefaults()
	if err != nil {
		return SecretConfigView{}, &UserError{Code: "bad_config", Kind: KindUsage,
			Msg:  "the local agent secret metadata cannot be used",
			Hint: "Update the client and check the local agent configuration."}
	}
	bindings := make(map[string]api.SecretBind, len(defaults))
	for name, metadata := range defaults {
		bindings[name] = metadata.Bind
	}
	return NormaliseSecretDefinitions(settings.Secrets, bindings)
}

// ConfiguredSecretNames returns every configured command name after complete
// settings validation. It fetches no values and applies no host or tier filter.
func ConfiguredSecretNames(settings Settings) (map[string]bool, error) {
	view, err := NormaliseSettingsSecrets(settings)
	if err != nil {
		return nil, err
	}
	names := make(map[string]bool, len(view.Commands))
	for name := range view.Commands {
		names[name] = true
	}
	return names, nil
}

func reservedSocketSecretError() *UserError {
	return &UserError{Msg: "the session sets SSH_AUTH_SOCK itself, so it cannot be a secret",
		Hint: "Use --ssh-agent for one unwalled session on a host you own.", Code: "bad_secret", Kind: KindUsage}
}
