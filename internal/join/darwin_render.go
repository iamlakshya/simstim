package join

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"path"
	"strconv"
	"strings"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/join/code"
)

const macFactsAction = "facts-v1"

func macRootBase() string              { return path.Join("/private/var/db", brand.Name, "join") }
func macJoinDir(account string) string { return path.Join(macRootBase(), account) }
func macHelperPath(account string) string {
	return path.Join("/usr/local/libexec", brand.Name, "mac", account, "client")
}
func macFactsDispatcher(account string) string {
	return path.Join("/usr/local/libexec", brand.Name, "mac", account, "facts")
}

func macKeyLine(p *DarwinPlan, port uint16) (string, error) {
	if p == nil || !code.ValidAccount(p.account) || port == 0 {
		return "", macFailure("join_mac_key", "the outer key has no original checked plan")
	}
	return fmt.Sprintf("restrict,port-forwarding,permitopen=\"127.0.0.1:%d\",permitlisten=\"127.0.0.1:%d\",command=\"%s\" %s\n", port, port, macFactsDispatcher(p.account), p.invite.PublicKey), nil
}

func macDropIn(account string, port uint16) (string, error) {
	if !code.ValidAccount(account) || port == 0 {
		return "", macFailure("join_mac_sshd", "the Mac SSH policy needs its checked account and VM port")
	}
	var out strings.Builder
	fmt.Fprintf(&out, "Match User %s\n", account)
	for _, item := range macSSHValues(port) {
		fmt.Fprintf(&out, "    %s %s\n", item[0], item[1])
	}
	fmt.Fprintf(&out, "    ForceCommand %s\nMatch all\n", macFactsDispatcher(account))
	return out.String(), nil
}

func macSSHValues(port uint16) [][2]string {
	return [][2]string{
		{"AuthenticationMethods", "publickey"}, {"AllowTcpForwarding", "local"},
		{"AllowStreamLocalForwarding", "no"}, {"PermitListen", "none"},
		{"PermitOpen", "127.0.0.1:" + strconv.Itoa(int(port))}, {"PermitTTY", "no"},
		{"AllowAgentForwarding", "no"}, {"X11Forwarding", "no"},
		{"PermitTunnel", "no"}, {"PermitUserRC", "no"},
		{"PasswordAuthentication", "no"}, {"KbdInteractiveAuthentication", "no"},
	}
}

func verifyMacSSHD(text, account string, port uint16) error {
	found := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		key := strings.ToLower(fields[0])
		if _, duplicate := found[key]; duplicate {
			return macFailure("join_mac_sshd", "sshd returned duplicate effective settings")
		}
		found[key] = strings.Join(fields[1:], " ")
	}
	for _, expected := range append(macSSHValues(port), [2]string{"ForceCommand", macFactsDispatcher(account)}) {
		if found[strings.ToLower(expected[0])] != expected[1] {
			return &code.Error{Code: "join_mac_sshd", Message: "the Mac's effective sshd setting " + expected[0] + " differs from the narrow join policy", Hint: "Ask the Mac's owner or MDM administrator to check earlier sshd rules, then run host join again."}
		}
	}
	return nil
}

func macPlist(account, instance, limaBinary, limaHome, workingDir, mode string, uid int) ([]byte, error) {
	if !code.ValidAccount(account) || instance != account+"-host" || !path.IsAbs(limaBinary) || !path.IsAbs(limaHome) || !path.IsAbs(workingDir) || uid <= 0 || mode != "daemon" && mode != "user-agent" {
		return nil, macFailure("join_mac_launch", "the Mac startup job has no fixed original VM inputs")
	}
	quote := func(value string) string {
		var b bytes.Buffer
		_ = xml.EscapeText(&b, []byte(value))
		return b.String()
	}
	var out strings.Builder
	out.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n<plist version=\"1.0\"><dict>\n")
	label := "io.lima-vm.daemon." + instance
	if mode == "user-agent" {
		label = "io.lima-vm.autostart." + instance
	}
	fmt.Fprintf(&out, "<key>Label</key><string>%s</string>\n", quote(label))
	if mode == "daemon" {
		fmt.Fprintf(&out, "<key>UserName</key><string>%s</string>\n", quote(account))
	}
	fmt.Fprintf(&out, "<key>EnvironmentVariables</key><dict><key>LIMA_HOME</key><string>%s</string><key>PATH</key><string>/usr/bin:/bin:/usr/sbin:/sbin</string></dict>\n", quote(limaHome))
	fmt.Fprintf(&out, "<key>ProgramArguments</key><array><string>%s</string><string>host</string><string>_mac-vm-start</string><string>--account</string><string>%s</string></array>\n", quote(macHelperPath(account)), quote(account))
	fmt.Fprintf(&out, "<key>WorkingDirectory</key><string>%s</string>\n", quote(workingDir))
	out.WriteString("<key>RunAtLoad</key><true/><key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict><key>ExitTimeOut</key><integer>60</integer><key>ProcessType</key><string>Background</string>\n</dict></plist>\n")
	return []byte(out.String()), nil
}

func macDispatcherScript(account string) []byte {
	return []byte("#!/bin/sh\nset -eu\n[ \"${SSH_ORIGINAL_COMMAND-}\" = '" + macFactsAction + "' ] || { printf '%s\\n' 'This key permits facts-v1 and the VM channel only.' >&2; exit 126; }\nexec '" + macHelperPath(account) + "' host _mac-facts --account '" + account + "'\n")
}

func macHasSSHDInclude(text string) bool {
	inMatch := false
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(strings.SplitN(line, "#", 2)[0])
		if len(fields) == 0 {
			continue
		}
		if strings.EqualFold(fields[0], "Match") {
			inMatch = !(len(fields) == 2 && strings.EqualFold(fields[1], "all"))
			continue
		}
		if inMatch || !strings.EqualFold(fields[0], "Include") {
			continue
		}
		for _, value := range fields[1:] {
			switch strings.Trim(value, `"`) {
			case "/etc/ssh/sshd_config.d/*", "/etc/ssh/sshd_config.d/*.conf", "/private/etc/ssh/sshd_config.d/*", "/private/etc/ssh/sshd_config.d/*.conf":
				return true
			}
		}
	}
	return false
}
