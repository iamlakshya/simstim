package client

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The paste guard (spec 7.6, guarantee G10) keeps a drop from uploading a
// private file, or a laptop file whose path a session wrote to the
// clipboard in the last day. It reads every byte that goes to the terminal
// for clipboard writes (clip_reader.go), and never changes one. It adds no
// key of its own: it acts only on a paste that the client already turns
// into an upload. On a refusal, the paste goes to the session as the
// terminal sent it.

// tryHint is the hint of every refusal.
const tryHint = " Try: copy the file to the host yourself."

// pasteGuard is the guard of one attach. One goroutine uses it at a time.
// It reads every byte with both readings of spec 7.6, Ghostty's and
// xterm's with C1 controls on, from the start of the attach. A drop is
// refused when any reading refuses it.
type pasteGuard struct {
	readers []*clipReader
	mem     *clipMemory
	now     func() time.Time
	// The writes that the readers saw in this part of the output, and the
	// latest time until which a reader that lost track of a write refuses
	// every drop. They go to the memory in one write before the output
	// goes on.
	part clipWrites
}

// newPasteGuard starts the guard of an attach. It takes the state that the
// last attach in this process left of the terminal, such as a kitty
// transfer that has not ended, for each reading.
func newPasteGuard(mem *clipMemory) *pasteGuard {
	g := &pasteGuard{mem: mem, now: mem.now}
	g.readers = newReadings(g.collect, g.markUnsure, mem.now, mem.takeTerms())
	return g
}

// clipReadings is how many readings the guard reads with.
const clipReadings = 2

// newReadings makes a reader for each reading: Ghostty's, then xterm's.
// Each records with its index. terms holds the terminal's state of each,
// and a missing one is new.
func newReadings(record func(reading int, sel bool, sums ...[32]byte) error, unsure func(time.Time),
	now func() time.Time, terms []*clipTerm) []*clipReader {
	out := make([]*clipReader, clipReadings)
	for i := range out {
		var t *clipTerm
		if i < len(terms) {
			t = terms[i]
		}
		out[i] = newClipReader(func(sel bool, sums ...[32]byte) error { return record(i, sel, sums...) }, now, t)
		out[i].unsure = unsure
	}
	out[1].readAsXterm()
	return out
}

// collect keeps the hashes of a write until the end of the part.
func (g *pasteGuard) collect(reading int, sel bool, sums ...[32]byte) error {
	g.part.add(reading, sel, sums...)
	return nil
}

// markUnsure keeps, until the end of the part, the time until which a
// reader that lost track of a write refuses every drop.
func (g *pasteGuard) markUnsure(until time.Time) { g.part.unsureUntil(until) }

// flush writes the hashes of this part, and any time until which every
// drop is refused, to the memory, which appends them to the journal. When
// the journal cannot take them, every reading lost track of the writes:
// the guard tries once more to note that alone, and refuses every drop in
// this process for a day in any case.
func (g *pasteGuard) flush() {
	if g.part.empty() {
		return
	}
	err := g.mem.addBatch(&g.part)
	g.part.reset()
	if err == nil {
		return
	}
	now := g.now()
	for _, r := range g.readers {
		r.term.orphanAt = now
	}
	_ = g.mem.addBatch(&clipWrites{unsure: now.Add(clipMaxAge)})
}

// detach ends the guard of an attach: an unfinished kitty transfer counts
// as a write as far as it went, and the terminal's state of each reading,
// with the time when it lost track of a write, waits for the next attach
// in this process.
func (g *pasteGuard) detach() {
	terms := make([]*clipTerm, len(g.readers))
	for i, r := range g.readers {
		r.detach()
		terms[i] = r.term
	}
	g.flush()
	g.mem.keepTerms(terms)
}

// observe reads bytes on their way to the terminal, with every reading,
// and notes the writes in them before it returns.
func (g *pasteGuard) observe(b []byte) {
	if g == nil {
		return
	}
	for _, r := range g.readers {
		r.observe(b)
	}
	g.flush()
}

