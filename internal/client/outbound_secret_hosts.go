package client

import (
	"fmt"
	"slices"
)

// Presence is meaningful in the canonical owned view: absent adds no
// restriction; present empty denies every host. Use the actual saved label,
// never its SSH destination, display alias or a case-folded replacement.
func allowSecretOnHost(h Host, name string, view SecretConfigView) error {
	hosts, restricted := view.OnHosts[name]
	if !restricted || slices.Contains(hosts, h.Name) {
		return nil
	}
	return &UserError{Msg: fmt.Sprintf("%s is not allowed on the saved host %s", name, hostDisplayName(h)),
		Hint: "Choose a saved host listed in this secret's on_hosts, or update that list in config.toml.",
		Code: "secret_not_here", Kind: KindRefused}
}
