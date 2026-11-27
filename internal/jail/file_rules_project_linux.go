package jail

import (
	"errors"
	"fmt"
	"strconv"

	"golang.org/x/sys/unix"
)

const readonlyFilesPrefixV1 = ".jail-file-rules-v1"

// KCMP_FILE in Linux's closed kcmp UAPI compares struct file identity. The
// pinned x/sys release exports SYS_KCMP, but no KCMP_FILE constant.
const readonlyKcmpFileV1 = 0

type readonlyFileSnapshotV1 struct {
	rule    pinnedFileRuleV1
	flags   int
	fdFlags int
	offset  int64
}

type readonlyFileGroupV1 struct {
	files  []readonlyFileSnapshotV1
	opened int
	target string
}

// projectReadonlyFileRulesV1 is a private v1 security transform. The caller
// has checked the complete inherited vector and its writable/backing aliases,
// verified the final mount graph, and still owns the trusted mount stage.
// It calls this before pivoting or making the new tmpfs root read-only.
//
// Every projected slot keeps FD_CLOEXEC. A Read grant keeps its initial
// cursor; an Exec-only grant becomes O_PATH, which exposes no read or seek
// operation. Child slots that share an OFD keep sharing one reopened OFD;
// independent OFDs stay independent. Projected OFDs cease sharing their
// parent's cursor. The grant's rights, not the old FD's readable status,
// determine whether the reopened descriptor permits reading.
// Ambiguous same-inode groups fail before effects when KCMP_FILE is denied.
// Any replacement failure closes every selected slot: the caller must abort.
// No capabilities, inode grants, writable aliases or seccomp policy change.
func projectReadonlyFileRulesV1(root int, dev uint64, rules []pinnedFileRuleV1) (result error) {
	groups, err := snapshotReadonlyFilesV1(rules)
	if err != nil || len(groups) == 0 {
		return err
	}
	prefix, err := makeReadonlyFilesPrefixV1(root, dev)
	if err != nil {
		return err
	}
	replacing := false
	defer func() {
		for index := range groups {
			if groups[index].opened >= 0 {
				result = errors.Join(result, closeReadonlyFDV1(groups[index].opened))
			}
		}
		for index := range groups {
			group := &groups[index]
			if result != nil && group.target != "" {
				result = errors.Join(result, removeReadonlyTargetV1(prefix, group.target))
			}
		}
		if result != nil {
			result = errors.Join(result, unix.Unlinkat(root, readonlyFilesPrefixV1, unix.AT_REMOVEDIR))
		}
		result = errors.Join(result, closeReadonlyFDV1(prefix))
		if result != nil && replacing {
			for _, group := range groups {
				for _, file := range group.files {
					result = errors.Join(result, closeReadonlyFDV1(file.rule.FD))
				}
			}
		}
	}()
	for index := range groups {
		group := &groups[index]
		group.target = strconv.Itoa(index)
		group.opened, err = mountReadonlyFileV1(prefix, dev, group.target, group.files[0])
		if err != nil {
			return err
		}
	}
	for _, group := range groups {
		for _, file := range group.files {
			flags := 0
			if file.fdFlags&unix.FD_CLOEXEC != 0 {
				flags = unix.O_CLOEXEC
			}
			replacing = true
			if err := unix.Dup3(group.opened, file.rule.FD, flags); err != nil {
				return readonlyFileFailureV1(file.rule.FD, "replace its original descriptor", err)
			}
		}
	}
	return nil
}

func readonlyFileFailureV1(fd int, action string, err error) error {
	return stepFailed(fmt.Sprintf("cannot project inherited file fd %d read-only: %s", fd, action),
		"Keep the original file grants unchanged and use a host with private bind mounts and same-process KCMP_FILE support for aliases.", err)
}

func closeReadonlyFDV1(fd int) error {
	if err := unix.Close(fd); err != nil {
		return readonlyFileFailureV1(fd, "close a projection descriptor", err)
	}
	return nil
}