// idle reports whether the client may write bytes of its own to the
// terminal without cutting a sequence or a character, in every reading.
func (g *pasteGuard) idle() bool {
	if g == nil {
		return true
	}
	for _, r := range g.readers {
		if !r.idle() {
			return false
		}
	}
	return true
}

// copying reports whether an iTerm2 copy runs in any reading.
func (g *pasteGuard) copying() bool {
	if g == nil {
		return false
	}
	for _, r := range g.readers {
		if r.copy != nil {
			return true
		}
	}
	return false
}

// orphaned reports whether any reading lost track of a write in the last day.
func (g *pasteGuard) orphaned() bool {
	for _, r := range g.readers {
		if r.orphaned() {
			return true
		}
	}
	return false
}

// inProgress is the hashes of every reading's writes that have not ended.
func (g *pasteGuard) inProgress() [][32]byte {
	var out [][32]byte
	for _, r := range g.readers {
		out = append(out, r.inProgress()...)
	}
	return out
}

// dropped is the files of a drop that may be uploaded, open for reading.
type dropped struct {
	files []*os.File
}

func (d *dropped) close() {
	for _, f := range d.files {
		_ = f.Close()
	}
}

// check opens the files of a drop, or returns the notice that says why the
// paste goes in as text. text is the paste, and paths its words.
func (g *pasteGuard) check(text []byte, paths []string) (*dropped, string) {
	d := &dropped{}
	for _, p := range paths {
		f, why := openDropped(p)
		if why != "" {
			d.close()
			return nil, refusalNotice(paths, p, why)
		}
		d.files = append(d.files, f)
	}
	if g == nil {
		return d, ""
	}
	if g.orphaned() {
		d.close()
		return nil, refusalNotice(paths, "", "may be a path that a session put on your clipboard: in the last day, "+
			"this client lost track of a clipboard write, such as kitty data that joins a transfer that it did "+
			"not see start")
	}
	// The text to compare, once for the drop: the whole paste, and each
	// path. names holds the path of each hash, or "" for the whole paste.
	var sums [][32]byte
	var names []string
	for i, t := range append([]string{string(text)}, paths...) {
		if s, ok := keySum(t); ok {
			name := t
			if i == 0 {
				name = ""
			}
			sums, names = append(sums, s), append(names, name)
		}
	}
	why, match := g.mem.check(sums, g.inProgress())
	if why == "" {
		return d, ""
	}
	d.close()
	name := ""
	if match >= 0 {
		name = names[match]
	}
	return nil, refusalNotice(paths, name, why)
}

// refusalNotice says that a drop went in as text, and why. name is the
// file that the reason is about, or "" for the whole drop.
func refusalNotice(paths []string, name, why string) string {
	what := "it"
	switch {
	case len(paths) > 1 && name != "":
		what = tildePath(name)
	case len(paths) > 1:
		what = "the paste"
	}
	files := tildePath(paths[0])
	if len(paths) > 1 {
		files = fmt.Sprintf("%d files", len(paths))
	}
	return fmt.Sprintf("did not upload %s: %s %s. The paste went in as text.%s", files, what, why, tryHint)
}

// readDropped reads an open file, up to limit bytes. ok is false when it
// holds more.
func readDropped(f *os.File, limit int) (data []byte, ok bool, err error) {
	data, err = io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil {
		return nil, false, fmt.Errorf("read %s: %w", f.Name(), err)
	}
	return data, len(data) <= limit, nil
}

// privateFolders are the folders in the home folder whose files are never
// uploaded, and privateNames the names (spec 7.6).
var privateFolders = []string{".ssh", ".aws", ".config", ".gnupg", ".kube", ".docker", "Library/Keychains"}

// openDropped opens a dropped file for upload, or says why it must stay on
// the laptop. It checks the path as given and the path with every link
// resolved, by the identity of the folders on them, so another spelling of
// a private folder, a link to it and a private folder that is a link all
// count. It then opens the resolved path without following a link, checks
// that it opened the regular file that it checked, and returns it, so the
// upload reads those bytes.
func openDropped(path string) (*os.File, string) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, "could not be checked (" + err.Error() + ")"
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return nil, "could not be checked (" + err.Error() + ")"
	}
	folders := privateFolderInfos()
	for _, p := range []string{path, resolved} {
		if why := inPrivateFolder(p, folders); why != "" {
			return nil, why
		}
	}
	for _, p := range []string{path, resolved} {
		if why := privateName(filepath.Base(p)); why != "" {
			return nil, why
		}
	}
	if dropGap != nil {
		dropGap()
	}
	// A folder on the path may change after these checks. So the open goes
	// through folder handles, and checks each again (paste_open_*.go).
	return openResolved(resolved, folders)
}

