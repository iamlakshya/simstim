package vmneed

import (
	"fmt"

	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/supply"
)

// KernelCommandLine gives the common two-table dm-verity boot command. The
// caller has already validated the complete canonical runtime and image.
// No guest-provided string supplies a table or selects a host path.
func KernelCommandLine(tools supply.Item, root image.Request) string {
	return fmt.Sprintf("ro quiet root=/dev/dm-0 rootfstype=erofs init=/init dm-mod.waitfor=/dev/vda,/dev/vdb dm-mod.create=\"tools,,0,ro,0 %d verity 1 /dev/vda /dev/vda 4096 4096 %d %d sha256 %s %s 1 panic_on_corruption;image,,1,ro,0 %d verity 1 /dev/vdb /dev/vdb 4096 4096 %d %d sha256 %s %s 1 panic_on_corruption\" -- wall-init --microvm-v1", tools.DataBlocks*8, tools.DataBlocks, tools.DataBlocks, tools.RootHash, tools.Salt, root.DataBlocks*8, root.DataBlocks, root.DataBlocks+1, root.RootHash, tools.Salt)
}