func snapshotReadonlyFilesV1(rules []pinnedFileRuleV1) ([]readonlyFileGroupV1, error) {
	text := make([]FileRuleV1, len(rules))
	files := 0
	for index, rule := range rules {
		if rule.FD < 3 || rule.FD > 1<<31-1 {
			return nil, failure("invalid inherited-file projection slot", "Use the original checked child Files slots.")
		}
		text[index] = rule.FileRuleV1
		files = max(files, rule.FD-2)
	}
	if _, err := checkFileRulesV1(text, files); err != nil {
		return nil, err
	}
	var snapshots []readonlyFileSnapshotV1
	for _, rule := range rules {
		if rule.Access&Write != 0 || rule.Access&(Read|Exec) == 0 {
			continue
		}
		got, err := inspectFileRuleV1(rule.FD, rule.FileRuleV1)
		if err != nil {
			return nil, err
		}
		if got.Dev != rule.Dev || got.Ino != rule.Ino {
			return nil, failure("an inherited-file projection changed inode", "Keep every original selected descriptor open until the jail starts.")
		}
		for _, other := range rules {
			if other.Dev == rule.Dev && other.Ino == rule.Ino && (other.Access != rule.Access || other.Role != rule.Role || other.Mode != rule.Mode) {
				return nil, failure("an inherited-file projection has conflicting inode aliases", "Give all inherited aliases the same exact role, mode and rights.")
			}
		}
		flags, err := unix.FcntlInt(uintptr(rule.FD), unix.F_GETFL, 0)
		if err != nil {
			return nil, readonlyFileFailureV1(rule.FD, "snapshot its open-file flags", err)
		}
		fdFlags, err := unix.FcntlInt(uintptr(rule.FD), unix.F_GETFD, 0)
		if err != nil {
			return nil, readonlyFileFailureV1(rule.FD, "snapshot close-on-exec", err)
		}
		var offset int64
		if flags&unix.O_PATH == 0 {
			offset, err = unix.Seek(rule.FD, 0, unix.SEEK_CUR)
			if err != nil {
				return nil, readonlyFileFailureV1(rule.FD, "snapshot its initial cursor", err)
			}
		}
		snapshots = append(snapshots, readonlyFileSnapshotV1{rule: rule, flags: flags, fdFlags: fdFlags, offset: offset})
	}
	var groups []readonlyFileGroupV1
	for _, snapshot := range snapshots {
		matched := false
		for index := range groups {
			first := groups[index].files[0]
			if first.rule.Dev != snapshot.rule.Dev || first.rule.Ino != snapshot.rule.Ino {
				continue
			}
			same, err := sameReadonlyOFDV1(first.rule.FD, snapshot.rule.FD)
			if err != nil {
				return nil, readonlyFileFailureV1(snapshot.rule.FD, "disambiguate same-inode OFDs before changing any slot", err)
			}
			if !same {
				continue
			}
			if first.offset != snapshot.offset || first.flags != snapshot.flags {
				return nil, failure("shared inherited-file state changed during its snapshot", "Stop concurrent use of the parent descriptors until the jail starts.")
			}
			groups[index].files = append(groups[index].files, snapshot)
			matched = true
			break
		}
		if !matched {
			groups = append(groups, readonlyFileGroupV1{files: []readonlyFileSnapshotV1{snapshot}, opened: -1})
		}
	}
	return groups, nil
}

func sameReadonlyOFDV1(first, second int) (bool, error) {
	if first < 0 || second < 0 {
		return false, unix.EBADF
	}
	pid := unix.Getpid()
	if pid <= 0 {
		return false, unix.ESRCH
	}
	order, _, errno := unix.Syscall6(unix.SYS_KCMP, uintptr(pid), uintptr(pid), readonlyKcmpFileV1, uintptr(first), uintptr(second), 0)
	if errno != 0 {
		return false, errno
	}
	return order == 0, nil
}
