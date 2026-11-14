package supply

import (
	"errors"
	"fmt"
	"math"
	"path"
	"strings"
)

// validateBootItem checks the release asset names and dm-verity layout of
// the boot pair specified in isolation spec 20.1.
func (it Item) validateBootItem() error {
	switch it.Kind {
	case KindKernel:
		if it.Licence != "GPL-2.0-only" {
			return errors.New("a kernel needs licence GPL-2.0-only")
		}
		if path.Base(it.URL) != KernelAsset(it.Arch) {
			return fmt.Errorf("a kernel for %s needs asset %s", it.Arch, KernelAsset(it.Arch))
		}
		if path.Base(it.SourceURL) != KernelSourceAsset(it.Version) {
			return fmt.Errorf("a kernel at %s needs source asset %s", it.Version, KernelSourceAsset(it.Version))
		}
	case KindTools:
		if path.Base(it.URL) != ToolsAsset(it.Arch) {
			return fmt.Errorf("tools for %s need asset %s", it.Arch, ToolsAsset(it.Arch))
		}
		return it.validateTools()
	}
	return nil
}

func (it Item) validateTools() error {
	switch {
	case !hex64.MatchString(it.RootHash):
		return fmt.Errorf("root_hash %q is not 64 lower-case hex digits", it.RootHash)
	case it.Salt != strings.Repeat("0", 64):
		return errors.New("salt must be 64 zero hex digits")
	case it.DataBlocks <= 0:
		return errors.New("no data_blocks")
	case it.DataBlocks > math.MaxInt64/4096:
		return errors.New("data_blocks exceeds the supported byte range")
	case it.Size%4096 != 0:
		return errors.New("tools size is not a multiple of 4096 bytes")
	case it.DataBlocks*4096 >= it.Size:
		return errors.New("tools size has no room for its verity tree after the data blocks")
	}
	return nil
}
