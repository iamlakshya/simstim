package client

import "github.com/iamlakshya/simstim/internal/api"

// cleanNamedSSHKeyView owns only public display lists. A row cannot choose
// which local key a later original grant may inspect or fetch.
func cleanNamedSSHKeyView(view *api.NamedSSHKeyViewV1) *api.NamedSSHKeyViewV1 {
	if !view.Valid() {
		return nil
	}
	return api.CloneNamedSSHKeyViewV1(view)
}
