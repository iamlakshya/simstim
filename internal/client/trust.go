package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

// The user's answers to "may this repository's setup script run?", kept by
// host and repository in the client's state, so each repository is asked
// about once on each host. A yes holds for the content of the scripts that
// it was given for, by their setup hash: a change asks again (spec 19.2).

// SetupQuestion asks whether a repository's setup script may run.
type SetupQuestion = api.SetupQuestion

// trustVersion is the version of setup-trust.json that this client writes.
const trustVersion = 2

// trustFile is setup-trust.json. A file without a version is version 1,
// which kept a yes or a no in Hosts.
type trustFile struct {
	Version int `json:"version,omitempty"`
	// Hosts maps a host's name to its repositories and whether their setup
	// scripts may run. From version 2 it holds refusals only, so that an
	// older client, which reads only Hosts, keeps refusing.
	Hosts map[string]map[string]bool `json:"hosts"`
	// Trusted maps a host's name to the repositories that the user said yes
	// for, with the setup hash of each yes.
	Trusted map[string]map[string]trustEntry `json:"trusted"`
}

// trustEntry is one yes.
type trustEntry struct {
	// SHA256 is the setup hash that the yes was for. It is empty for a yes
	// from version 1, or one given to an older helper, which sends no hash:
	// such a yes asks again once.
	SHA256 string    `json:"sha256"`
	At     time.Time `json:"at,omitzero"`
}

// SetupAnswers are the user's answers about the setup scripts of the
// repositories on one host.
type SetupAnswers struct {
	// Refused holds each repository that the user said no for, as false.
	// It goes in NewArgs.SetupTrust.
	Refused map[string]bool
	// Hashes holds the setup hash of each yes that has one. It goes in
	// NewArgs.SetupHashes.
	Hashes map[string]string
	// Trusted holds each repository that the user said yes for, with a hash
	// or without one.
	Trusted map[string]bool
	// Warning is set when setup-trust.json did not parse. The client then
	// moved it aside and reads no answers, or could not move it.
	Warning *UserError
}

// Apply puts the answers into a new session's arguments.
func (s SetupAnswers) Apply(a *NewArgs) {
	a.SetupTrust, a.SetupHashes = s.Refused, s.Hashes
}

// SetupPast is what the user said before about the repository of a
// question. The question's words follow it (spec 19.2).
type SetupPast int

const (
	// SetupNotTrusted is a repository with no yes, or a question from an
	// older helper, which sends no hash.
	SetupNotTrusted SetupPast = iota
	// SetupChanged is a repository whose yes holds another setup hash: its
	// scripts changed after the yes.
	SetupChanged
	// SetupUnchecked is a repository whose yes holds no hash: a yes from
	// version 1 of the file, or one given to an older helper. Its scripts
	// may or may not have changed since.
	SetupUnchecked
)

// Past says what the user said before about the repository of q.
func (s SetupAnswers) Past(q SetupQuestion) SetupPast {
	kept := s.Hashes[q.Repo]
	switch {
	case q.Hash == "" || !s.Trusted[q.Repo]:
		return SetupNotTrusted
	case kept == "":
		return SetupUnchecked
	case kept != q.Hash:
		return SetupChanged
	}
	return SetupNotTrusted
}

// Answered returns s with the user's answer to q, as RememberSetup keeps
// it, for the question after the next start.
func (s SetupAnswers) Answered(q SetupQuestion, run bool) SetupAnswers {
	out := SetupAnswers{Refused: maps.Clone(s.Refused), Hashes: maps.Clone(s.Hashes), Trusted: maps.Clone(s.Trusted)}
	delete(out.Refused, q.Repo)
	delete(out.Hashes, q.Repo)
	delete(out.Trusted, q.Repo)
	switch {
	case !run:
		out.Refused = setKey(out.Refused, q.Repo, false)
	case q.Hash == "":
		out.Trusted = setKey(out.Trusted, q.Repo, true)
	default:
		out.Trusted = setKey(out.Trusted, q.Repo, true)
		out.Hashes = setKey(out.Hashes, q.Repo, q.Hash)
	}
	return out
}

