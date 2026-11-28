package process

import (
	"fmt"
	"strings"

	"github.com/iamlakshya/simstim/internal/brand"
)

const systemBwrapPath = "/usr/bin/bwrap"

// AppArmorFallbackCmd returns the one-time sudo command that installs and loads
// Ubuntu's distribution bubblewrap AppArmor profile (spec H7, decision 0053).
func AppArmorFallbackCmd() string {
	return aptInstallAndProfileCmd(false, false)
}

func missingBwrapFix(osInfo osReleaseInfo, lookPath func(string) (string, error), appArmorRestrict, hasProfile, hasExtraProfile bool) string {
	if appArmorRestrict && !hasProfile {
		if isAptDistro(osInfo, lookPath) {
			return aptInstallAndProfileCmd(true, hasExtraProfile)
		}
		return installBwrapCmd(osInfo, lookPath) + " (then load your distribution's AppArmor profile for " + systemBwrapPath + ")"
	}
	return installBwrapCmd(osInfo, lookPath)
}

func appArmorBwrapDiagnosis(st probeState, lookPath func(string) (string, error)) (reason, fix string) {
	if st.bwrapPath != "" && st.bwrapPath != systemBwrapPath {
		if !st.hasSystemBwrap {
			reason = fmt.Sprintf("AppArmor blocks %s; install the distribution bubblewrap package at %s", st.bwrapPath, systemBwrapPath)
			if st.hasProfile {
				return reason, installBwrapCmd(st.osInfo, lookPath)
			}
			if isAptDistro(st.osInfo, lookPath) {
				return reason, aptInstallAndProfileCmd(true, st.hasExtraProfile)
			}
			return reason, installBwrapCmd(st.osInfo, lookPath) + " (then load your distribution's AppArmor profile for " + systemBwrapPath + ")"
		}
		if st.hasProfile {
			reason = fmt.Sprintf("AppArmor blocks %s; the distribution profile covers %s", st.bwrapPath, systemBwrapPath)
			return reason, "sudo apparmor_parser -r " + st.profilePath
		}
	}
	reason = "Ubuntu's AppArmor user-namespace restriction blocks bubblewrap (kernel.apparmor_restrict_unprivileged_userns = 1)"
	switch {
	case st.profilePath != "":
		return reason, "sudo apparmor_parser -r " + st.profilePath
	case st.hasExtraProfile:
		return reason, fmt.Sprintf("sudo sh -c 'install -m 0644 %s %s && apparmor_parser -r %s'", appArmorBwrapExtra, appArmorBwrapRestrict, appArmorBwrapRestrict)
	case isAptDistro(st.osInfo, lookPath):
		return reason, aptInstallAndProfileCmd(false, false)
	default:
		return reason, "install your distribution's AppArmor profile for " + systemBwrapPath + " and reload it with sudo apparmor_parser -r"
	}
}

func aptInstallAndProfileCmd(needBwrap, hasExtraProfile bool) string {
	var pkgs []string
	if needBwrap {
		pkgs = append(pkgs, "bubblewrap")
	}
	if !hasExtraProfile {
		pkgs = append(pkgs, "apparmor-profiles", "apparmor-utils")
	}
	var steps []string
	if len(pkgs) > 0 {
		steps = append(steps, "apt-get update && apt-get install -y "+strings.Join(pkgs, " "))
	}
	steps = append(steps,
		fmt.Sprintf("install -m 0644 %s %s", appArmorBwrapExtra, appArmorBwrapRestrict),
		"apparmor_parser -r "+appArmorBwrapRestrict,
	)
	return "sudo sh -c '" + strings.Join(steps, " && ") + "'"
}

func installBwrapCmd(osInfo osReleaseInfo, lookPath func(string) (string, error)) string {
	switch {
	case osInfo.ID == "debian" || osInfo.ID == "ubuntu" || strings.Contains(osInfo.IDLike, "debian") || strings.Contains(osInfo.IDLike, "ubuntu"):
		return "sudo sh -c 'apt-get update && apt-get install -y bubblewrap'"
	case osInfo.ID == "fedora" || osInfo.ID == "rhel" || osInfo.ID == "centos" || osInfo.ID == "rocky" || osInfo.ID == "almalinux" || strings.Contains(osInfo.IDLike, "fedora") || strings.Contains(osInfo.IDLike, "rhel"):
		return "sudo dnf install bubblewrap"
	case osInfo.ID == "arch" || osInfo.ID == "manjaro" || strings.Contains(osInfo.IDLike, "arch"):
		return "sudo pacman -S bubblewrap"
	case osInfo.ID == "alpine":
		return "sudo apk add bubblewrap"
	case strings.Contains(osInfo.ID, "suse") || strings.Contains(osInfo.IDLike, "suse"):
		return "sudo zypper install bubblewrap"
	}
	for _, pm := range []struct{ bin, cmd string }{
		{"apt-get", "sudo sh -c 'apt-get update && apt-get install -y bubblewrap'"},
		{"dnf", "sudo dnf install bubblewrap"},
		{"pacman", "sudo pacman -S bubblewrap"},
		{"apk", "sudo apk add bubblewrap"},
		{"zypper", "sudo zypper install bubblewrap"},
	} {
		if hasBin(lookPath, pm.bin) {
			return pm.cmd
		}
	}
	return "install the bubblewrap package"
}

func userNSSysctlFix(key, val string) string {
	conf := "/etc/sysctl.d/50-" + brand.Name + "-userns.conf"
	return fmt.Sprintf("sudo sh -c 'printf \"%s = %s\\n\" > %s && sysctl -p %s'", key, val, conf, conf)
}

func isAptDistro(osInfo osReleaseInfo, lookPath func(string) (string, error)) bool {
	if osInfo.ID == "ubuntu" || osInfo.ID == "debian" || strings.Contains(osInfo.IDLike, "ubuntu") || strings.Contains(osInfo.IDLike, "debian") {
		return true
	}
	return osInfo.ID == "" && hasBin(lookPath, "apt-get")
}

func hasBin(lookPath func(string) (string, error), name string) bool {
	if lookPath == nil {
		return false
	}
	p, err := lookPath(name)
	return err == nil && p != ""
}
