package supervisor

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
)

// processGone reports whether pid has gone, or waits as a zombie.
func processGone(pid int) bool {
	out, err := exec.CommandContext(context.Background(), "ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	st := strings.TrimSpace(string(out))
	return err != nil || st == "" || strings.HasPrefix(st, "Z")
}
