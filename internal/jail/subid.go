package jail

import (
	"bufio"
	"io"
	"strconv"
	"strings"
)

// subID is the subordinate user and group that a jail runs its command as,
// and the programs that map them into its user namespace. Inside, the login
// user is 0 and the subordinate IDs are 1.
type subID struct {
	UID       int    `json:"uid"`     // the login user's
	GID       int    `json:"gid"`     // the login user's primary group
	SubUID    int    `json:"sub_uid"` // the first ID of the user's first range in /etc/subuid
	SubGID    int    `json:"sub_gid"` // the same, in /etc/subgid
	NewUIDMap string `json:"newuidmap"`
	NewGIDMap string `json:"newgidmap"`
}

// firstRange reads an /etc/subuid or /etc/subgid file, and returns the
// start of the first range that it gives the user, named by name or by
// number. Each line is "user:start:count". The file is plain text that root
// writes, so a line that does not parse is skipped.
func firstRange(r io.Reader, name string, id int) (int, bool) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, ":")
		if len(f) != 3 || !names(f[0], name, id) {
			continue
		}
		start, err1 := strconv.Atoi(f[1])
		count, err2 := strconv.Atoi(f[2])
		if err1 != nil || err2 != nil || start <= 0 || count < 1 {
			continue
		}
		return start, true
	}
	return 0, false
}

// names says whether a line's first field names the user.
func names(field, name string, id int) bool {
	return (name != "" && field == name) || field == strconv.Itoa(id)
}
