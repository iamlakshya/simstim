//go:build linux

package microvm

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/iamlakshya/simstim/internal/slotlaunch"
)

func (s *sandbox) startOriginalSlotSourceV1(ctx context.Context, request slotlaunch.Request, files *bootFiles) (*slotlaunch.NativeSourceV1, error) {
	held := []*os.File{files.vmm, files.kernel, files.tools, files.image, files.agents}
	required, selectionErr := s.driver.reservationSelectionV2(ctx)
	if selectionErr != nil {
		return nil, selectionErr
	}
	if callback := s.driver.options.ReservationV2; callback != nil {
		var source *slotlaunch.NativeSourceV1
		var producerErr error
		used := false
		provided, err := callback(ctx, func(original []*os.File) error {
			if used {
				return fmt.Errorf("the actual Cold/new-Boot reserved input was offered twice")
			}
			used = true
			source, producerErr = slotlaunch.StartReservedNativeV2(ctx, request, held, original)
			return producerErr
		})
		if err = errors.Join(err, producerErr); err != nil {
			return source, err
		}
		if provided != used {
			return source, fmt.Errorf("the original Cold reserved presence differed from its actual native input")
		}
		if used {
			if source == nil {
				return nil, fmt.Errorf("the actual reserved root producer returned no native owner")
			}
			return source, nil
		}
		if required {
			return nil, fmt.Errorf("the actual reserved Boot omitted its required original native operands")
		}
	}
	if s.driver.options.ForkDiskV1 == nil {
		return slotlaunch.StartNativeV1(ctx, request, held)
	}
	var source *slotlaunch.NativeSourceV1
	var producerErr error
	used := false
	provided, err := s.driver.options.ForkDiskV1(ctx, func(disk *os.File) error {
		if used {
			return fmt.Errorf("the original new Boot offered its fork disk more than once")
		}
		used = true
		source, producerErr = slotlaunch.StartForkNativeV1(ctx, request, held, disk)
		return producerErr
	})
	if err = errors.Join(err, producerErr); err != nil {
		return source, err
	}
	if provided != used {
		return source, fmt.Errorf("the original new Boot's fork-data presence differed from its actual borrowed descriptor")
	}
	if used {
		if source == nil {
			return nil, fmt.Errorf("the actual fork root producer returned no native owner")
		}
		return source, nil
	}
	return slotlaunch.StartNativeV1(ctx, request, held)
}
