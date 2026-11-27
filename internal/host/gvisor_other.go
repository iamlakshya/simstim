//go:build !linux

package host

import "github.com/iamlakshya/simstim/internal/wall"

// NewGvisorDriver requires the Linux native namespace and original boot owner.
func NewGvisorDriver() (wall.Driver, error) {
	return nil, UserError("wall_unavailable", "gvisor requires a Linux host", "Use a Linux host for this sandbox.")
}
