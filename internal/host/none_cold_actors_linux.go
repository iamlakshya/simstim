//go:build linux

package host

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	processrecord "github.com/iamlakshya/simstim/internal/proc"
)

const noneActorScanBoundV1 = 65536
const noneActorCommandBoundV1 = 65536

// A closed role associates a CURRENT process with this exact target. A saved
// process number is only an additional refusal selector. Neither is a grant,
// a signal target, a whole-unit proof, or permission for a different wall.
func noneActorRoleV1(argv []string, id string) bool {
	if len(argv) >= 3 && argv[2] == id {
		return argv[1] == "supervise" || argv[1] == "supervise-restore-v1" || argv[1] == "holder"
	}
	return len(argv) >= 5 && argv[1] == "wall-init" && argv[2] == "--wall" && argv[3] == "none" && argv[4] == id
}

func readNoneActorCommandV1(ctx context.Context, root *os.Root) ([]string, error) {
	file, err := root.Open("cmdline")
	if err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(io.LimitReader(sessionOperationReader{ctx: ctx, reader: file}, noneActorCommandBoundV1+1))
	err = errors.Join(err, file.Close())
	if err != nil {
		return nil, err
	}
	oversized := len(raw) > noneActorCommandBoundV1
	if len(raw) == 0 {
		return nil, nil
	}
	terminated := raw[len(raw)-1] == 0
	if terminated {
		raw = raw[:len(raw)-1]
	}
	parts := bytes.Split(raw, []byte{0})
	argv := make([]string, len(parts))
	for i, part := range parts {
		argv[i] = string(part)
	}
	if oversized || !terminated {
		return argv, sessionLeaseError("a current process has an oversized or incomplete command observation")
	}
	return argv, nil
}

func checkNoneColdActorsV1(ctx context.Context, source *Manifest, members ...string) error {
	if ctx == nil || !validNoneCleanupV1(source, BootID()) {
		return sessionLeaseError("the None process check has no original trusted cleanup epoch")
	}
	proc, err := os.OpenRoot(procRoot)
	if err != nil {
		return err
	}
	defer proc.Close()
	return scanNoneColdActorsV1(ctx, proc, source, members...)
}

func scanNoneColdActorsV1(ctx context.Context, proc *os.Root, source *Manifest, members ...string) error {
	if ctx == nil || proc == nil || source == nil || source.NoneCleanup == nil || !validID(source.ID) {
		return sessionLeaseError("the None process check has no exact original target")
	}
	targets := append([]string{source.ID}, members...)
	for _, id := range targets {
		if !validID(id) {
			return sessionLeaseError("the None refusal check has an unsupported original member")
		}
	}
	matches := func(argv []string) bool {
		for _, id := range targets {
			if noneActorRoleV1(argv, id) {
				return true
			}
		}
		return false
	}
	reader, err := proc.Open(".")
	if err != nil {
		return err
	}
	defer reader.Close()
	count := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries, readErr := reader.ReadDir(128)
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			pid, err := strconv.Atoi(entry.Name())
			if err != nil || pid <= 0 {
				continue
			}
			count++
			if count > noneActorScanBoundV1 {
				return sessionLeaseError("the bounded None process check could not inspect this host completely")
			}
			associated := pid == source.NoneCleanup.SupervisorPID || pid == source.NoneCleanup.InitPID
			root, err := proc.OpenRoot(entry.Name())
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				if associated {
					return errors.Join(sessionLeaseError("a recorded original None actor has an unknown current lifetime"), err)
				}
				continue // UID/path ignorance does not associate an unrelated actor.
			}
			if associated {
				observed, birthErr := readNoneActorBirthV1(ctx, root)
				if errors.Is(birthErr, os.ErrNotExist) {
					root.Close()
					continue
				}
				if birthErr != nil {
					root.Close()
					return errors.Join(sessionLeaseError("a recorded original None actor has an unknown current kernel birth"), birthErr)
				}
				expected := source.NoneCleanup.SupervisorBirth
				if pid == source.NoneCleanup.InitPID {
					expected = source.NoneCleanup.InitBirth
				}
				associated = observed == expected
				if associated {
					root.Close()
					return sessionLeaseError("the same original None actor still has a current kernel lifetime")
				}
			}
			login, uidErr := readNoneActorLoginV1(ctx, root, uint32(os.Getuid()))
			if errors.Is(uidErr, os.ErrNotExist) {
				root.Close()
				continue
			}
			if uidErr == nil && !login {
				root.Close()
				continue
			}
			argv, commandErr := readNoneActorCommandV1(ctx, root)
			closeErr := root.Close()
			if commandErr != nil {
				if matches(argv) {
					return errors.Join(sessionLeaseError("a current matching None helper has an uncertain command observation"), commandErr, closeErr)
				}
				if errors.Is(commandErr, os.ErrNotExist) {
					continue
				}
				continue // Unreadable unrelated ND0 processes must not block resume.
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if matches(argv) {
				return sessionLeaseError(fmt.Sprintf("a current helper still matches this original None session (%d)", pid))
			}
		}
		if errors.Is(readErr, io.EOF) {
			return ctx.Err()
		}
		if readErr != nil {
			return readErr
		}
	}
}

func readNoneActorBirthV1(ctx context.Context, root *os.Root) (string, error) {
	file, err := root.Open("stat")
	if err != nil {
		return "", err
	}
	raw, err := io.ReadAll(io.LimitReader(sessionOperationReader{ctx: ctx, reader: file}, 65537))
	err = errors.Join(err, file.Close())
	if err != nil {
		return "", err
	}
	if len(raw) > 65536 {
		return "", sessionLeaseError("the current target kernel record exceeds its observation bound")
	}
	return processrecord.LinuxBirthTicksV1(raw)
}

// Dumpable=0 can make the proc directory itself root-owned. The current
// kernel status credentials, not that directory's owner or readable exe,
// select the account. Unknown unrelated state still supplies no association.
func readNoneActorLoginV1(ctx context.Context, root *os.Root, wanted uint32) (bool, error) {
	file, err := root.Open("status")
	if err != nil {
		return false, err
	}
	raw, err := io.ReadAll(io.LimitReader(sessionOperationReader{ctx: ctx, reader: file}, 65537))
	err = errors.Join(err, file.Close())
	if err != nil {
		return false, err
	}
	if len(raw) > 65536 {
		return false, sessionLeaseError("the current kernel account record exceeds its observation bound")
	}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != "Uid:" {
			continue
		}
		if len(fields) != 5 {
			return false, sessionLeaseError("the current kernel account observation is incomplete")
		}
		values := [4]uint32{}
		for i, value := range fields[1:] {
			n, err := strconv.ParseUint(value, 10, 32)
			if err != nil {
				return false, sessionLeaseError("the current kernel account observation is invalid")
			}
			values[i] = uint32(n)
		}
		return values[0] == wanted || values[1] == wanted, ctx.Err()
	}
	return false, sessionLeaseError("the current kernel account observation is absent")
}
