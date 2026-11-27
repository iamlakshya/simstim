package jail

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

type pinnedFileRuleV1 struct {
	FileRuleV1
	Dev uint64 `json:"dev"`
	Ino uint64 `json:"ino"`
}

func snapshotFileRulesV1(rules []FileRuleV1, files []*os.File) ([]pinnedFileRuleV1, error) {
	if _, err := checkFileRulesV1(rules, len(files)); err != nil {
		return nil, err
	}
	var pinned []pinnedFileRuleV1
	for _, rule := range rules {
		file, err := captureFile(files[rule.FD-3], "file rule", "Keep each selected Spec.Files handle open until Start.")
		if err != nil {
			return nil, err
		}
		var identity pinnedFileRuleV1
		raw, err := file.SyscallConn()
		var inspectErr error
		if err == nil {
			err = raw.Control(func(fd uintptr) { identity, inspectErr = inspectFileRuleV1(int(fd), rule) })
		}
		file.Close()
		if err := errors.Join(err, inspectErr); err != nil {
			return nil, err
		}
		pinned = append(pinned, identity)
	}
	return pinned, nil
}

func inspectFileRuleV1(fd int, rule FileRuleV1) (pinnedFileRuleV1, error) {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return pinnedFileRuleV1{}, stepFailed("cannot inspect an inherited-file rule", "Keep the selected Files handle open.", err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&0o7777 != uint32(rule.Mode) {
		return pinnedFileRuleV1{}, failure("an inherited-file rule is not its exact regular file mode", "Pass only the checked regular file or memfd named by that rule.")
	}
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
	if err != nil {
		return pinnedFileRuleV1{}, stepFailed("cannot read an inherited-file access mode", "Keep the selected Files handle open.", err)
	}
	access := flags & unix.O_ACCMODE
	if rule.Access&Read != 0 && (flags&unix.O_PATH != 0 || access == unix.O_WRONLY) || rule.Access&Write != 0 && (flags&unix.O_PATH != 0 || access == unix.O_RDONLY) {
		return pinnedFileRuleV1{}, failure("an inherited-file rule exceeds its captured descriptor's access", "Grant only access already held by that Files descriptor.")
	}
	return pinnedFileRuleV1{FileRuleV1: rule, Dev: st.Dev, Ino: st.Ino}, nil
}

func checkCapturedFileRulesV1(rules []pinnedFileRuleV1, files []*os.File) error {
	if len(rules) == 0 {
		return nil
	}
	for _, rule := range rules {
		if rule.FD < 3 || rule.FD-3 >= len(files) {
			return failure("an inherited-file rule escaped the final Files vector", "Report this as a bug.")
		}
		raw, err := files[rule.FD-3].SyscallConn()
		var inspectErr error
		if err == nil {
			err = raw.Control(func(fd uintptr) {
				got, e := inspectFileRuleV1(int(fd), rule.FileRuleV1)
				inspectErr = e
				if e == nil && (got.Dev != rule.Dev || got.Ino != rule.Ino) {
					inspectErr = failure("an inherited-file rule changed descriptor identity before Start", "Keep the original selected file open and unchanged until Start.")
				}
			})
		}
		if err := errors.Join(err, inspectErr); err != nil {
			return err
		}
	}
	// Landlock grants inode rights. A second inherited alias must not make
	// a slot appear ungranted, or carry a conflicting role/right set.
	for slot, file := range files {
		raw, err := file.SyscallConn()
		var inspectErr error
		if err == nil {
			err = raw.Control(func(fd uintptr) {
				var st unix.Stat_t
				if e := unix.Fstat(int(fd), &st); e != nil {
					inspectErr = e
					return
				}
				for _, selected := range rules {
					if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Dev != selected.Dev || st.Ino != selected.Ino || slot+3 == selected.FD {
						continue
					}
					matching := false
					for _, other := range rules {
						if other.FD == slot+3 && other.Access == selected.Access && other.Role == selected.Role && other.Mode == selected.Mode {
							matching = true
						}
					}
					if !matching {
						inspectErr = failure("an inherited-file rule has an ungranted or conflicting inode alias", "Use one selected Files slot per object, or give every alias the same exact role and rights.")
						return
					}
				}
			})
		}
		if err := errors.Join(err, inspectErr); err != nil {
			return fmt.Errorf("check final inherited-file aliases: %w", err)
		}
	}
	return nil
}

func checkStageFileRulesV1(c *setup) error {
	text := make([]FileRuleV1, len(c.FileRulesV1))
	for i, rule := range c.FileRulesV1 {
		text[i] = rule.FileRuleV1
	}
	if _, err := checkFileRulesV1(text, c.Files); err != nil {
		return err
	}
	for _, rule := range c.FileRulesV1 {
		got, err := inspectFileRuleV1(rule.FD, rule.FileRuleV1)
		if err != nil {
			return err
		}
		if got.Dev != rule.Dev || got.Ino != rule.Ino {
			return failure("a jail stage inherited another file for a selected rule", "Start jails with jail.Command, not by hand.")
		}
	}
	return nil
}
