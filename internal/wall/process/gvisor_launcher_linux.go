//go:build linux

package process

import (
	"crypto/sha256"
	"debug/elf"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"golang.org/x/sys/unix"

	"github.com/iamlakshya/simstim/internal/brand"
)

// OpenGvisorOuterLauncherV1 selects the same static/AppArmor launcher as the
// process wall and returns its pinned current executable. It supplies public
// bootstrap code only; the original jail constructor owns all private grants.
func OpenGvisorOuterLauncherV1(helperDir string) (*os.File, error) {
	selected, err := launcherPath(helperDir)
	if err != nil {
		return nil, err
	}
	launcher, err := openLauncherCodeV1(selected)
	if err != nil {
		return nil, setupError("cannot capture the fixed gvisor outer launcher", err)
	}
	current, err := openLauncherCodeV1(filepath.Join(helperDir, brand.UsernsBinary))
	if err != nil {
		return nil, setupError("cannot capture the current gvisor outer launcher", errors.Join(err, launcher.Close()))
	}
	actualHash, actualErr := launcherCodeHashV1(launcher)
	expectedHash, expectedErr := launcherCodeHashV1(current)
	closeErr := current.Close()
	if actualErr != nil || expectedErr != nil || closeErr != nil || actualHash != expectedHash {
		return nil, setupError("the selected gvisor outer launcher is not the current helper version", errors.Join(actualErr, expectedErr, closeErr, launcher.Close()))
	}
	return launcher, nil
}

func openLauncherCodeV1(name string) (*os.File, error) {
	pinned, err := pinSource(name)
	if err != nil {
		return nil, err
	}
	before, statErr := pinned.Stat()
	descriptor, openErr := unix.Open("/proc/self/fd/"+strconv.Itoa(int(pinned.Fd())), unix.O_RDONLY|unix.O_CLOEXEC, 0)
	closeErr := pinned.Close()
	if openErr != nil {
		return nil, errors.Join(statErr, openErr, closeErr)
	}
	code := os.NewFile(uintptr(descriptor), "fixed-native-launcher")
	after, err := code.Stat()
	if statErr != nil || closeErr != nil || err != nil || !os.SameFile(before, after) || !after.Mode().IsRegular() || after.Mode().Perm()&0111 == 0 || after.Mode()&(os.ModeSetuid|os.ModeSetgid) != 0 {
		return nil, errors.Join(fmt.Errorf("the pinned launcher lost its executable identity"), statErr, closeErr, err, code.Close())
	}
	parsed, err := elf.NewFile(code)
	if err != nil {
		return nil, errors.Join(err, code.Close())
	}
	for _, segment := range parsed.Progs {
		if segment.Type == elf.PT_INTERP || segment.Type == elf.PT_DYNAMIC {
			return nil, errors.Join(fmt.Errorf("the pinned launcher is not static"), code.Close())
		}
	}
	return code, nil
}

func launcherCodeHashV1(file *os.File) ([32]byte, error) {
	var result [32]byte
	before, err := file.Stat()
	if err != nil {
		return result, err
	}
	digest := sha256.New()
	count, err := io.Copy(digest, io.NewSectionReader(file, 0, before.Size()))
	after, statErr := file.Stat()
	if err != nil || statErr != nil || count != before.Size() || after.Size() != before.Size() || !os.SameFile(before, after) {
		return result, errors.Join(err, statErr, fmt.Errorf("the pinned launcher changed during current-version validation"))
	}
	copy(result[:], digest.Sum(nil))
	return result, nil
}
