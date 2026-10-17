package bundle

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/diag"
	"github.com/iamlakshya/simstim/internal/version"
)

// hostname is the laptop's name. Tests replace it.
var hostname = os.Hostname

// laptopValues are the laptop's private values: its name, its user and the
// home folder.
func laptopValues() Values {
	var v Values
	if h, err := hostname(); err == nil && h != "" {
		v.Laptop = append(v.Laptop, h)
		if short, _, ok := strings.Cut(h, "."); ok {
			v.Laptop = append(v.Laptop, short)
		}
	}
	if u, err := user.Current(); err == nil {
		v.Users = append(v.Users, u.Username)
		v.Homes = append(v.Homes, u.HomeDir)
	}
	for _, k := range []string{"USER", "LOGNAME"} {
		if x := os.Getenv(k); x != "" {
			v.Users = append(v.Users, x)
		}
	}
	if h, err := os.UserHomeDir(); err == nil {
		v.Homes = append(v.Homes, h)
	}
	return v
}

// configValues adds each host's name and SSH target in hosts.toml. For a
// host reached with SSH, it adds what `ssh -G` says too: the host's real
// name, the jump hosts, the user and the identity files. That runs no
// connection.
func (b *builder) configValues(ctx context.Context) {
	hosts, err := client.LoadHosts()
	if err != nil {
		return
	}
	for _, h := range hosts {
		b.vals.Hosts = append(b.vals.Hosts, h.Name, h.SSH)
		if h.Machine != nil && !strings.Contains(h.Machine.ID, "/") {
			b.vals.Hosts = append(b.vals.Hosts, h.Machine.ID)
		}
		if h.Machine == nil && h.SSH != "" {
			b.sshValues(ctx, h)
		}
	}
}

// sshValues adds the values of `ssh -G` for a host.
func (b *builder) sshValues(ctx context.Context, h client.Host) {
	vals, err := (&client.SSH{Host: h}).ConfigValues(ctx)
	if err != nil {
		return
	}
	b.vals.Hosts = append(b.vals.Hosts, vals["hostname"]...)
	b.vals.Users = append(b.vals.Users, vals["user"]...)
	b.vals.Keys = append(b.vals.Keys, vals["identityfile"]...)
	for _, j := range vals["proxyjump"] {
		if j != "none" {
			b.vals.Hosts = append(b.vals.Hosts, strings.Split(j, ",")...)
		}
	}
}

// hostFacts reads what the bundle needs from the host before it writes
// anything: host doctor, the sessions, the host's list of files and, with
// --ssh, the ssh -v log.
func (b *builder) hostFacts(ctx context.Context) error {
	info, err := b.conn.Doctor(ctx)
	if err != nil {
		return err
	}
	b.info = info
	b.vals.Hosts = append(b.vals.Hosts, info.Hostname)
	if short, _, ok := strings.Cut(info.Hostname, "."); ok {
		b.vals.Hosts = append(b.vals.Hosts, short)
	}
	b.vals.Users = append(b.vals.Users, info.User)
	sessions, err := b.conn.Sessions(ctx)
	if err != nil {
		return err
	}
	for _, s := range sessions {
		b.vals.Sessions = append(b.vals.Sessions, s.Name)
		b.vals.Folders = append(b.vals.Folders, s.Cwd)
	}
	var ids []string
	if b.o.Session != "" {
		s, err := pick(sessions, b.o.Session, b.o.Host.Name)
		if err != nil {
			return err
		}
		ids = []string{s.ID}
	}
	if b.col, err = b.conn.DebugCollect(ctx, ids, len(ids) == 0); err != nil {
		return err
	}
	b.vals.Homes = append(b.vals.Homes, b.col.Home)
	if b.o.SSH {
		b.sshFacts(ctx)
	}
	return nil
}

// keyRe finds where ssh -v names a key: the text before the key's type,
// which is a path or the key's comment.
var keyRe = regexp.MustCompile(`(?m)(?:Will attempt key|Offering public key|Server accepts key|Trying private key): (.+?) [A-Z][A-Z0-9-]* SHA256:`)

// secretLine is a line of ssh -v that can hold a secret: the value of an
// environment variable that SendEnv or SetEnv sends, or a proxy's command
// line, which can hold a token.
var secretLine = regexp.MustCompile(`(?m)^.*(?:[Ss]etting env |[Ss]ending env |[Ee]xecuting proxy|[Ee]xecuting command|[Ee]xecuting local command|[Pp]roxy command).*$`)

