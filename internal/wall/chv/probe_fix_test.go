package chv

import "testing"

// The fix for /dev/kvm names the user so that the shell expands $USER when
// the name is unknown, and quotes a known name.
func TestKVMAccessFixUser(t *testing.T) {
	for _, tc := range []struct {
		user    string
		setfacl bool
		want    string
	}{
		{"", false, `sudo usermod -aG kvm "$USER"`},
		{"$USER", false, `sudo usermod -aG kvm "$USER"`},
		{"", true, `sudo sh -c 'setfacl -m "u:$1:rw" /dev/kvm && usermod -aG kvm "$1"' _ "$USER"`},
		{"admin", false, "sudo usermod -aG kvm admin"},
	} {
		if got := kvmAccessFix(tc.user, tc.setfacl); got != tc.want {
			t.Errorf("kvmAccessFix(%q, %v) = %s, want %s", tc.user, tc.setfacl, got, tc.want)
		}
	}
}