// dropGap, when set, runs between the checks of a path and its open. Tests
// use it to change a folder on the path just there.
var dropGap func()

type privateFolder struct {
	name string
	info os.FileInfo
}

// privateFolderInfos finds the private folders that exist. os.Stat follows
// a folder that is a link, so the identity is that of its target.
func privateFolderInfos() []privateFolder {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil
	}
	var out []privateFolder
	for _, d := range privateFolders {
		if fi, err := os.Stat(filepath.Join(home, filepath.FromSlash(d))); err == nil {
			out = append(out, privateFolder{name: d, info: fi})
		}
	}
	return out
}

// inPrivateFolder says whether path is inside a private folder, by the
// identity of each folder above it, or "".
func inPrivateFolder(path string, folders []privateFolder) string {
	for dir := filepath.Dir(path); ; {
		if st, err := os.Stat(dir); err == nil {
			for _, f := range folders {
				if os.SameFile(st, f.info) {
					return "is in ~/" + f.name + ", a private folder"
				}
			}
		}
		up := filepath.Dir(dir)
		if up == dir {
			return ""
		}
		dir = up
	}
}

// privateName says why a file's name marks it as private, or "". It
// compares with Unicode case folding, as a file system that ignores case
// does, so "ſ" counts as "s".
func privateName(name string) string {
	pattern := ""
	switch {
	case strings.EqualFold(name, ".env"):
		pattern = ".env"
	case foldPrefix(name, ".env.") && !envTemplate(name):
		pattern = ".env.*"
	case foldSuffix(name, ".pem"):
		pattern = "*.pem"
	case foldSuffix(name, ".key"):
		pattern = "*.key"
	case foldPrefix(name, "id_"):
		pattern = "id_*"
	case foldPrefix(name, "credentials"):
		pattern = "credentials*"
	default:
		return ""
	}
	return "has a private name (" + pattern + ")"
}

// envTemplate reports whether a .env.* file is one of the three names that
// by custom hold no secret (question 109).
func envTemplate(name string) bool {
	for _, t := range []string{".env.example", ".env.sample", ".env.template"} {
		if strings.EqualFold(name, t) {
			return true
		}
	}
	return false
}

func foldPrefix(s, prefix string) bool {
	r, p := []rune(s), []rune(prefix)
	return len(r) >= len(p) && strings.EqualFold(string(r[:len(p)]), prefix)
}

func foldSuffix(s, suffix string) bool {
	r, p := []rune(s), []rune(suffix)
	return len(r) >= len(p) && strings.EqualFold(string(r[len(r)-len(p):]), suffix)
}

// tildePath shortens a path in the home folder to ~/….
func tildePath(path string) string {
	h, err := os.UserHomeDir()
	if err != nil || h == "" {
		return path
	}
	rel, err := filepath.Rel(h, path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return path
	}
	return "~/" + filepath.ToSlash(rel)
}

// uploadNotice names files that were uploaded, each with its size, in one
// notice for a drop.
func uploadNotice(names []string, sizes []int) string {
	parts := make([]string, len(names))
	for i, name := range names {
		var s string
		switch size := sizes[i]; {
		case size >= 1<<20:
			s = fmt.Sprintf("%.1f MiB", float64(size)/(1<<20))
		case size >= 1<<10:
			s = fmt.Sprintf("%.1f KiB", float64(size)/(1<<10))
		default:
			s = fmt.Sprintf("%d bytes", size)
		}
		parts[i] = name + " (" + s + ")"
	}
	return "uploaded " + strings.Join(parts, ", ")
}

// guardFlushWait is how long an attach waits at its end for its clipboard
// writes to reach the shared file.
const guardFlushWait = 2 * time.Second
