//go:build darwin

package macfacts

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Read executes only the fixed public observation commands. Failures keep
// their Known fields false; output never becomes a command, path or grant.
func Read(ctx context.Context) Facts {
	facts := Facts{Version: Version, Kind: "mac"}
	read := func(name string, args ...string) (string, bool) {
		limited, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		command := exec.CommandContext(limited, name, args...)
		command.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LC_ALL=C"}
		var output cappedOutput
		command.Stdout, command.Stderr = &output, &output
		if err := command.Run(); err != nil || output.overflow {
			return "", false
		}
		return strings.TrimSpace(string(output.data)), true
	}
	if text, ok := read("/usr/bin/sw_vers", "-productVersion"); ok && clean(text) {
		facts.MacOS = text
	}
	if text, ok := read("/usr/sbin/sysctl", "-n", "machdep.cpu.brand_string"); ok && clean(text) {
		facts.Chip = text
	}
	if text, ok := read("/usr/bin/pmset", "-g", "batt"); ok {
		facts.OnBattery, facts.OnBatteryKnown, facts.BatteryPercent, facts.BatteryKnown = ParseBattery(text)
	}
	if text, ok := read("/usr/bin/pmset", "-g", "custom"); ok {
		facts.ACSleepMinutes, facts.ACSleepKnown = ParseACSleep(text)
	}
	if text, ok := read("/usr/bin/profiles", "status", "-type", "enrollment"); ok {
		facts.MDMEnrolled, facts.MDMKnown = ParseMDM(text)
	}
	if text, ok := read("/usr/bin/pmset", "-g", "assertions"); ok {
		facts.SleepPrevented, facts.SleepAssertionsKnown = ParseSleepAssertions(text)
	}
	if text, ok := read("/usr/bin/pmset", "-g", "therm"); ok {
		facts.ThermalWarning, facts.ThermalKnown = ParseThermal(text)
	}

	// Only the presence of the fixed managed preference is observed. Never
	// enumerate configuration profiles, certificates, keys or their values.
	_, err := os.Lstat("/Library/Managed Preferences/com.apple.sshd.plist")
	facts.SSHDManaged, facts.SSHDManagedKnown = err == nil, err == nil || os.IsNotExist(err)
	return facts
}

type cappedOutput struct {
	data     []byte
	overflow bool
}

func (w *cappedOutput) Write(data []byte) (int, error) {
	if len(data) > MaxBytes-len(w.data) {
		w.overflow = true
		return 0, fmt.Errorf("the fixed Mac observation exceeds its output bound")
	}
	w.data = append(w.data, data...)
	return len(data), nil
}
