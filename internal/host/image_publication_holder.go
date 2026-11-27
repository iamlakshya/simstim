package host

import (
	"context"
	"os"
	"os/exec"

	"github.com/iamlakshya/simstim/internal/publicationlease"
)

type publicationHolderContextV1 struct{}

// Only the concrete original converter guardian captures the fixed private
// handoff. It marks its duplicate CLOEXEC before any busctl/systemctl child.
func capturePublicationGuardianV1(ctx context.Context) (context.Context, func() error, error) {
	holder, err := publicationlease.CaptureGuardianV1()
	if err != nil {
		return nil, nil, err
	}
	return context.WithValue(ctx, publicationHolderContextV1{}, holder), holder.CloseV1, nil
}

// appendPublicationGuardianV1 fills the exact fixed slot in the concrete
// guardian's actual ExtraFiles. Intermediate holes explicitly become closed
// child slots. The caller retains this same OFD until native creation/join.
func appendPublicationGuardianV1(command *exec.Cmd, file *os.File) error {
	index := publicationlease.GuardianFDV1 - 3
	if command == nil || file == nil || command.Process != nil || len(command.ExtraFiles) > index {
		return placementError("the original guardian lost its private fence slot")
	}
	files := make([]*os.File, index+1)
	copy(files, command.ExtraFiles)
	files[index] = file
	command.ExtraFiles = files
	return nil
}