// AnswerSetup returns a with the user's answer to q, for the start after
// the answer. A yes to a question with a hash starts again with that hash
// in SetupHashes, not with SetupRun, so a script that changes again before
// the start asks again. A question without a hash comes from an older
// helper: the answer then goes in Setup, for this start only. A no goes in
// Setup too.
func AnswerSetup(a NewArgs, q SetupQuestion, run bool) NewArgs {
	switch {
	case !run:
		a.Setup = api.SetupSkip
	case q.Hash == "":
		a.Setup = api.SetupRun
	default:
		a.SetupHashes = maps.Clone(a.SetupHashes)
		a.SetupHashes = setKey(a.SetupHashes, q.Repo, q.Hash)
	}
	return a
}

var trustMu sync.Mutex

func trustPath() string { return filepath.Join(StateDir(), "setup-trust.json") }

// renameTrust moves a file that does not parse aside. Tests replace it.
var renameTrust = os.Rename

// loadTrust reads setup-trust.json, as version 2. A yes in Hosts, from
// version 1 or from an older client that rewrote the file, moves to Trusted
// with no hash, unless Trusted has one already. A no in Hosts drops any yes
// in Trusted, so a no wins. A file that does not parse moves aside (see
// badTrustFile), and loadTrust returns no answers and a warning. stuck is
// true when that file could not move: an answer must not write over it.
func loadTrust() (f trustFile, warn *UserError, stuck bool) {
	b, err := os.ReadFile(trustPath())
	if err == nil && json.Unmarshal(b, &f) != nil {
		f = trustFile{}
		warn, stuck = badTrustFile()
	}
	if f.Hosts == nil {
		f.Hosts = map[string]map[string]bool{}
	}
	if f.Trusted == nil {
		f.Trusted = map[string]map[string]trustEntry{}
	}
	for host, repos := range f.Hosts {
		for repo, run := range repos {
			if !run {
				delete(f.Trusted[host], repo)
				continue
			}
			delete(repos, repo)
			if _, ok := f.Trusted[host][repo]; !ok {
				f.trusted(host)[repo] = trustEntry{}
			}
		}
	}
	f.Version = trustVersion
	return f, warn, stuck
}

// badTrustFile moves a setup-trust.json that does not parse to the first
// free name of setup-trust.json.bad, setup-trust.json.bad.1 and so on, so
// that it never replaces an older one. The names have no limit. It returns
// the warning that says so, and whether the file could not move: a name
// that cannot be checked, or a move that fails, leaves it in place.
func badTrustFile() (warn *UserError, stuck bool) {
	for i := 0; ; i++ {
		bad := trustPath() + ".bad"
		if i > 0 {
			bad += "." + strconv.Itoa(i)
		}
		_, err := os.Lstat(bad)
		switch {
		case err == nil:
			continue // keep an older one
		case !errors.Is(err, fs.ErrNotExist):
			return stuckTrustFile(fmt.Errorf("check %s: %w", filepath.Base(bad), err)), true
		}
		// The file may be gone already: another client moved it.
		if err := renameTrust(trustPath(), bad); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return stuckTrustFile(err), true
		}
		return &UserError{Msg: "could not read setup-trust.json, so each setup script asks again. The old file is now " + filepath.Base(bad) + ".",
			Hint: "Compare the two files, then delete " + bad + ".", Code: "trust_file"}, false
	}
}

// stuckTrustFile is the warning about a setup-trust.json that does not
// parse and could not move aside, because of err.
func stuckTrustFile(err error) *UserError {
	return &UserError{Msg: "could not read setup-trust.json, and could not move it aside: " + err.Error() + ". Each setup script asks again, and no answer is kept.",
		Hint: "Fix or delete " + trustPath() + ".", Code: "trust_file"}
}

func (f *trustFile) trusted(host string) map[string]trustEntry {
	if f.Trusted[host] == nil {
		f.Trusted[host] = map[string]trustEntry{}
	}
	return f.Trusted[host]
}

// trustWait is the longest wait for the lock of setup-trust.json: the wait
// of the clipboard memory's lock, which also guards a small file that a
// client holds for a moment. Tests shorten it.
var trustWait = clipWait

