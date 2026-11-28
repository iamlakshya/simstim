package host

import (
	"encoding/binary"
	"testing"
)

func TestCapProbeParsers(t *testing.T) {
	osRelease := []byte("NAME=\"Debian GNU/Linux\"\nPRETTY_NAME=\"Debian GNU/Linux 13 (trixie)\"\nID=debian\n")
	if got := parseOSReleasePrettyName(osRelease); got != "Debian GNU/Linux 13 (trixie)" {
		t.Errorf("parseOSReleasePrettyName = %q", got)
	}
	if got := parseOSReleasePrettyName([]byte("NAME=Ubuntu\nVERSION_ID=\"24.04\"\n")); got != "Ubuntu 24.04" {
		t.Errorf("parseOSReleasePrettyName fallback = %q", got)
	}

	cpuIntel := []byte("processor\t: 0\nflags\t\t: fpu vme vmxnet vmx avx2\n")
	if got := parseCPUInfoVirt(cpuIntel); got != "vmx" {
		t.Errorf("parseCPUInfoVirt intel = %q, want vmx", got)
	}
	cpuAMD := []byte("processor\t: 0\nflags\t\t: fpu svm sse2\n")
	if got := parseCPUInfoVirt(cpuAMD); got != "svm" {
		t.Errorf("parseCPUInfoVirt amd = %q, want svm", got)
	}
	cpuNone := []byte("processor\t: 0\nflags\t\t: fpu vmxnet avx2\n")
	if got := parseCPUInfoVirt(cpuNone); got != "" {
		t.Errorf("parseCPUInfoVirt none = %q, want empty", got)
	}

	if got := parseCgroupControllers([]byte("cpu memory pids\n")); len(got) != 3 || got[0] != "cpu" || got[2] != "pids" {
		t.Errorf("parseCgroupControllers = %q", got)
	}
	if got := parseSystemdVersion([]byte("systemd 257 (257.9-1~deb13u1)\n+PAM +AUDIT\n")); got != 257 {
		t.Errorf("parseSystemdVersion = %d, want 257", got)
	}
	if got := parseProcStatusGroups([]byte("Name:\tsystemd\nUid:\t1000\nGroups:\t4 20 27 109\n")); len(got) != 4 || got[3] != 109 {
		t.Errorf("parseProcStatusGroups = %v", got)
	}
	subid := []byte("# comment\nme:100000:65536\n1001:165536:65536\nzero:231072:0\n")
	if !parseSubID(subid, "me", 1000) || !parseSubID(subid, "other", 1001) || parseSubID(subid, "zero", 1002) || parseSubID(subid, "absent", 1003) {
		t.Error("parseSubID mismatch")
	}
}

func makePOSIXACL(entries ...[3]uint32) []byte {
	buf := make([]byte, 4+len(entries)*8)
	binary.LittleEndian.PutUint32(buf[:4], 2)
	for i, e := range entries {
		off := 4 + i*8
		binary.LittleEndian.PutUint16(buf[off:off+2], uint16(e[0]))
		binary.LittleEndian.PutUint16(buf[off+2:off+4], uint16(e[1]))
		binary.LittleEndian.PutUint32(buf[off+4:off+8], e[2])
	}
	return buf
}

func TestParsePOSIXACLAccess(t *testing.T) {
	// User 1000 with rw (0x06) and mask rw (0x06) -> true.
	okACL := makePOSIXACL([3]uint32{0x02, 0x06, 1000}, [3]uint32{0x10, 0x06, 0xffffffff})
	if !parsePOSIXACLAccess(okACL, 1000) {
		t.Error("expected rw ACL for uid 1000 to match")
	}
	if parsePOSIXACLAccess(okACL, 1001) {
		t.Error("unexpected match for uid 1001")
	}
	// User 1000 with read-only (0x04) -> false.
	roACL := makePOSIXACL([3]uint32{0x02, 0x04, 1000}, [3]uint32{0x10, 0x06, 0xffffffff})
	if parsePOSIXACLAccess(roACL, 1000) {
		t.Error("read-only ACL must not satisfy rw check")
	}
	// User 1000 with rw, but ACL_MASK restricts to read-only (0x04) -> false.
	maskedACL := makePOSIXACL([3]uint32{0x02, 0x06, 1000}, [3]uint32{0x10, 0x04, 0xffffffff})
	if parsePOSIXACLAccess(maskedACL, 1000) {
		t.Error("masked-out ACL must not satisfy rw check")
	}
	// Wrong version or truncated bytes -> false.
	if parsePOSIXACLAccess([]byte{1, 0, 0, 0, 2, 0, 6, 0, 0, 0, 0, 0}, 0) {
		t.Error("wrong ACL version matched")
	}
	if parsePOSIXACLAccess([]byte{2, 0, 0}, 1000) {
		t.Error("truncated ACL matched")
	}
}
