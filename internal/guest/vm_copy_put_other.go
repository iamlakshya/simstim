//go:build !linux

package guest

import (
	"context"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

func (g *Init) ownedVMFilePut(context.Context, *link.Stream, proto.FilePutHeaderV1) (bool, error) {
	return false, nil
}
