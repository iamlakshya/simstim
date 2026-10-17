package bundle

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

// Options say what a bundle covers, and where it goes.
type Options struct {
	// Host is the host to cover, or nil for the laptop's side only.
	Host *client.Host
	// Session names one session of Host, by its name or its ID. Empty
	// covers every session of Host that has debug files.
	Session string
	// SSH adds the log of one new connection with ssh -v.
	SSH bool
	// Dir is the folder that the archive goes in.
	Dir string
	// Interactive lets ssh ask for a password or a host key, as a command
	// in a terminal may. The launcher never prompts.
	Interactive bool
}

// File is one file in a bundle, and its size.
type File struct {
	Name string
	Size int64
}

// Left is a file that a bundle left out, and why. From is the file on this
// computer, when its name in the bundle does not say: the command prints
// it, and replaced.txt does not.
type Left struct {
	Name string
	From string
	Why  string
}

// Count is how many values of one kind a bundle replaced.
type Count struct {
	Kind string
	N    int
}

// String is the count in words, such as 4 host names.
func (c Count) String() string {
	t := kindText[c.Kind]
	if c.N == 1 {
		return "1 " + t.one
	}
	return fmt.Sprintf("%d %s", c.N, t.many)
}

// Result is what a bundle holds. What it says of the files left out and
// of the host's notes names things as the user knows them: the bundle
// replaces values only in the archive, replaced.txt too.
type Result struct {
	// Path is the archive's path, and Size its size in bytes.
	Path string
	Size int64
	// Files are the files in it, in its order.
	Files []File
	// Left are the files that it left out, and Notes what the host could
	// not gather.
	Left  []Left
	Notes []string
	// Counts are the replacements, as replaced.txt lists them.
	Counts []Count
}

// GitHubMax is the largest .gz file that GitHub takes on an issue: 25 MB.
const GitHubMax = 25_000_000

// Largest returns the n largest files of the bundle, largest first.
func (r *Result) Largest(n int) []File {
	files := append([]File(nil), r.Files...)
	sort.SliceStable(files, func(i, j int) bool { return files[i].Size > files[j].Size })
	return files[:min(n, len(files))]
}

// The limits of a bundle: one file, all the host's files together, and
// how many files a host may list. The limits of the debug files keep a
// bundle well under them. They stop a host that sends more.
const (
	maxFile      = 64 << 20
	maxHostBytes = 512 << 20
	maxHostFiles = 4096
)

// Write writes a bundle: the laptop's side, and with o.Host the host's.
// It returns what the bundle holds. Nothing is uploaded. When ctx ends
// first, it leaves no file, and says that it stopped.
func Write(ctx context.Context, o Options) (*Result, error) {
	res, err := write(ctx, o)
	if err != nil && ctx.Err() != nil {
		return nil, &client.UserError{Msg: "the bundle stopped before it was complete, and left no file",
			Hint: brand.Name + " debug bundle" + target(o) + "   (to start it again)"}
	}
	return res, err
}

// target is the bundle's argument, for a hint.
func target(o Options) string {
	switch {
	case o.Host == nil:
		return ""
	case o.Session != "":
		return " " + o.Host.Name + "/" + o.Session
	}
	return " " + o.Host.Name
}

func write(ctx context.Context, o Options) (*Result, error) {
	b := &builder{o: o, vals: laptopValues(), res: &Result{}}
	b.configValues(ctx)
	if o.Host != nil {
		conn, err := client.Dial(ctx, *o.Host, o.Interactive)
		if err != nil {
			return nil, err
		}
		defer conn.Close()
		b.conn = conn
		if err := b.hostFacts(ctx); err != nil {
			return nil, err
		}
	}
	now := time.Now()
	a, err := newArchive(o.Dir, now)
	if err != nil {
		return nil, &client.UserError{Msg: "cannot write the bundle in " + o.Dir + ": " + err.Error(), Hint: "Run it in a folder that you can write."}
	}
	done := false
	defer func() {
		if !done {
			a.abort()
		}
	}()
	b.a, b.s = a, NewScrubber(b.vals)
	if err := b.fill(ctx, now); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("write the bundle: %w", err)
	}
	size, err := a.close()
	done = true
	if err != nil {
		return nil, err
	}
	b.res.Path, b.res.Size, b.res.Files = a.path, size, a.files
	return b.res, nil
}

// builder holds one bundle while it is written.
type builder struct {
	o      Options
	vals   Values
	res    *Result
	conn   *client.Conn
	info   client.HostInfo
	col    client.DebugCollected
	sshLog []byte
	hidden int // the alerts file's values that the host replaced
	a      *archive
	s      *Scrubber
}