// lockTrust takes the exclusive lock that every read, recovery and save of
// setup-trust.json holds, on setup-trust.json.lock, with the file lock of
// filelock.go. So two client processes never lose each other's answers,
// and never move the same broken file aside. It waits up to trustWait.
// trustMu still orders the goroutines of one process.
func lockTrust() (unlock func(), err error) {
	return lockPath(trustPath()+".lock", trustWait, true)
}

// trustLockError is the error of a lock of setup-trust.json that could not
// be taken. what says what the caller does without it.
func trustLockError(what string, err error) *UserError {
	hint := "Check that you can write to " + StateDir() + ", then try again."
	if errors.Is(err, errLockBusy) {
		hint = "Another client is reading or saving setup answers. Wait a moment, then try again."
	}
	return &UserError{Msg: "could not lock setup-trust.json, so " + what + ": " + err.Error(), Hint: hint, Code: "trust_lock"}
}

// LoadSetupAnswers returns the user's answers for a host. When the lock of
// setup-trust.json cannot be taken, it fails closed: it returns no answers,
// so no script is trusted, and a warning that says why.
func LoadSetupAnswers(host string) SetupAnswers {
	trustMu.Lock()
	defer trustMu.Unlock()
	if _, err := os.Stat(StateDir()); errors.Is(err, fs.ErrNotExist) {
		return SetupAnswers{} // no file yet, so no answers
	}
	unlock, err := lockTrust()
	if err != nil {
		return SetupAnswers{Warning: trustLockError("no setup script is trusted this time", err)}
	}
	defer unlock()
	f, warn, _ := loadTrust()
	s := SetupAnswers{Warning: warn}
	for repo := range f.Hosts[host] {
		s.Refused = setKey(s.Refused, repo, false)
	}
	for repo, e := range f.Trusted[host] {
		s.Trusted = setKey(s.Trusted, repo, true)
		if e.SHA256 != "" {
			s.Hashes = setKey(s.Hashes, repo, e.SHA256)
		}
	}
	return s
}

func setKey[V any](m map[string]V, k string, v V) map[string]V {
	if m == nil {
		m = map[string]V{}
	}
	m[k] = v
	return m
}

// RememberSetup keeps the user's answer for a repository on a host. A yes
// keeps hash, the setup hash of the question, and removes any no. A no
// removes any yes. When setup-trust.json does not parse, such as after
// another program changed it since the answers were read, it moves the file
// aside. It then keeps the answer in a new file, and returns warn, which
// names the old file. When the file cannot move aside, it keeps nothing,
// and returns that warning as err. It reads, recovers and saves the file
// under its lock. When the lock cannot be taken, it keeps nothing, and
// returns an error that says why.
func RememberSetup(host, repo string, run bool, hash string) (warn *UserError, err error) {
	trustMu.Lock()
	defer trustMu.Unlock()
	if err := os.MkdirAll(StateDir(), 0o700); err != nil {
		return nil, fmt.Errorf("make %s: %w", StateDir(), err)
	}
	unlock, err := lockTrust()
	if err != nil {
		return nil, trustLockError("the answer is not kept", err)
	}
	defer unlock()
	f, warn, stuck := loadTrust()
	if stuck {
		return nil, warn
	}
	return warn, saveAnswer(f, host, repo, run, hash)
}

// saveAnswer writes f with the user's answer for repo on host. The caller
// holds the file's lock, so no other client writes the same temporary file.
func saveAnswer(f trustFile, host, repo string, run bool, hash string) error {
	if run {
		delete(f.Hosts[host], repo)
		f.trusted(host)[repo] = trustEntry{SHA256: hash, At: time.Now().UTC().Truncate(time.Second)}
	} else {
		delete(f.Trusted[host], repo)
		if f.Hosts[host] == nil {
			f.Hosts[host] = map[string]bool{}
		}
		f.Hosts[host][repo] = false
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("save the setup answers: %w", err)
	}
	tmp := trustPath() + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, trustPath()); err != nil {
		return fmt.Errorf("save the answer: %w", err)
	}
	return nil
}
