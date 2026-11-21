package client

import "context"

// CheckAlertGrants checks the laptop's current policy before a front end
// runs a token command. The command itself and its output stay with the caller.
func CheckAlertGrants(ctx context.Context, h Host, cfg AlertsConfig, tokenCommand bool) error {
	tier, _, err := HostGrantTier(ctx, h)
	if err != nil {
		return err
	}
	return checkAlertGrants(h, tier, cfg, tokenCommand, LoadSettings())
}

// CheckAlertGrants checks the post-dial identity and policy before a front
// end runs a token command. SetAlerts checks them again before publication.
func (c *Conn) CheckAlertGrants(ctx context.Context, cfg AlertsConfig, tokenCommand bool) error {
	grants, classifyErr := alertTokenGrants(cfg, tokenCommand)
	ctx, release, err := c.beginGrant(ctx, "host alerts set", len(grants) > 0 || classifyErr != nil)
	if err != nil {
		return err
	}
	defer release()
	return CheckAlertGrants(ctx, c.Host, cfg, tokenCommand)
}

func checkAlertGrants(h Host, tier Tier, cfg AlertsConfig, tokenCommand bool, settings Settings) error {
	if IsSessionMachineV1(h) {
		tier = min(tier, TierShared)
	}
	grants, err := alertTokenGrants(cfg, tokenCommand)
	if err != nil {
		// An unreadable address may hide a capability. Refuse it below own
		// without including any part of the address in the error.
		if tier < TierOwn {
			if refusal := Allow(h, tier, Outbound{Kind: OutboundAlertToken}, settings); refusal != nil {
				return refusal
			}
		}
		return err
	}
	if err := ValidateSettings(settings); err != nil {
		return err
	}
	for _, grant := range grants {
		if err := Allow(h, tier, grant, settings); err != nil {
			return err
		}
	}
	return nil
}

func alertTokenGrants(cfg AlertsConfig, tokenCommand bool) ([]Outbound, error) {
	seen := map[string]bool{}
	var grants []Outbound
	for _, d := range cfg.Destinations {
		pending := ""
		if tokenCommand && d.Type == "ntfy" {
			pending = "pending"
		}
		label, credential, err := AlertCredentialKind(d, pending)
		if err != nil {
			return nil, err
		}
		if credential && !seen[label] {
			seen[label] = true
			grants = append(grants, Outbound{Kind: OutboundAlertToken, Name: label})
		}
	}
	return grants, nil
}
