package jail

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

func readonlyStageRulesV1(c *setup) ([]pinnedFileRuleV1, error) {
	if err := checkStageFileRulesV1(c); err != nil {
		return nil, err
	}
	rules := slices.Clone(c.FileRulesV1)
	if c.ExecFD <= 0 {
		return rules, nil
	}
	for _, rule := range rules {
		if rule.FD == c.ExecFD {
			if rule.Role != FileProgramV1 || rule.Access&Exec == 0 {
				return nil, failure("the inherited executable conflicts with its selected data-file role", "Give the executable its exact program role and Exec grant.")
			}
			return rules, nil
		}
	}
	if c.ExecFD < 3 || c.ExecFD-3 >= c.Files {
		return nil, failure("the inherited executable escaped its actual Files vector", "Use the original checked executable slot.")
	}
	var st unix.Stat_t
	if err := unix.Fstat(c.ExecFD, &st); err != nil {
		return nil, stepFailed("cannot inspect the inherited executable", "Keep the original executable descriptor open.", err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&0o7000 != 0 {
		return nil, failure("the inherited executable is not an ordinary regular file", "Use the original checked executable descriptor.")
	}
	flags, err := unix.FcntlInt(uintptr(c.ExecFD), unix.F_GETFL, 0)
	if err != nil {
		return nil, err
	}
	if flags&unix.O_PATH == 0 && flags&unix.O_ACCMODE == unix.O_WRONLY {
		return nil, failure("a write-only descriptor cannot become a readonly executable grant", "Pass the original executable capability with its existing rights.")
	}
	access := Exec
	if flags&unix.O_PATH == 0 {
		access |= Read
	}
	rule := FileRuleV1{FD: c.ExecFD, Role: FileProgramV1, Access: access, Mode: os.FileMode(st.Mode & 0o777)}
	text := make([]FileRuleV1, 0, len(rules)+1)
	for _, existing := range rules {
		text = append(text, existing.FileRuleV1)
	}
	text = append(text, rule)
	if _, err := checkFileRulesV1(text, c.Files); err != nil {
		return nil, err
	}
	pinned, err := inspectFileRuleV1(c.ExecFD, rule)
	if err != nil {
		return nil, err
	}
	return append(rules, pinned), nil
}

func verifyInitPrivateStateV1() error {
	dumpable, err := unix.PrctlRetInt(unix.PR_GET_DUMPABLE, 0, 0, 0, 0)
	if err != nil || dumpable != 0 {
		return stepFailed("the actual jail init is dumpable", "Keep its original owner and inspect the host's confinement support.", err)
	}
	header := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	var data [2]unix.CapUserData
	if err := unix.Capget(&header, &data[0]); err != nil {
		return err
	}
	for _, part := range data {
		if part.Effective != 0 || part.Permitted != 0 || part.Inheritable != 0 {
			return failure("the actual jail init still has process capabilities", "Keep the original jail owner and inspect its confinement setup.")
		}
	}
	return nil
}

func closeInitInheritedV1(files []*os.File) error {
	var failure error
	for _, file := range files {
		if file != nil {
			failure = errors.Join(failure, file.Close())
		}
	}
	return failure
}

// checkReadonlyBindAliasesV1 applies the existing native backing/alias checker
// to readonly inherited regular files too. A writable bind must not provide a
// second path to the same protected object after its FD is projected read-only.
func checkReadonlyBindAliasesV1(c *setup, fds []int, rules []pinnedFileRuleV1) error {
	var binds []bind
	var files []*os.File
	defer func() {
		for _, file := range files {
			_ = file.Close()
		}
	}()
	for index, bound := range c.Binds {
		if index >= len(fds) {
			return failure("the checked bind vector is incomplete", "Retain the original jail setup.")
		}
		fd, err := unix.FcntlInt(uintptr(fds[index]), unix.F_DUPFD_CLOEXEC, 3)
		if err != nil {
			return err
		}
		binds = append(binds, bound)
		files = append(files, os.NewFile(uintptr(fd), "checked-bind-alias"))
	}
	for _, rule := range rules {
		if rule.Access&Write != 0 {
			continue
		}
		got, err := inspectFileRuleV1(rule.FD, rule.FileRuleV1)
		if err != nil || got.Dev != rule.Dev || got.Ino != rule.Ino {
			return errors.Join(failure("a readonly inherited file changed before alias checking", "Keep the original Files vector unchanged."), err)
		}
		for _, bound := range c.Binds {
			if bound.Write && bound.Dev == rule.Dev && bound.Ino == rule.Ino {
				return failure("a writable bind aliases a readonly inherited file", "Remove the writable alias before starting the jail.")
			}
		}
		var st unix.Stat_t
		if err := unix.Fstat(rule.FD, &st); err != nil {
			return err
		}
		if st.Nlink == 0 {
			continue
		} // No directory entry survives; direct mounted aliases were checked above.
		real, err := os.Readlink("/proc/self/fd/" + strconv.Itoa(rule.FD))
		if err != nil || strings.HasSuffix(real, " (deleted)") {
			return errors.Join(failure("a readonly inherited file lost its checked backing path", "Keep the original file bound until the jail starts."), err)
		}
		fd, err := unix.FcntlInt(uintptr(rule.FD), unix.F_DUPFD_CLOEXEC, 3)
		if err != nil {
			return err
		}
		binds = append(binds, bind{Path: fmt.Sprintf("/%s/%d", readonlyFilesPrefixV1, rule.FD), Kind: kindFile, Dev: rule.Dev, Ino: rule.Ino, Real: real})
		files = append(files, os.NewFile(uintptr(fd), "readonly-file-alias"))
	}
	return checkBackingBindAliases(binds, files)
}
