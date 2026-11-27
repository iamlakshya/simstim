//go:build linux

package host

import (
	"fmt"
	"os"
	"testing"

	"github.com/godbus/dbus/v5"
)

func TestVerifyManagerCgroupPathsV1(t *testing.T) {
	uid := 1000
	validInit := fmt.Appendf(nil, "0::/user.slice/user-%d.slice/user@%d.service/init.scope\n", uid, uid)
	if err := verifyManagerCgroupPathsV1(validInit, uid, true); err != nil {
		t.Fatalf("valid init.scope rejected: %v", err)
	}
	validSvc := fmt.Appendf(nil, "0::/user.slice/user-%d.slice/user@%d.service\n", uid, uid)
	if err := verifyManagerCgroupPathsV1(validSvc, uid, false); err != nil {
		t.Fatalf("valid user@.service rejected when init.scope optional: %v", err)
	}
	if err := verifyManagerCgroupPathsV1(validSvc, uid, true); err == nil {
		t.Fatal("user@.service accepted when init.scope required")
	}
	for _, bad := range []string{
		fmt.Sprintf("0::/../user@%d.service/init.scope\n", uid),
		fmt.Sprintf("0::/user.slice/user-%d.slice/session-1.scope\n", uid),
		fmt.Sprintf("0::/user.slice/user-%d.slice/user@%d.service/init.scope\n", uid+1, uid+1),
		"0::/\n",
	} {
		if err := verifyManagerCgroupPathsV1([]byte(bad), uid, false); err == nil {
			t.Errorf("accepted invalid cgroup %q", bad)
		}
	}
}

func TestVerifyManagerStatusV1(t *testing.T) {
	uid := os.Getuid()
	goodPeer := fmt.Sprintf("Name:\tsystemd\nPid:\t420\nTgid:\t420\nNSpid:\t420\nPPid:\t1\nUid:\t%d\t%d\t%d\t%d\nGid:\t%d\t%d\t%d\t%d\nCapPrm:\t0000000800000000\n", uid, uid, uid, uid, uid, uid, uid, uid)
	goodSelf := "CapPrm:\t0000000000000000\nCapEff:\t0000000000000000\n"
	if err := verifyManagerStatusV1([]byte(goodPeer), []byte(goodSelf), 420, uid); err != nil {
		t.Fatalf("valid systemd --user status rejected: %v", err)
	}
	cases := []struct {
		name string
		peer string
		self string
	}{
		{"zero-cap dumpable peer", fmt.Sprintf("Name:\tsystemd\nPid:\t420\nTgid:\t420\nNSpid:\t420\nPPid:\t1\nUid:\t%d\t%d\t%d\t%d\nGid:\t%d\t%d\t%d\t%d\nCapPrm:\t0000000000000000\n", uid, uid, uid, uid, uid, uid, uid, uid), goodSelf},
		{"equal self CapPrm", goodPeer, "CapPrm:\t0000000800000000\nCapEff:\t0000000000000000\n"},
		{"wrong tgid", fmt.Sprintf("Name:\tsystemd\nPid:\t420\nTgid:\t421\nNSpid:\t420\nPPid:\t1\nUid:\t%d\t%d\t%d\t%d\nGid:\t%d\t%d\t%d\t%d\nCapPrm:\t0000000800000000\n", uid, uid, uid, uid, uid, uid, uid, uid), goodSelf},
		{"wrong ppid", fmt.Sprintf("Name:\tsystemd\nPid:\t420\nTgid:\t420\nNSpid:\t420\nPPid:\t99\nUid:\t%d\t%d\t%d\t%d\nGid:\t%d\t%d\t%d\t%d\nCapPrm:\t0000000800000000\n", uid, uid, uid, uid, uid, uid, uid, uid), goodSelf},
		{"nested pidns", fmt.Sprintf("Name:\tsystemd\nPid:\t420\nTgid:\t420\nNSpid:\t420\t1\nPPid:\t1\nUid:\t%d\t%d\t%d\t%d\nGid:\t%d\t%d\t%d\t%d\nCapPrm:\t0000000800000000\n", uid, uid, uid, uid, uid, uid, uid, uid), goodSelf},
		{"wrong name", fmt.Sprintf("Name:\tfake\nPid:\t420\nTgid:\t420\nNSpid:\t420\nPPid:\t1\nUid:\t%d\t%d\t%d\t%d\nGid:\t%d\t%d\t%d\t%d\nCapPrm:\t0000000800000000\n", uid, uid, uid, uid, uid, uid, uid, uid), goodSelf},
		{"wrong uid", fmt.Sprintf("Name:\tsystemd\nPid:\t420\nTgid:\t420\nNSpid:\t420\nPPid:\t1\nUid:\t%d\t%d\t%d\t%d\nGid:\t%d\t%d\t%d\t%d\nCapPrm:\t0000000800000000\n", uid+1, uid+1, uid+1, uid+1, uid, uid, uid, uid), goodSelf},
		{"extra uid field", fmt.Sprintf("Name:\tsystemd\nPid:\t420\nTgid:\t420\nNSpid:\t420\nPPid:\t1\nUid:\t%d\t%d\t%d\t%d\t0\nGid:\t%d\t%d\t%d\t%d\nCapPrm:\t0000000800000000\n", uid, uid, uid, uid, uid, uid, uid, uid), goodSelf},
		{"mismatched gid", fmt.Sprintf("Name:\tsystemd\nPid:\t420\nTgid:\t420\nNSpid:\t420\nPPid:\t1\nUid:\t%d\t%d\t%d\t%d\nGid:\t%d\t%d\t%d\t%d\nCapPrm:\t0000000800000000\n", uid, uid, uid, uid, uid, uid+1, uid, uid), goodSelf},
	}
	for _, tc := range cases {
		if err := verifyManagerStatusV1([]byte(tc.peer), []byte(tc.self), 420, uid); err == nil {
			t.Errorf("%s: unexpectedly accepted", tc.name)
		}
	}
}

