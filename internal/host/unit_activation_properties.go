package host

import (
	"errors"
	"path"
	"strconv"
	"strings"
)

// unitActivationProperties is a strict manager observation. It is not a
// startup grant and cannot authorise a signal, descriptor handoff or save.
// The launch owner must bind it to its actual manager and live native peer.
type unitActivationProperties struct {
	InvocationID string
	MainPID      uint32
	ControlGroup string
	Type         string
}

// decodeUnitActivationProperties reads only the four properties explicitly
// requested from the manager. Missing, repeated and unrelated fields fail
// rather than silently selecting a value from an ambiguous response.
func decodeUnitActivationProperties(raw []byte) (unitActivationProperties, error) {
	var result unitActivationProperties
	seen := make(map[string]bool, 4)
	for _, line := range strings.Split(string(raw), "\n") {
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || seen[key] {
			return unitActivationProperties{}, unitActivationPropertyError()
		}
		seen[key] = true
		switch key {
		case "InvocationID":
			result.InvocationID = value
		case "MainPID":
			pid, err := strconv.ParseUint(value, 10, 31)
			if err != nil || pid == 0 || strconv.FormatUint(pid, 10) != value {
				return unitActivationProperties{}, unitActivationPropertyError()
			}
			result.MainPID = uint32(pid)
		case "ControlGroup":
			result.ControlGroup = value
		case "Type":
			result.Type = value
		default:
			return unitActivationProperties{}, unitActivationPropertyError()
		}
	}
	if len(seen) != 4 || !validUnitActivationProperties(result) {
		return unitActivationProperties{}, unitActivationPropertyError()
	}
	return result, nil
}

func validUnitActivationProperties(value unitActivationProperties) bool {
	if len(value.InvocationID) != 32 || value.MainPID == 0 || value.MainPID > 1<<31-1 {
		return false
	}
	nonzero := false
	for _, c := range value.InvocationID {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
		nonzero = nonzero || c != '0'
	}
	if !nonzero || !path.IsAbs(value.ControlGroup) || value.ControlGroup == "/" || path.Clean(value.ControlGroup) != value.ControlGroup || strings.ContainsAny(value.ControlGroup, "\x00\r\n") {
		return false
	}
	// Valid manager data does not itself mean that the launch contract is
	// suitable. In particular, a startup grant may require Type=exec.
	switch value.Type {
	case "simple", "exec", "forking", "oneshot", "dbus", "notify", "notify-reload", "idle":
		return true
	default:
		return false
	}
}

// sameUnitActivationProperties compares observations only. A matching tuple
// still needs the original launch and live native-object checks.
func sameUnitActivationProperties(expected, current unitActivationProperties) bool {
	return validUnitActivationProperties(expected) && validUnitActivationProperties(current) && expected == current
}

func unitActivationPropertyError() error {
	return errors.New("the service manager returned incomplete or ambiguous activation properties. Try: retry the start after checking the user service manager")
}
