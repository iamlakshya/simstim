//go:build linux

package jail

import (
	"github.com/iamlakshya/simstim/internal/nitrodev"
	"os"
	"testing"
)

func TestSuppliedNitroDeviceRejectsForeignNativeDescriptor(t *testing.T) {
	file, err := os.OpenFile("/dev/null", os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	for _, target := range []string{nitrodev.Device, "/elsewhere"} {
		if _, err := checkSuppliedBindV1(int(file.Fd()), true, nitrodev.Device, target); err == nil {
			t.Fatal("a path label promoted another native device")
		}
	}
}
