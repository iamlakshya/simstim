package files

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Capture owns a run's incoming files and the trusted publication point.
// The guest sees only Restore and Incoming, never their parent or CURRENT.
type Capture struct {
	// Restore names the one committed generation exposed read-only.
	Restore string
	// Incoming names only this run's writable capture area.
	Incoming string
	// Nonce binds the ready claim to this capture, without granting a path.
	Nonce    string
	plan     StatePlan
	session  *os.Root
	store    *os.Root
	incoming *os.Root
}

// BeginCapture pins a fresh capture area and the last committed generation.
// It must run on the trusted side, before starting the sandbox.
func BeginCapture(sessionDir string, plan StatePlan) (_ *Capture, err error) {
	if !filepath.IsAbs(sessionDir) || filepath.Clean(sessionDir) != sessionDir {
		return nil, fmt.Errorf("state session root must be absolute and clean")
	}
	if err = plan.Validate(); err != nil {
		return nil, err
	}
	sessionDir, err = filepath.EvalSymlinks(sessionDir)
	if err != nil {
		return nil, err
	}
	session, err := os.OpenRoot(sessionDir)
	if err != nil {
		return nil, err
	}
	c := &Capture{plan: plan, session: session}
	defer func() {
		if err != nil {
			c.Close()
		}
	}()
	for _, dir := range []string{"state-files/epochs", "state-files/empty", "state-incoming"} {
		if err = session.MkdirAll(dir, 0700); err != nil {
			return nil, err
		}
	}
	c.store, err = session.OpenRoot("state-files")
	if err != nil {
		return nil, err
	}
	c.Restore = filepath.Join(sessionDir, "state-files", "empty")
	current, err := OpenRegular(c.store, "CURRENT")
	if err == nil {
		b, e := io.ReadAll(io.LimitReader(current, 36))
		current.Close()
		name := strings.TrimSpace(string(b))
		if e != nil || !epochName(name) {
			return nil, fmt.Errorf("invalid committed state generation")
		}
		st, e := c.store.Lstat("epochs/" + name)
		if e != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("committed state generation is missing")
		}
		c.Restore = filepath.Join(sessionDir, "state-files", "epochs", name)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	keep := ""
	if current := filepath.Base(c.Restore); epochName(current) {
		keep = current
	}
	if err = c.pruneEpochs(keep); err != nil {
		return nil, err
	}
	var token [16]byte
	if _, err = rand.Read(token[:]); err != nil {
		return nil, err
	}
	c.Nonce = hex.EncodeToString(token[:])
	relative := "state-incoming/" + c.Nonce
	if err = session.Mkdir(relative, 0700); err != nil {
		return nil, err
	}
	c.Incoming = filepath.Join(sessionDir, filepath.FromSlash(relative))
	c.incoming, err = session.OpenRoot(relative)
	if err != nil {
		return nil, err
	}
	return c, nil
}

func epochName(s string) bool {
	if len(s) != 34 || !strings.HasPrefix(s, "e-") || s != strings.ToLower(s) {
		return false
	}
	_, err := hex.DecodeString(s[2:])
	return err == nil
}

// Commit publishes this run's complete file families. The caller must first
// observe a clean owned sandbox exit and finish its cleanup. A ready event
// alone never establishes that condition. Neither nonce nor any path is
// taken from the guest as an authority to select a destination.
func (c *Capture) Commit(clean bool, nonce string) error {
	if !clean || nonce != c.Nonce {
		return fmt.Errorf("state capture has no matching clean sandbox exit")
	}
	name := "e-" + c.Nonce
	temp := "epochs/" + name
	if err := c.store.Mkdir(temp, 0700); err != nil {
		return err
	}
	dst, err := c.store.OpenRoot(temp)
	if err != nil {
		return err
	}
	err = CopyFamilies(c.incoming, dst, c.plan.Files)
	if err == nil {
		err = syncRoot(dst)
	}
	dst.Close()
	if err != nil {
		c.store.RemoveAll(temp)
		return fmt.Errorf("capture state families: %w", err)
	}
	epochs, err := c.store.OpenRoot("epochs")
	if err != nil {
		return err
	}
	err = syncRoot(epochs)
	epochs.Close()
	if err != nil {
		return err
	}
	if err = c.store.Remove("CURRENT.new"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := c.store.OpenFile("CURRENT.new", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = f.WriteString(name + "\n")
	if err == nil {
		err = f.Sync()
	}
	err = errorsClose(err, f)
	if err != nil {
		c.store.Remove("CURRENT.new")
		return err
	}
	if err = c.store.Rename("CURRENT.new", "CURRENT"); err != nil {
		return err
	}
	if err = syncRoot(c.store); err != nil {
		return err
	}
	return c.pruneEpochs(name)
}

// Close removes only this run's incoming area. It leaves committed epochs.
func (c *Capture) Close() error {
	if c == nil {
		return nil
	}
	if c.incoming != nil {
		c.incoming.Close()
		c.incoming = nil
	}
	var err error
	if c.session != nil && c.Nonce != "" {
		err = c.session.RemoveAll("state-incoming/" + c.Nonce)
	}
	if c.store != nil {
		c.store.Close()
		c.store = nil
	}
	if c.session != nil {
		c.session.Close()
		c.session = nil
	}
	return err
}

func (c *Capture) pruneEpochs(keep string) error {
	root, err := c.store.OpenRoot("epochs")
	if err != nil {
		return err
	}
	defer root.Close()
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	entries, err := directory.ReadDir(-1)
	directory.Close()
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() != keep {
			if err = root.RemoveAll(entry.Name()); err != nil {
				return err
			}
		}
	}
	return syncRoot(root)
}