// fill writes every file of the bundle.
func (b *builder) fill(ctx context.Context, now time.Time) error {
	if err := b.a.add("README.txt", readme(now)); err != nil {
		return err
	}
	if err := b.laptopFiles(ctx); err != nil {
		return err
	}
	if b.conn != nil {
		if err := b.hostFiles(ctx); err != nil {
			return err
		}
	}
	b.res.Counts = b.s.Counts()
	if b.hidden > 0 {
		b.res.Counts = append(b.res.Counts, Count{Kind: KindAlert, N: b.hidden})
	}
	return b.a.add("replaced.txt", replacedFile(b.res, b.s))
}

// put prepares a file for the bundle, and adds it, or leaves it out and
// says why.
func (b *builder) put(name string, data []byte) error {
	out, why := b.prepare(name, data)
	if why != "" {
		b.res.Left = append(b.res.Left, Left{Name: name, Why: why})
		return nil
	}
	return b.a.add(name, out)
}

// prepare replaces the private values in a file, by its kind, then checks
// it again. It returns why it leaves the file out.
func (b *builder) prepare(name string, data []byte) ([]byte, string) {
	switch {
	case strings.HasSuffix(name, ".pprof"):
		out, table, err := rewriteProfile(data, b.s.rewritePath)
		if err != nil {
			return nil, "the bundle cannot read it as a profile: " + err.Error()
		}
		for _, str := range table {
			if k := b.s.checkPath(str.s, str.file); k != "" {
				return nil, "after the replacements, it still holds " + kindText[k].a
			}
		}
		return out, ""
	case strings.HasSuffix(name, ".trace"):
		if b.s.holdsHome(data) {
			return nil, "it holds a home folder: the program that saved it was built without -trimpath"
		}
		return data, ""
	}
	out := b.s.Scrub(data)
	if k := b.s.Check(out); k != "" {
		return nil, "after the replacements, it still holds " + kindText[k].a
	}
	return out, ""
}

// hostFiles adds the host's side: host doctor, the ssh -v log, and each
// file that the host listed. A name from the host must be a plain path
// under host/, and the host's files together stay under maxHostBytes.
func (b *builder) hostFiles(ctx context.Context) error {
	if err := b.put("host/doctor.json", jsonText(b.info)); err != nil {
		return err
	}
	if b.sshLog != nil {
		if err := b.put("host/ssh.log", b.sshLog); err != nil {
			return err
		}
	}
	var total int64
	for i, f := range b.col.Files {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("read the host's files: %w", err)
		}
		if err := checkEntryName(f.Path); err != nil || !strings.HasPrefix(f.Path, "host/") || i >= maxHostFiles {
			b.res.Left = append(b.res.Left, Left{Name: fmt.Sprintf("%q", f.Path), Why: "the host gave a name that is not a plain path under host/, or too many files"})
			continue
		}
		if total >= maxHostBytes {
			b.res.Left = append(b.res.Left, Left{Name: f.Path, Why: fmt.Sprintf("the host's files reached the bundle's limit of %d MiB", maxHostBytes>>20)})
			continue
		}
		data, hidden, err := b.conn.DebugReadFile(ctx, f, min(maxFile, maxHostBytes-total))
		if ctx.Err() != nil {
			return fmt.Errorf("read the host's files: %w", ctx.Err())
		}
		if errors.Is(err, client.ErrClosed) {
			return &client.UserError{Msg: "the connection to " + b.o.Host.Name + " closed while the bundle read its files",
				Hint: brand.Name + " debug bundle" + target(b.o) + "   (try again)"}
		}
		if err != nil {
			b.res.Left = append(b.res.Left, Left{Name: f.Path, Why: "the host could not send it: " + printable(err.Error())})
			continue
		}
		total += int64(len(data))
		b.hidden += hidden
		if err := b.put(f.Path, data); err != nil {
			return err
		}
	}
	for _, n := range b.col.Notes {
		b.res.Notes = append(b.res.Notes, printable(n))
	}
	return nil
}

// printable is text from the host with each character that a terminal
// could take as a control code turned into ?.
func printable(s string) string {
	return strings.Map(func(r rune) rune {
		if r < ' ' || r == 0x7f || r >= 0x80 && r < 0xa0 {
			return '?'
		}
		return r
	}, s)
}

// pick finds a session by its ID, or by a name that only it has.
func pick(list []client.Session, ref, host string) (client.Session, error) {
	var byName []client.Session
	for _, s := range list {
		if s.ID == ref {
			return s, nil
		}
		if s.Name == ref {
			byName = append(byName, s)
		}
	}
	switch len(byName) {
	case 1:
		return byName[0], nil
	case 0:
		return client.Session{}, &client.UserError{Msg: fmt.Sprintf("no session %q on %s", ref, host), Hint: brand.Name + " ls --host " + host}
	}
	return client.Session{}, &client.UserError{Msg: fmt.Sprintf("%d sessions are named %q on %s", len(byName), ref, host),
		Hint: "Use the session ID from `" + brand.Name + " ls`."}
}
