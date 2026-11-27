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
	"sync"
)

// Capture owns a run's incoming files and the trusted publication point.
// The guest sees only Restore and Incoming, never their parent or CURRENT.
type Capture struct {
	mu              sync.Mutex
	closing, closed bool
	closeErr        error
	incomingName    string
	incomingInfo    os.FileInfo
	removed         bool
	failedFiles     []*os.File
	failedRoots     []*os.Root
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
func BeginCapture(sessionDir string, plan StatePlan) (capture *Capture, err error) {
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
			capture = c
			err = errors.Join(err, c.Close())
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
		closeErr := c.closeFileV1(current)
		name := strings.TrimSpace(string(b))
		if e != nil || closeErr != nil || !epochName(name) {
			return nil, errors.Join(fmt.Errorf("invalid committed state generation"), e, closeErr)
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
	c.incomingName = relative // Only the successful exclusive creator owns this name.
	c.incomingInfo, err = session.Lstat(relative)
	if err != nil || !c.incomingInfo.IsDir() || c.incomingInfo.Mode()&os.ModeSymlink != 0 {
		return nil, errors.Join(fmt.Errorf("the created incoming capture has no native directory identity"), err)
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
	if c == nil {
		return fmt.Errorf("state capture has no original owner")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closing || c.closed || c.closeErr != nil || c.store == nil || c.incoming == nil {
		return fmt.Errorf("state capture is incomplete or closing")
	}
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
	err = errors.Join(err, c.closeRootV1(dst))
	if err != nil {
		c.store.RemoveAll(temp)
		return fmt.Errorf("capture state families: %w", err)
	}
	epochs, err := c.store.OpenRoot("epochs")
	if err != nil {
		return err
	}
	err = syncRoot(epochs)
	err = errors.Join(err, c.closeRootV1(epochs))
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
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closing = true
	if c.closeErr != nil {
		return c.closeErr
	}
	if c.incoming != nil {
		if err := c.incoming.Close(); err != nil {
			c.closeErr = err
			return err
		}
		c.incoming = nil
	}
	if !c.removed && c.incomingName != "" {
		if c.session == nil {
			return fmt.Errorf("state capture lost its original session root")
		}
		actual, err := c.session.Lstat(c.incomingName)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err == nil && (c.incomingInfo == nil || !os.SameFile(c.incomingInfo, actual)) {
			return fmt.Errorf("the original incoming capture name was replaced")
		}
		if err := c.session.RemoveAll(c.incomingName); err != nil {
			return err
		}
		c.removed = true
	}
	if c.store != nil {
		if err := c.store.Close(); err != nil {
			c.closeErr = errors.Join(c.closeErr, err)
		} else {
			c.store = nil
		}
	}
	if c.session != nil {
		if err := c.session.Close(); err != nil {
			c.closeErr = errors.Join(c.closeErr, err)
		} else {
			c.session = nil
		}
	}
	if c.closeErr != nil {
		return c.closeErr
	}
	c.closed = true
	return nil
}

// Descriptor Close errors cannot become success on a second call: os.File and
// os.Root may already have invalidated their handles. Keep the actual failed
// owner and error instead of reopening a pathname or accepting ErrClosed.
func (c *Capture) closeFileV1(file *os.File) error {
	if err := file.Close(); err != nil {
		c.failedFiles = append(c.failedFiles, file)
		c.closeErr = errors.Join(c.closeErr, err)
		return err
	}
	return nil
}
func (c *Capture) closeRootV1(root *os.Root) error {
	if err := root.Close(); err != nil {
		c.failedRoots = append(c.failedRoots, root)
		c.closeErr = errors.Join(c.closeErr, err)
		return err
	}
	return nil
}

func (c *Capture) pruneEpochs(keep string) (returned error) {
	root, err := c.store.OpenRoot("epochs")
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, c.closeRootV1(root)) }()
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	entries, err := directory.ReadDir(-1)
	err = errors.Join(err, c.closeFileV1(directory))
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
