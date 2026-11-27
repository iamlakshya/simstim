package proc

import (
	"fmt"
	"strconv"
	"strings"
)

// LinuxBirthTicksV1 decodes diagnostic field 22 of a kernel proc stat record.
// The result is DATA only. It never identifies a process without its original
// native lifetime, and cannot authorise a signal, cleanup or execution.
func LinuxBirthTicksV1(raw []byte) (string, error) {
	line := string(raw)
	end := strings.LastIndexByte(line, ')')
	if end < 0 {
		return "", fmt.Errorf("the kernel process observation has no command boundary")
	}
	fields := strings.Fields(line[end+1:])
	if len(fields) <= 19 {
		return "", fmt.Errorf("the kernel process observation has no birth tick")
	}
	value, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil || value == 0 {
		return "", fmt.Errorf("the kernel process observation has an invalid birth tick")
	}
	return strconv.FormatUint(value, 10), nil
}