func TestVerifyUserNamespaceMapBytesV1(t *testing.T) {
	hostMap := []byte("         0          0 4294967295\n")
	if err := verifyUserNamespaceMapBytesV1(hostMap, hostMap, hostMap); err != nil {
		t.Fatalf("matching host root maps rejected: %v", err)
	}
	for _, tc := range []struct {
		name             string
		pid1, self, peer string
	}{
		{"empty peer map", string(hostMap), string(hostMap), ""},
		{"single-uid child userns", string(hostMap), string(hostMap), "1000 1000 1\n"},
		{"mismatched self vs pid1", string(hostMap), "0 1000 1\n", "0 1000 1\n"},
		{"mismatched peer map", string(hostMap), string(hostMap), "0 0 65536\n"},
		{"truncated triplet", string(hostMap), string(hostMap), "0 0\n"},
		{"zero extent count", string(hostMap), string(hostMap), "0 0 0\n"},
	} {
		if err := verifyUserNamespaceMapBytesV1([]byte(tc.pid1), []byte(tc.self), []byte(tc.peer)); err == nil {
			t.Errorf("%s: unexpectedly accepted", tc.name)
		}
	}
}

func TestVerifyManagerCmdlineV1(t *testing.T) {
	for _, good := range []struct {
		raw  []byte
		want string
	}{
		{[]byte("/usr/lib/systemd/systemd\x00--user\x00"), "/usr/lib/systemd/systemd"},
		{[]byte("/lib/systemd/systemd\x00--user\x00"), "/lib/systemd/systemd"},
	} {
		got, err := verifyManagerCmdlineV1(good.raw)
		if err != nil || got != good.want {
			t.Fatalf("verifyManagerCmdlineV1(%q) = %q, %v; want %q, nil", good.raw, got, err, good.want)
		}
	}
	for _, bad := range [][]byte{
		nil,
		[]byte("/usr/lib/systemd/systemd\x00"),
		[]byte("/usr/bin/fake\x00--user\x00"),
		[]byte("/usr/lib/systemd/systemd\x00--user\x00--extra\x00"),
	} {
		if _, err := verifyManagerCmdlineV1(bad); err == nil {
			t.Errorf("verifyManagerCmdlineV1(%q) unexpectedly succeeded", bad)
		}
	}
}

func TestManagerStartupEventsEmptyInvocationIDV1(t *testing.T) {
	unit := "test-unit.service"
	path := dbus.ObjectPath("/org/freedesktop/systemd1/unit/test_2dunit_2eservice")
	makeMessage := func(inv []byte) *dbus.Message {
		return &dbus.Message{
			Type: dbus.TypeSignal,
			Headers: map[dbus.HeaderField]dbus.Variant{
				dbus.FieldInterface: dbus.MakeVariant("org.freedesktop.DBus.Properties"),
				dbus.FieldMember:    dbus.MakeVariant("PropertiesChanged"),
				dbus.FieldPath:      dbus.MakeVariant(path),
			},
			Body: []any{
				systemdUnitInterface,
				map[string]dbus.Variant{"InvocationID": dbus.MakeVariant(inv)},
				[]string{},
			},
		}
	}
	events := &managerStartupEventsV1{unit: unit, path: path, wake: make(chan struct{}, 1)}
	events.observe(makeMessage([]byte{}))
	if events.err != nil {
		t.Fatalf("pre-activation empty InvocationID faulted: %v", events.err)
	}
	events.observe(makeMessage(make([]byte, 16)))
	if events.err != nil {
		t.Fatalf("pre-activation zero InvocationID faulted: %v", events.err)
	}
	events.activation.InvocationID = "0123456789abcdef0123456789abcdef"
	events.observe(makeMessage([]byte{}))
	if events.err == nil {
		t.Fatal("post-activation empty InvocationID did not fault")
	}
	eventsZero := &managerStartupEventsV1{unit: unit, path: path, wake: make(chan struct{}, 1)}
	eventsZero.activation.InvocationID = "0123456789abcdef0123456789abcdef"
	eventsZero.observe(makeMessage(make([]byte, 16)))
	if eventsZero.err == nil {
		t.Fatal("post-activation zero InvocationID did not fault")
	}
}
