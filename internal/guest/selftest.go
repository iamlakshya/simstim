package guest

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/iamlakshya/simstim/internal/proto"
)

// selfTestEnv holds the kernel interfaces that wall-init --self-test uses, so
// unit tests can run on any OS without privilege or rebooting the machine.
type selfTestEnv struct {
	getpid      func() int
	prepareFS   func() error
	rootDev     func() (uint32, uint32, error)
	dm0BlockDev func() (uint32, uint32, error)
	sysDM0Dev   func() (string, error)
	powerOff    func() error
}

func defaultSelfTestEnv() selfTestEnv {
	return selfTestEnv{
		getpid:      os.Getpid,
		prepareFS:   prepareSelfTestFS,
		rootDev:     statRootDev,
		dm0BlockDev: statDM0BlockDev,
		sysDM0Dev: func() (string, error) {
			b, err := os.ReadFile("/sys/block/dm-0/dev")
			return strings.TrimSpace(string(b)), err
		},
		powerOff: powerOffGuest,
	}
}

func runSelfTest(out, errOut io.Writer) error {
	return runSelfTestWith(defaultSelfTestEnv(), out, errOut)
}

func runSelfTestWith(env selfTestEnv, out, errOut io.Writer) error {
	pid := env.getpid()
	if pid != 1 {
		return &proto.Error{
			Code:    "wall_init",
			Message: fmt.Sprintf("wall-init --self-test must run as PID 1, not PID %d", pid),
			Hint:    "Run just guest-boot to boot the guest init as PID 1 in a microvm.",
		}
	}
	err := checkRootOnDM0(env)
	if err != nil {
		fmt.Fprintln(errOut, "self-test failed:", err)
		_ = env.powerOff()
		return &proto.Error{
			Code:    "wall_init",
			Message: "wall-init --self-test failed: " + err.Error(),
			Hint:    "Boot with root=/dev/dm-0 and dm-mod.create for the tools image (spec 20.1).",
		}
	}
	fmt.Fprintln(out, "self-test ok")
	if err := env.powerOff(); err != nil {
		return &proto.Error{
			Code:    "wall_init",
			Message: "wall-init --self-test could not power off the guest: " + err.Error(),
			Hint:    "Check CONFIG_ACPI on amd64 and CONFIG_ARM_PSCI_FW on arm64.",
		}
	}
	return nil
}

func checkRootOnDM0(env selfTestEnv) error {
	if err := env.prepareFS(); err != nil {
		return err
	}
	rMaj, rMin, err := env.rootDev()
	if err != nil {
		return fmt.Errorf("stat /: %w", err)
	}
	dMaj, dMin, err := env.dm0BlockDev()
	if err != nil {
		return fmt.Errorf("stat /dev/dm-0: %w", err)
	}
	if rMaj != dMaj || rMin != dMin {
		return fmt.Errorf("/ is on %d:%d, not /dev/dm-0 (%d:%d)", rMaj, rMin, dMaj, dMin)
	}
	sysDev, err := env.sysDM0Dev()
	if err != nil {
		return fmt.Errorf("read /sys/block/dm-0/dev: %w", err)
	}
	want := fmt.Sprintf("%d:%d", dMaj, dMin)
	if sysDev != want {
		return fmt.Errorf("/sys/block/dm-0/dev is %q, want %q", sysDev, want)
	}
	return nil
}