// dropSecretLines leaves out each line of an ssh -v log that can hold a
// secret, and says so in its place.
func dropSecretLines(log []byte) []byte {
	return secretLine.ReplaceAll(log, []byte("(a line that can hold a secret, left out by the bundle)"))
}

// sshPath is a place where ssh -v names a file: before, the file's path,
// and after. A path can hold a space, so each runs to a fixed end. The
// file's placeholder, and the names of the usual files of its kind.
type sshPath struct {
	re    *regexp.Regexp
	ph    string
	usual []string
}

// sshPaths are where ssh -v names a config file: as it reads one, as it
// includes one, before the line of one that applies, and an include that
// matched nothing. They are also where it names a file of known hosts,
// whatever its name: as it finds a key in one, and as it opens one. The
// last is any other path of a known_hosts file.
var sshPaths = []sshPath{
	{regexp.MustCompile(`(?m)(Reading configuration data )(.+)()$`), "<ssh-config>", []string{"config"}},
	{regexp.MustCompile(`(Including file )(.+?)( depth [0-9]+)`), "<ssh-config>", []string{"config"}},
	{regexp.MustCompile(`(include )(.+?)( matched no files)`), "<ssh-config>", nil},
	{regexp.MustCompile(`(?m)^(debug[0-9]: )(.+?)( line [0-9]+:)`), "<ssh-config>", []string{"config"}},
	{regexp.MustCompile(`(?m)(Found (?:CA )?key in )(.+)(:[0-9]+)$`), "<known-hosts>", []string{"known_hosts", "known_hosts2"}},
	{regexp.MustCompile(`(fopen )(.+?)(: )`), "<known-hosts>", []string{"known_hosts", "known_hosts2"}},
	{regexp.MustCompile(`()(~?/[^\s:'"]*known_hosts[^\s:'"]*)()`), "<known-hosts>", []string{"known_hosts", "known_hosts2"}},
}

// hideConfigPaths replaces the path of each config file and each file of
// known hosts in an ssh -v log, but the usual ones and the client's own: a
// file kept in a project's folder names the project.
func hideConfigPaths(log []byte, homes []string) []byte {
	usual := func(p string, names []string) bool {
		if strings.HasPrefix(p, "/etc/ssh/") || strings.HasPrefix(p, client.ConfigDir()+"/ssh/") || placeholderRe.MatchString(p) {
			return true
		}
		for _, n := range names {
			if p == "~/.ssh/"+n || slices.ContainsFunc(homes, func(h string) bool { return h != "" && p == filepath.Join(h, ".ssh", n) }) {
				return true
			}
		}
		return false
	}
	for _, sp := range sshPaths {
		log = sp.re.ReplaceAllFunc(log, func(m []byte) []byte {
			sub := sp.re.FindSubmatch(m)
			if usual(string(sub[2]), sp.usual) {
				return m
			}
			return slices.Concat(sub[1], []byte(sp.ph), sub[3])
		})
	}
	return log
}

// verboseLog is the ssh -v log of one new connection to h. A host that a
// provider made has none. Tests replace it.
var verboseLog = func(ctx context.Context, h client.Host) ([]byte, error) {
	if h.Machine != nil {
		return nil, fmt.Errorf("%s is reached through its provider, not SSH", h.Name)
	}
	return (&client.SSH{Host: h}).VerboseLog(ctx)
}

// sshFacts runs one ssh -v connection, for host/ssh.log, and adds the
// names of the keys that it names.
func (b *builder) sshFacts(ctx context.Context) {
	log, err := verboseLog(ctx, *b.o.Host)
	if err != nil {
		b.res.Notes = append(b.res.Notes, "no ssh -v log: "+err.Error())
		return
	}
	b.sshLog = hideConfigPaths(dropSecretLines(log), b.vals.Homes)
	for _, m := range keyRe.FindAllSubmatch(b.sshLog, -1) {
		b.vals.Keys = append(b.vals.Keys, string(m[1]))
	}
}

// laptopFiles adds the laptop's side: doctor, the files in the debug folder
// whose names the debug mode gives them, and the debug files outside it
// that outside.txt lists.
func (b *builder) laptopFiles(ctx context.Context) error {
	if err := b.put("laptop/doctor.txt", doctorText()); err != nil { //nolint:contextcheck // doctor's local checks take no context: they read files and run ssh -V
		return err
	}
	dir := client.DebugDir()
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("read the laptop's files: %w", err)
		}
		if diag.IsDebugName(e.Name()) {
			if err := b.putFile("laptop/"+e.Name(), filepath.Join(dir, e.Name())); err != nil {
				return err
			}
		}
	}
	return b.outsideFiles(ctx, dir)
}

