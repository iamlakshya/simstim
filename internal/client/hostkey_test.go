package client

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

// Box and box have two generated SSH configs, also on a disk that ignores
// case, as they have two locks and two records: the forget of Box leaves
// the config of box. A name that cannot name a host gets no path.
func TestCaseDistinctHostsKeepTheirConfigs(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	upper, lower := Host{Name: "Box", SSH: "Box"}, Host{Name: "box", SSH: "box"}
	if strings.EqualFold(wrapperPath(upper), wrapperPath(lower)) {
		t.Fatalf("Box and box share %s", wrapperPath(upper))
	}
	// The name in the file's name is in lower case, as the end-to-end
	// tests find it.
	key := HostFileKey("Box")
	suffix := strings.TrimPrefix(key, "box-")
	if !strings.HasPrefix(key, "box-") || len(suffix) != 32 || strings.Trim(suffix, "0123456789abcdef") != "" {
		t.Errorf("HostFileKey(Box) = %q, want lower-case prefix and canonical 128-bit hex suffix", key)
	}
	if strings.EqualFold(key, HostFileKey("box")) {
		t.Error("case-distinct names share their canonical file key")
	}
	if err := SaveHosts(context.Background(), []Host{upper, lower}); err != nil {
		t.Fatal(err)
	}
	upperControl, lowerControl := controlKey(upper.Name, nil), controlKey(lower.Name, nil)
	if len(upperControl) != 26 || !controlKeyRE.MatchString(upperControl) || strings.EqualFold(upperControl, lowerControl) {
		t.Errorf("case-distinct hosts have invalid canonical 128-bit control keys: %q, %q", upperControl, lowerControl)
	}
	for _, h := range []Host{upper, lower} {
		if _, err := writeWrapper(h); err != nil {
			t.Fatal(err)
		}
	}
	if err := UpdateHosts(context.Background(), HostChange{Kind: "host rm", Was: &upper}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(wrapperPath(upper)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the config of Box stays: %v", err)
	}
	if _, err := os.Stat(wrapperPath(lower)); err != nil {
		t.Errorf("the forget of Box removed the config of box: %v", err)
	}
	if odd := wrapperPath(Host{Name: "../x"}); odd != "" {
		t.Errorf("a name that cannot name a host has the config %s", odd)
	}
}

// The lock of a system without file locks fails, on every system that
// runs the tests: no command claims a lock that it does not hold.
func TestALockWithoutFileLocks(t *testing.T) {
	if err := lockUnsupported(); !errors.Is(err, errNoLocks) {
		t.Errorf("lockUnsupported = %v", err)
	}
}
