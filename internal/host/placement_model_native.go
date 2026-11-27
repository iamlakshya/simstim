package host

import (
	"os"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/cachefs"
)

func apiPurgeRef(ref purgeRef) api.PurgeNativeReference {
	return api.PurgeNativeReference{Device: ref.Device, Inode: ref.Inode, UID: ref.UID, Mode: ref.Mode, Size: ref.Size, Modified: ref.ModTime}
}

func nativePurgeRef(ref api.PurgeNativeReference) purgeRef {
	return purgeRef{Device: ref.Device, Inode: ref.Inode, UID: ref.UID, Mode: ref.Mode, Size: ref.Size, ModTime: ref.Modified}
}

func nativeReferenceEqual(a, b api.PurgeNativeReference) bool {
	return a.Device == b.Device && a.Inode == b.Inode && a.UID == b.UID && a.Mode == b.Mode && a.Size == b.Size && a.Modified.Equal(b.Modified)
}

func terminalStagesComplete(stages []string) bool {
	return slices.Equal(stages, []string{"precheck", "sessions", "footprints", "roots", "helpers", "host-complete"})
}

func syncCapturedDirectory(root *os.Root) error {
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	return cachefs.SyncDirectory(dir)
}
