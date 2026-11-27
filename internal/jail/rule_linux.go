package jail

import (
	"errors"

	"golang.org/x/sys/unix"
)

// verifyRules reopens every rule before init confines either mode. A rule
// cannot move from a read-only bind into a writable one between stages.
func verifyRules(c *setup, fds []int) error {
	for _, r := range pinnedRules(c.Rules) {
		fd, err := reopenRule(c, fds, r)
		if err != nil {
			return err
		}
		_ = unix.Close(fd)
	}
	return nil
}

func reopenRule(c *setup, fds []int, r rule) (int, error) {
	var pinned unix.Stat_t
	err := unix.Fstat(r.FD, &pinned)
	if err == nil && (pinned.Dev != r.Dev || pinned.Ino != r.Ino) {
		err = errors.New("the inherited rule descriptor names another file")
	}
	if err == nil {
		err = samePath(r.FD, r.Real)
	}
	if err != nil {
		return -1, pinError("the pinned Landlock rule's path "+quote(r.Path)+" changed",
			"Do not change a jail's paths between jail.Command and Start.", err)
	}
	paths := make([]string, len(c.Binds))
	for i, b := range c.Binds {
		paths[i] = b.Path
	}
	at := holder(paths, r.Path)
	if at < 0 || at >= len(fds) {
		return -1, failure("the Landlock rule's path "+quote(r.Path)+" has no bind",
			"Keep each rule beneath one of the jail's binds.")
	}
	fd, err := openBeneath(fds[at], paths[at], r.Path)
	var st unix.Stat_t
	if err == nil {
		err = unix.Fstat(fd, &st)
	}
	if err == nil && (st.Dev != r.Dev || st.Ino != r.Ino) {
		err = errors.New("it names another file than when jail.Command pinned it")
	}
	if err == nil {
		err = samePath(fd, r.Real)
	}
	if err != nil {
		if fd >= 0 {
			_ = unix.Close(fd)
		}
		return -1, pinError("the Landlock rule's path "+quote(r.Path)+" changed",
			"Do not change a jail's paths between jail.Command and Start.", err)
	}
	return fd, nil
}
