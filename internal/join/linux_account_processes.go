//go:build linux

package join

import (
	"bufio"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// waitCreatedAccountStopped accepts actual absence only after a complete proc
// read. A failed loginctl call never turns into a successful stop by itself.
func (e *linuxExecutor) waitCreatedAccountStopped(ctx context.Context, entry *ledgerEntry) error {
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		if _, err := e.verifyCreatedAccount(entry, false); err != nil {
			return err
		}
		remaining, err := accountProcessesRemain(entry.UID)
		if err != nil {
			return err
		}
		if !remaining {
			return nil
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-deadline.C:
			timer.Stop()
			return codeFailure("join_undo", "some original account processes remain; stop its services and retry the retained leave script")
		case <-timer.C:
		}
	}
}
func accountProcessesRemain(uid int) (bool, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return false, err
	}
	wanted := strconv.Itoa(uid)
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 {
			continue
		}
		file, err := os.Open(filepath.Join("/proc", entry.Name(), "status"))
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrProcessDone) {
			continue
		}
		if err != nil {
			return false, err
		}
		scanner := bufio.NewScanner(file)
		found := false
		remaining := false
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "Uid:") {
				continue
			}
			fields := strings.Fields(strings.TrimPrefix(line, "Uid:"))
			if len(fields) != 4 {
				file.Close()
				return false, codeFailure("join_undo", "a process account identity could not be checked")
			}
			found = true
			for _, field := range fields {
				if field == wanted {
					remaining = true
				}
			}
			break
		}
		scanErr := scanner.Err()
		closeErr := file.Close()
		if scanErr != nil {
			return false, scanErr
		}
		if closeErr != nil {
			return false, closeErr
		}
		if !found {
			if _, err = os.Lstat(filepath.Join("/proc", entry.Name())); errors.Is(err, os.ErrNotExist) {
				continue
			}
			return false, codeFailure("join_undo", "a live process account identity was unavailable")
		}
		if remaining {
			return true, nil
		}
	}
	return false, nil
}
func refuseOwnCreatedAccountSession(entry *ledgerEntry) error {
	body, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return err
	}
	if strings.Contains(string(body), "/user-"+strconv.Itoa(entry.UID)+".slice/") {
		return codeFailure("join_undo", "leave is running inside the account it must stop; use the machine owner's separate console")
	}
	return nil
}
