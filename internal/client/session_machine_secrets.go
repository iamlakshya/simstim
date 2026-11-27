package client

// A machine's tier and secret modes remain restrictive after detach/resume.
// This check runs before a value command and never grants an exposure.
func checkSessionMachineSecretV1(h Host, settings Settings, name string, intent *wallGrantIntent) error {
	if !IsSessionMachineV1(h) {
		return nil
	}
	if err := checkSessionMachineSecretTierV1(h, settings, name); err != nil {
		return err
	}
	if intent == nil || !intent.exposed[name] {
		return &UserError{Msg: name + " can reach a provider machine only as an exposed secret", Hint: "Start a fresh new --machine e2b request with --expose " + name + ".", Code: "machine_secret_exposure", Kind: KindRefused}
	}
	return nil
}

func checkSessionMachineSecretTierV1(h Host, settings Settings, name string) error {
	if !IsSessionMachineV1(h) {
		return nil
	}
	minimum, err := minimumSecretTier(settings, name)
	if err != nil {
		return err
	}
	if minimum != TierShared {
		return &UserError{Msg: name + " is not marked for a shared provider machine", Hint: "Set [secret_tiers] " + name + " = \"shared\" only for a credential this machine may read.", Code: "tier_refused", Kind: KindRefused}
	}
	return nil
}
