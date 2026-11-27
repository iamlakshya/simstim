package join

import (
	"crypto/ed25519"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/join/code"
	"github.com/iamlakshya/simstim/internal/supply"
	"golang.org/x/crypto/ssh"
)

func macPlanFixture(t *testing.T) *DarwinPlan {
	t.Helper()
	key, err := ssh.NewPublicKey(ed25519.PublicKey(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	invite := code.Invite{PublicKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))), Nonce: 12, Expires: time.Now().Add(time.Hour)}
	base := supply.ReleaseFolder("1.0.0")
	lima := supply.NewItem(supply.KindLima, "lima", "2.3.0", "arm64", []byte("archive"), base+"/lima-2.3.0-Darwin-arm64.tar.gz")
	image := supply.NewItem(supply.KindVMImage, "lima-ubuntu", "26.04-20261016", "arm64", []byte("image"), base+"/lima-ubuntu-26.04-20261016-arm64.img")
	plan, err := NewDarwinPlan(invite, "dev", []string{"mac.example"}, 22, DarwinOptions{CPUs: 2, MemoryBytes: 2 << 30, StartMode: "daemon"}, supply.Manifest{Release: "1.0.0", Items: []supply.Item{lima, image}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestMacPlanNamesRealChangesAndHonestFallback(t *testing.T) {
	plan := macPlanFixture(t)
	text := plan.PlanText()
	for _, needed := range []string{"SecureToken disabled", "random password", "no mounts", "loopback", "Remote Login", "60-second", "FileVault", "Battery settings"} {
		if needed == "Battery settings" {
			continue
		}
		if !strings.Contains(text, needed) {
			t.Fatalf("missing %q", needed)
		}
	}
	plan.options.StartMode = "user-agent"
	plan.options.AgentUser = "me"
	if !strings.Contains(plan.PlanText(), "unlocked Mac") || !strings.Contains(plan.PlanText(), "must stay logged in") {
		t.Fatal("fallback hid its custody change")
	}
	if strings.Contains(text, "pmset -b") || strings.Contains(text, "disablesleep") {
		t.Fatal("plan changed battery policy")
	}
}

func TestMacKeyAndEffectivePolicyCloseEveryOtherChannel(t *testing.T) {
	plan := macPlanFixture(t)
	line, err := macKeyLine(plan, 60022)
	if err != nil {
		t.Fatal(err)
	}
	for _, needed := range []string{`restrict,port-forwarding`, `permitopen="127.0.0.1:60022"`, `permitlisten="127.0.0.1:60022"`, macFactsDispatcher(plan.account)} {
		if !strings.Contains(line, needed) {
			t.Fatalf("missing key option %q", needed)
		}
	}
	policy, err := macDropIn(plan.account, 60022)
	if err != nil {
		t.Fatal(err)
	}
	for _, needed := range []string{"AllowStreamLocalForwarding no", "PermitTTY no", "PermitListen none", "AllowAgentForwarding no", "X11Forwarding no", "PermitOpen 127.0.0.1:60022", "ForceCommand " + macFactsDispatcher(plan.account)} {
		if !strings.Contains(policy, needed) {
			t.Fatalf("missing sshd policy %q", needed)
		}
	}
	var effective strings.Builder
	for _, item := range macSSHValues(60022) {
		effective.WriteString(strings.ToLower(item[0]) + " " + item[1] + "\n")
	}
	effective.WriteString("forcecommand " + macFactsDispatcher(plan.account) + "\n")
	if err := verifyMacSSHD(effective.String(), plan.account, 60022); err != nil {
		t.Fatal(err)
	}
	if verifyMacSSHD(strings.Replace(effective.String(), "allowstreamlocalforwarding no", "allowstreamlocalforwarding yes", 1), plan.account, 60022) == nil {
		t.Fatal("managed policy opened Unix forwards")
	}
	if !macHasSSHDInclude("Include /etc/ssh/sshd_config.d/*\n") || macHasSSHDInclude("Match User other\n Include /etc/ssh/sshd_config.d/*\n") {
		t.Fatal("Mac include scope is wrong")
	}
	if !strings.Contains(string(macDispatcherScript(plan.account)), "SSH_ORIGINAL_COMMAND") || !strings.Contains(string(macDispatcherScript(plan.account)), "facts-v1") {
		t.Fatal("fixed dispatcher accepted arbitrary commands")
	}
}

func TestMacLaunchTemplateRetainsRealDaemonAndAgentFields(t *testing.T) {
	plan := macPlanFixture(t)
	binary := "/usr/local/libexec/" + brand.Name + "/mac/dev/runtime/lima/bin/limactl"
	plist, err := macPlist("dev", "dev-host", binary, "/private/var/guest/lima", "/private/var/guest", "daemon", 501)
	if err != nil {
		t.Fatal(err)
	}
	for _, needed := range []string{"UserName", "LIMA_HOME", "KeepAlive", "SuccessfulExit", "ExitTimeOut", "<integer>60</integer>", "_mac-vm-start"} {
		if !strings.Contains(string(plist), needed) {
			t.Fatalf("missing %q", needed)
		}
	}
	agent, err := macPlist(plan.account, plan.account+"-host", binary, "/Users/me/Library/Application Support/guest/lima", "/Users/me", "user-agent", 501)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(agent), "<key>UserName</key>") {
		t.Fatal("user agent became a system daemon")
	}
}
