package host

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/jail"
)

// RunImageGuardian attaches its own live PID before creating a jail. The
// inherited descriptors retain their order: layers, helper, fresh output.
func RunImageGuardian(args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("image guardian needs a descriptor count and scope name")
	}
	count, err := strconv.Atoi(args[0])
	saved := args[0] == savedRootConverterMode
	if saved {
		count, err = 0, nil
	}
	if err != nil || count < 0 || count > image.MaxLayers {
		return fmt.Errorf("invalid image descriptor count")
	}
	unit := args[1]
	if err := validateConverterUnit(unit); err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer cancel()
	ctx, closeFence, err := capturePublicationGuardianV1(ctx)
	if err != nil {
		return err
	}
	defer closeFence()
	files := make([]*os.File, count+2)
	for i := range files {
		files[i] = os.NewFile(uintptr(3+i), "image-inherited")
		defer files[i].Close()
	}
	// No scope subprocess may inherit the original private layer/helper/output
	// capabilities. The jail receives them only through its explicit Files.
	if err := markImageGuardianPrivateV1(files); err != nil {
		return err
	}
	var scope imageScopeInfo
	if saved {
		return serveSavedRootGuardian(ctx, unit, files[0], files[1], os.Stdin, os.Stdout)
	}
	return imageGuardian(ctx, files, unit, func(ctx context.Context, unit string) error {
		var err error
		scope, err = imageAttachScope(ctx, unit)
		return err
	}, func(ctx context.Context, spec jail.Spec) error { return runImageGuardianJail(ctx, spec, scope) })
}

func imageGuardian(ctx context.Context, files []*os.File, unit string, attach func(context.Context, string) error, start func(context.Context, jail.Spec) error) error {
	if err := attach(ctx, unit); err != nil {
		return err
	}
	count := len(files) - 2
	spec, err := converterSpec([]string{fmt.Sprintf("/proc/self/fd/%d", 3+count), "image-convert", strconv.Itoa(count)}, files)
	if err != nil {
		return err
	}
	return start(ctx, spec)
}

func imageAttachScope(ctx context.Context, unit string) (imageScopeInfo, error) {
	operation, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	command := exec.CommandContext(operation, "busctl", "--user", "call", "org.freedesktop.systemd1", "/org/freedesktop/systemd1", "org.freedesktop.systemd1.Manager", "StartTransientUnit", "ssa(sv)a(sa(sv))", unit, "fail", "4", "PIDs", "au", "1", strconv.Itoa(os.Getpid()), "MemoryMax", "t", "1073741824", "Slice", "s", brand.Slice, "CollectMode", "s", "inactive", "0")
	if data, err := command.CombinedOutput(); err != nil {
		return imageScopeInfo{}, UserError("image_scope", "the converter's 1 GiB scope could not start: "+string(data), "Check the systemd user manager with host doctor.")
	}
	scope, err := imageScopeProperties(operation, unit)
	if err != nil {
		return imageScopeInfo{}, err
	}
	cgroups, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return imageScopeInfo{}, fmt.Errorf("read image guardian's cgroup: %w", err)
	}
	ownGroup := ""
	for _, line := range strings.Split(string(cgroups), "\n") {
		if group, ok := strings.CutPrefix(line, "0::"); ok {
			ownGroup = group
		}
	}
	if scope.ActiveState != "active" || scope.MemoryMax != "1073741824" || scope.ControlGroup == "" || scope.ControlGroup != ownGroup || !validImageInvocation(scope.InvocationID) {
		return imageScopeInfo{}, UserError("image_scope", "the image guardian did not enter its verified 1 GiB scope", "Check the systemd user manager with host doctor.")
	}
	return scope, nil
}
