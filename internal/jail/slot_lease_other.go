//go:build !linux

package jail

import (
	"context"
	"github.com/iamlakshya/simstim/internal/slotlease"
	"os"
)

func CommandSlotV1(context.Context, Spec, *slotlease.SourceV1) (*Cmd, error) {
	return nil, errLinuxOnly
}
func (*Cmd) RetainSlotInitV1(*slotlease.SourceV1) (*os.File, error) { return nil, errLinuxOnly }
