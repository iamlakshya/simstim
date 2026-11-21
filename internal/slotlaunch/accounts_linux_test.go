//go:build linux

package slotlaunch

import (
	"fmt"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
)

func slotAccountFixture() (string, string) {
	var passwd, groups strings.Builder
	for i := range SlotCount {
		name := fmt.Sprintf("%s-s%02d", brand.Name, i)
		fmt.Fprintf(&passwd, "%s:x:%d:%d::/nonexistent:/usr/sbin/nologin\n", name, 21000+i, 23000+i)
		fmt.Fprintf(&groups, "%s:x:%d:\n", name, 23000+i)
	}
	return passwd.String(), groups.String()
}

func TestSlotAccountsRequireNamedPrivatePrimaryGroup(t *testing.T) {
	name := brand.Name + "-s00"
	for _, tc := range []struct {
		name   string
		change func(string, string) (string, string)
	}{
		{"missing primary row", func(p, g string) (string, string) {
			return p, strings.Replace(g, name+":x:23000:\n", "", 1)
		}},
		{"primary differs from named group without alias row", func(p, g string) (string, string) {
			return strings.Replace(p, ":21000:23000:", ":21000:22000:", 1), g
		}},
		{"duplicate named group", func(p, g string) (string, string) {
			return p, g + name + ":x:23000:\n"
		}},
		{"second differently numbered named group", func(p, g string) (string, string) {
			return p, g + name + ":x:24000:\n"
		}},
		{"root primary", func(p, g string) (string, string) {
			return strings.Replace(p, ":21000:23000:", ":21000:0:", 1), strings.Replace(g, name+":x:23000:", name+":x:0:", 1)
		}},
		{"KVM primary without explicit membership", func(p, g string) (string, string) {
			return p, g + "kvm:x:23000:\n"
		}},
		{"privileged group alias without explicit membership", func(p, g string) (string, string) {
			return p, g + "sudo:x:23000:\n"
		}},
		{"other slot primary without explicit membership", func(p, g string) (string, string) {
			return strings.Replace(p, ":21001:23001:", ":21001:23000:", 1), strings.Replace(g, "-s01:x:23001:", "-s01:x:23000:", 1)
		}},
		{"primary wraps kernel ID", func(p, g string) (string, string) {
			return strings.Replace(p, ":21000:23000:", ":21000:4294967296:", 1), strings.Replace(g, name+":x:23000:", name+":x:4294967296:", 1)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			passwd, groups := slotAccountFixture()
			passwd, groups = tc.change(passwd, groups)
			if _, err := parseSlotAccounts([]byte(passwd), []byte(groups)); err == nil {
				t.Fatal("unsafe primary group was accepted")
			}
		})
	}
}

func TestSlotAccountsKeepPrivatePrimaryAndOptionalKVM(t *testing.T) {
	passwd, groups := slotAccountFixture()
	groups += "kvm:x:24000:" + brand.Name + "-s00\n"
	accounts, err := parseSlotAccounts([]byte(passwd), []byte(groups))
	if err != nil {
		t.Fatal(err)
	}
	for i, account := range accounts {
		if account.uid != 21000+i || account.gid != 23000+i || len(account.groups) < 1 || account.groups[0] != uint32(account.gid) {
			t.Fatalf("slot %d lost its private primary group: %+v", i, account)
		}
	}
	if len(accounts[0].groups) != 2 || accounts[0].groups[1] != 24000 || len(accounts[1].groups) != 1 {
		t.Fatal("optional KVM membership changed unrelated slot groups")
	}
}

func TestCallerPrimaryCannotAliasAnySlotKernelGroup(t *testing.T) {
	for _, account := range []slotAccount{
		{gid: 22000},
		{gid: 22000, groups: []uint32{23000}},
		{gid: 23000, groups: []uint32{22000}},
	} {
		if err := checkCallerSlotGroups([]slotAccount{account}, 22000); err == nil {
			t.Fatalf("caller primary alias was accepted: %+v", account)
		}
	}
	if err := checkCallerSlotGroups([]slotAccount{{gid: 23000, groups: []uint32{23000, 24000}}}, 22000); err != nil {
		t.Fatal(err)
	}
}
