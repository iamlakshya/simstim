//go:build linux

package host

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/iamlakshya/simstim/internal/brand"
)

// custodyUnitWordV1 encodes one literal systemd word. Colon on ExecStart
// disables environment expansion; doubled percent closes specifier expansion.
func custodyUnitWordV1(value string) (string, error) {
	if strings.ContainsRune(value, 0) {
		return "", sessionLeaseError("a native runtime unit input contains a NUL byte")
	}
	var word strings.Builder
	word.WriteByte('"')
	for _, c := range []byte(value) {
		switch {
		case c == '%':
			word.WriteString("%%")
		case c == '"' || c == '\\':
			word.WriteByte('\\')
			word.WriteByte(c)
		case c < 0x20 || c == 0x7f:
			fmt.Fprintf(&word, "\\x%02x", c)
		default:
			word.WriteByte(c)
		}
	}
	word.WriteByte('"')
	return word.String(), nil
}

func custodyServiceFileV1(m *Manifest, executable string, argv []string) ([]byte, error) {
	if m == nil || !validID(m.ID) || !filepath.IsAbs(executable) || len(argv) != 3 || argv[0] != executable || argv[1] != "supervise" || argv[2] != m.ID {
		return nil, sessionLeaseError("the runtime custody service has no closed supervisor command")
	}
	var out strings.Builder
	fmt.Fprintf(&out, "# Owned session custody v1\n[Unit]\nDescription=%s session %s\nCollectMode=inactive-or-failed\n\n[Service]\nType=exec\nSlice=%s\nKillMode=mixed\nTimeoutStopSec=30\nOOMPolicy=continue\nRestart=no\nTasksMax=4096\nNotifyAccess=main\nFileDescriptorStoreMax=5\nFileDescriptorStorePreserve=yes\nExecStart=:", brand.Name, m.ID, brand.Slice)
	for i, value := range argv {
		word, err := custodyUnitWordV1(value)
		if err != nil {
			return nil, err
		}
		if i != 0 {
			out.WriteByte(' ')
		}
		out.WriteString(word)
	}
	out.WriteByte('\n')
	for _, entry := range supervisorLaunchEnv(UnitName(m.ID)) {
		word, err := custodyUnitWordV1(entry)
		if err != nil {
			return nil, err
		}
		out.WriteString("Environment=" + word + "\n")
	}
	out.WriteString("UnsetEnvironment=" + holderLockFDEnv + "\n")
	if m.Limits.MemoryMax > 0 {
		out.WriteString("MemoryMax=" + strconv.FormatInt(m.Limits.MemoryMax, 10) + "\nMemorySwapMax=0\n")
	}
	if out.Len() > 256<<10 {
		return nil, sessionLeaseError("the runtime custody unit exceeds its closed input bound")
	}
	return []byte(out.String()), nil
}

func custodyTargetFileV1() []byte {
	return []byte("# Owned session custody v1\n[Unit]\nDescription=" + brand.Name + " retained session custody\nDefaultDependencies=no\nStopWhenUnneeded=no\n")
}

func custodyPinFileV1(id string) (string, []byte, error) {
	if !validID(id) {
		return "", nil, sessionLeaseError("the custody pin has no exact session")
	}
	return "50-session-" + id + ".conf", []byte("# Owned session custody v1\n[Unit]\nAfter=" + UnitName(id) + "\n"), nil
}