// outsideFiles adds the debug files outside the debug folder that
// outside.txt lists, and the older part of each. Their names in the
// archive are numbers, as their own names can name a project. Each must
// start with a record of the debug mode: --debug-file can name a file of
// the user's own, and the client then adds its records to that file.
func (b *builder) outsideFiles(ctx context.Context, dir string) error {
	list, _ := diag.ReadPlain(filepath.Join(dir, diag.OutsideList), 1<<20)
	n := 0
	sc := bufio.NewScanner(bytes.NewReader(list))
	for sc.Scan() {
		p := strings.TrimSpace(sc.Text())
		if !filepath.IsAbs(p) {
			continue
		}
		n++
		for i, q := range []string{p, p + ".1"} {
			if err := ctx.Err(); err != nil {
				return fmt.Errorf("read the laptop's files: %w", err)
			}
			if _, err := os.Lstat(q); err != nil {
				continue
			}
			name := fmt.Sprintf("laptop/outside/%d.jsonl", n)
			if i == 1 {
				name += ".1"
			}
			data, why := readLaptop(q)
			if why == "" && !isRecords(data) {
				why = "it holds more than records of the debug mode"
			}
			// Only whole lines go: what follows the last line break is a
			// record still being written, or something else.
			data = data[:bytes.LastIndexByte(data, '\n')+1]
			if why != "" {
				b.res.Left = append(b.res.Left, Left{Name: name, From: q, Why: why})
				continue
			}
			if err := b.put(name, data); err != nil {
				return err
			}
		}
	}
	return nil
}

// isRecords reports whether each whole line of data is a record of the
// debug mode: a JSON object with the keys t, a time, ev, an event, and cat,
// a category or none, spelled as the debug mode spells them. A last line
// that is cut short, as in a file still being written, does not count.
func isRecords(data []byte) bool {
	lines := bytes.Split(data, []byte("\n"))
	lines = lines[:len(lines)-1] // the part after the last line break
	for _, line := range lines {
		var r map[string]json.RawMessage
		if json.Unmarshal(line, &r) != nil {
			return false
		}
		var t int64
		var ev, cat string
		if json.Unmarshal(r["t"], &t) != nil || json.Unmarshal(r["ev"], &ev) != nil || json.Unmarshal(r["cat"], &cat) != nil ||
			t <= 0 || ev == "" || cat != "" && !diag.AllCats.Has(cat) {
			return false
		}
	}
	return len(lines) > 0
}

// putFile reads a file of the laptop, and puts it in the bundle.
func (b *builder) putFile(name, path string) error {
	data, why := readLaptop(path)
	if why != "" {
		b.res.Left = append(b.res.Left, Left{Name: name, Why: why})
		return nil
	}
	return b.put(name, data)
}

// readLaptop reads a file of the laptop, or says why it cannot, without
// its path: a path can name a project. It reads only a regular file with
// one name, and follows no link: a link in place of a debug file must not
// ship what it points to.
func readLaptop(path string) ([]byte, string) {
	data, err := diag.ReadPlain(path, maxFile)
	if err != nil {
		for errors.Unwrap(err) != nil {
			err = errors.Unwrap(err)
		}
		return nil, "cannot read it: " + err.Error()
	}
	return data, ""
}

// doctorText is what `doctor` prints about this computer.
func doctorText() []byte {
	var out bytes.Buffer
	fmt.Fprintf(&out, "# %s doctor, %s %s\n", brand.Name, brand.Name, version.Version)
	for _, c := range client.LocalChecks() {
		mark := "✓"
		if !c.OK {
			mark = "✕"
		}
		fmt.Fprintf(&out, "%s %s: %s\n", mark, c.Name, c.Detail)
		if c.Fix != "" {
			fmt.Fprintf(&out, "  fix: %s\n", c.Fix)
		}
	}
	for _, f := range client.LocalFixes(false, nil) {
		fmt.Fprintf(&out, "✕ %s\n", f.Problem)
	}
	return out.Bytes()
}

// jsonText is v as indented JSON.
func jsonText(v any) []byte {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return []byte(err.Error())
	}
	return append(b, '\n')
}
