package client

import (
	"context"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/macfacts"
)

// appendJoinedMacFactsV1 uses H20's SAME live connection and original saved
// source. No fresh name lookup can adopt a removed and re-added host here.
func appendJoinedMacFactsV1(ctx context.Context, c *Conn, facts *api.HostFacts) {
	if c == nil || c.Host.JoinedSSH == nil {
		return
	}
	data, err := ReadJoinedMacFactsV1(ctx, c)
	if err != nil {
		facts.OnBatteryKnown = false
		facts.Health = append(facts.Health, api.Health{ID: "outer-facts-unknown", Severity: "warn", Message: "The original outer host's Mac facts could not be read.", Hint: "Run host doctor and check the original joined SSH path."})
		return
	}
	if len(data) == 0 {
		return
	}
	mac, err := macfacts.Decode(data)
	if err != nil {
		facts.OnBatteryKnown = false
		facts.Health = append(facts.Health, api.Health{ID: "mac-facts-unknown", Severity: "warn", Message: "The Mac returned unsupported or incomplete facts.", Hint: "Update the Mac join's original fixed facts helper."})
		return
	}
	facts.Mac = &mac
	facts.OnBattery, facts.OnBatteryKnown = mac.OnBattery, mac.OnBatteryKnown
	facts.Health = append(facts.Health, macHealthV1(mac)...)
}

func macHealthV1(mac macfacts.Facts) []api.Health {
	var health []api.Health
	add := func(id, message, hint string) {
		health = append(health, api.Health{ID: id, Severity: "warn", Message: message, Hint: hint})
	}
	if mac.OnBatteryKnown && mac.OnBattery {
		add("mac-on-battery", "The Mac host is on battery power.", "Connect the Mac to AC power and keep its lid open.")
	}
	if mac.ACSleepKnown && mac.ACSleepMinutes != 0 {
		add("mac-sleep-on", "The Mac can sleep while connected to AC power.", "Review host join's optional AC-only sleep change on the Mac.")
	}
	if mac.StartMode == "user-agent" {
		add("mac-user-agent", "The VM runs in the Mac user's login session; anyone at its unlocked screen can read the VM disk.", "Keep that user logged in and lock the Mac screen.")
	}
	if mac.LaunchKnown && !mac.LaunchEnabled {
		add("mac-startup-off", "The Mac's original VM startup job is not loaded.", "Check System Settings > General > Login Items & Extensions on the Mac.")
	}
	if mac.SSHDManagedKnown && mac.SSHDManaged {
		add("mac-managed-sshd", "The Mac has managed sshd settings.", "Ask the organisation's IT team to verify the narrow joined-account policy.")
	}
	if mac.ThermalKnown && mac.ThermalWarning {
		add("mac-thermal", "The Mac reports a thermal warning or CPU limit.", "Keep its lid open and provide good air flow.")
	}
	return health
}

func macDoctorChecksV1(mac macfacts.Facts) []Check {
	power := "power source unknown"
	if mac.OnBatteryKnown {
		power = "AC power"
		if mac.OnBattery {
			power = "battery power"
		}
	}
	mode := "startup mode unknown"
	if mac.StartMode == "daemon" {
		mode = "system launch daemon"
	}
	if mac.StartMode == "user-agent" {
		mode = "user launch agent"
	}
	return []Check{{Name: "Mac host", OK: mac.MacOS != "" && mac.Chip != "", Detail: CleanLine("macOS " + mac.MacOS + ", " + mac.Chip + ", " + power + ", " + mode), Fix: "Keep the Mac on, open its lid, lock its screen and log in locally after a restart."}}
}

// MacLeaveNoticeV1 inspects only the original joined outer route before guest
// purge. The message grants no cleanup, root or source authority on the Mac.
func (c *Conn) MacLeaveNoticeV1(ctx context.Context) (string, error) {
	if c == nil || c.Host.JoinedSSH == nil {
		return "", nil
	}
	data, err := ReadJoinedMacFactsV1(ctx, c)
	if err != nil {
		return "", err
	}
	if len(data) == 0 {
		return "", nil
	}
	if _, err := macfacts.Decode(data); err != nil {
		return "", err
	}
	return "Run sudo " + brand.Name + " host leave on the Mac to remove its dedicated account and VM.", nil
}
