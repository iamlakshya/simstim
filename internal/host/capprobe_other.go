//go:build !linux

package host

import (
	"os/user"

	"github.com/iamlakshya/simstim/internal/api"
)

func probeKVM(int, *user.User) api.KVM        { return api.KVM{} }
func probeDiskFS(string) string               { return "" }
func probeReflink(string) bool                { return false }
func readRootOwnedFile(string) ([]byte, bool) { return nil, false }
