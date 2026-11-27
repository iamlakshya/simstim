//go:build darwin

package join

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

type macFileState struct {
	Exists bool   `json:"exists"`
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
	Mode   uint32 `json:"mode"`
	SHA256 string `json:"sha256,omitempty"`
}

type macStep struct {
	ID     string       `json:"id"`
	State  string       `json:"state"`
	Path   string       `json:"path,omitempty"`
	Before macFileState `json:"before"`
	After  macFileState `json:"after"`
}

type macLedger struct {
	Version         int           `json:"v"`
	Account         string        `json:"account"`
	Nonce           uint64        `json:"nonce"`
	KeyHash         string        `json:"key_hash"`
	GUID            string        `json:"guid"`
	UID             int           `json:"uid"`
	GID             int           `json:"gid"`
	AgentUser       string        `json:"agent_user,omitempty"`
	AgentUID        int           `json:"agent_uid,omitempty"`
	AgentHome       string        `json:"agent_home,omitempty"`
	AgentGUID       string        `json:"agent_guid,omitempty"`
	Home            string        `json:"home"`
	LimaHome        string        `json:"lima_home"`
	Instance        string        `json:"instance"`
	HomeRef         macFileState  `json:"home_ref"`
	LimaRef         macFileState  `json:"lima_ref"`
	NestedDisabled  bool          `json:"nested_disabled"`
	VMRef           macFileState  `json:"vm_ref"`
	GuestPort       uint16        `json:"guest_port"`
	Options         DarwinOptions `json:"options"`
	LimaHash        string        `json:"lima_hash"`
	ImageHash       string        `json:"image_hash"`
	OriginalACSleep int           `json:"original_ac_sleep"`
	ACSleepRecorded bool          `json:"ac_sleep_recorded"`
	Steps           []macStep     `json:"steps"`
	Complete        bool          `json:"complete"`
}

func macDigest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

func macProtected(path string, directory bool) (os.FileInfo, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, macFailure("join_mac_path", "a root-owned Mac path is not absolute and clean")
	}
	var original os.FileInfo
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil {
			return nil, fmt.Errorf("inspect Mac join path: %w", err)
		}
		native, ok := info.Sys().(*syscall.Stat_t)
		if !ok || native.Uid != 0 || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0022 != 0 {
			return nil, macFailure("join_mac_path", "another user can replace a root-owned Mac join path")
		}
		if current == path {
			original = info
			if directory != info.IsDir() || !directory && !info.Mode().IsRegular() {
				return nil, macFailure("join_mac_path", "the Mac join object has another native type")
			}
		} else if !info.IsDir() {
			return nil, macFailure("join_mac_path", "a Mac join parent is not a directory")
		}
		if filepath.Dir(current) == current {
			break
		}
	}
	return original, nil
}

func macRootDir(path string, mode os.FileMode) error {
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		if _, err := macProtected(filepath.Dir(path), true); err != nil {
			return err
		}
		if err := os.Mkdir(path, mode); err != nil {
			return fmt.Errorf("make root-owned Mac join directory: %w", err)
		}
	} else if err != nil {
		return err
	}
	_, err := macProtected(path, true)
	return err
}

func macSyncDir(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(file.Sync(), file.Close())
}

func macAtomic(path string, data []byte, mode os.FileMode) error {
	if _, err := macProtected(filepath.Dir(path), true); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".join-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if n, err := file.Write(data); err != nil || n != len(data) {
		file.Close()
		return errors.Join(err, io.ErrShortWrite)
	}
	if err := errors.Join(file.Chmod(mode), file.Sync(), file.Close()); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return err
	}
	return macSyncDir(filepath.Dir(path))
}

func macReadFile(path string, limit int64) ([]byte, macFileState, error) {
	var state macFileState
	info, err := macProtected(path, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil, state, nil
	}
	if err != nil {
		return nil, state, err
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, state, err
	}
	defer file.Close()
	actual, err := file.Stat()
	if err != nil || !os.SameFile(info, actual) || actual.Size() > limit {
		return nil, state, macFailure("join_mac_file", "the protected Mac file changed or exceeds its bound")
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, state, errors.Join(err, macFailure("join_mac_file", "the protected Mac file exceeds its bound"))
	}
	native := actual.Sys().(*syscall.Stat_t)
	state = macFileState{Exists: true, Device: uint64(native.Dev), Inode: native.Ino, Mode: uint32(actual.Mode().Perm()), SHA256: macDigest(data)}
	return data, state, nil
}

func (e *macExecutor) save() error {
	data, err := json.MarshalIndent(e.ledger, "", "  ")
	if err != nil {
		return err
	}
	return macAtomic(filepath.Join(e.dir, "ledger.json"), append(data, '\n'), 0600)
}

func (e *macExecutor) step(id string) *macStep {
	for i := range e.ledger.Steps {
		if e.ledger.Steps[i].ID == id {
			return &e.ledger.Steps[i]
		}
	}
	return nil
}

func (e *macExecutor) intent(id, path string) (*macStep, error) {
	if step := e.step(id); step != nil {
		return step, nil
	}
	e.ledger.Steps = append(e.ledger.Steps, macStep{ID: id, State: "intent", Path: path})
	if err := e.save(); err != nil {
		return nil, err
	}
	return e.step(id), nil
}

func (e *macExecutor) applied(id string) error {
	step := e.step(id)
	if step == nil {
		return macFailure("join_mac_ledger", "the original Mac effect has no ahead journal")
	}
	step.State = "applied"
	return e.save()
}

// installFile retains native hard-link anchors before namespace publication.
// An interrupted write can be undone only through these original objects.
func (e *macExecutor) installFile(id, path string, data []byte, mode os.FileMode) error {
	step := e.step(id)
	anchorDir := filepath.Join(e.dir, "anchors")
	if err := macRootDir(anchorDir, 0700); err != nil {
		return err
	}
	beforeAnchor, afterAnchor := filepath.Join(anchorDir, id+"-before"), filepath.Join(anchorDir, id+"-after")
	if step == nil {
		_, before, err := macReadFile(path, macFileLimit(id))
		if err != nil {
			return err
		}
		step, err = e.intent(id, path)
		if err != nil {
			return err
		}
		step.Before = before
		if err := e.save(); err != nil {
			return err
		}
		if before.Exists {
			if err := os.Link(path, beforeAnchor); err != nil {
				return err
			}
			if err := macSyncDir(anchorDir); err != nil {
				return err
			}
		}
	}
	if step.Path != path {
		return macFailure("join_mac_ledger", "the original Mac journal names another target")
	}
	if step.State == "applied" {
		_, current, err := macReadFile(path, macFileLimit(id))
		if err != nil || current != step.After || current.SHA256 != macDigest(data) {
			return macFailure("join_mac_changed", "the installed Mac file changed after this join")
		}
		return nil
	}
	_, current, currentErr := macReadFile(path, macFileLimit(id))
	if currentErr != nil {
		return currentErr
	}
	if current != step.Before && current != step.After {
		return macFailure("join_mac_changed", "another native object replaced the Mac target before publication")
	}
	if _, err := os.Lstat(afterAnchor); errors.Is(err, os.ErrNotExist) {
		if err := macAtomic(afterAnchor, data, mode); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	_, after, err := macReadFile(afterAnchor, macFileLimit(id))
	if err != nil || after.SHA256 != macDigest(data) {
		return macFailure("join_mac_anchor", "the original prepared Mac file has changed")
	}
	step.After = after
	if err := e.save(); err != nil {
		return err
	}
	if _, err := macProtected(filepath.Dir(path), true); err != nil {
		return err
	}
	temporary := filepath.Join(filepath.Dir(path), ".join-"+e.ledger.GUID+"-"+id)
	if err := os.Link(afterAnchor, temporary); err != nil {
		return err
	}
	defer os.Remove(temporary)
	if err := os.Rename(temporary, path); err != nil {
		return err
	}
	if err := macSyncDir(filepath.Dir(path)); err != nil {
		return err
	}
	return e.applied(id)
}

func (e *macExecutor) undoFile(step *macStep) error {
	_, current, err := macReadFile(step.Path, macFileLimit(step.ID))
	if err != nil {
		return err
	}
	if current == step.Before {
		return nil
	}
	if current != step.After || !current.Exists {
		return macFailure("join_mac_changed", "a Mac join file no longer names its original installed object")
	}
	anchor := filepath.Join(e.dir, "anchors", step.ID+"-after")
	_, after, err := macReadFile(anchor, macFileLimit(step.ID))
	if err != nil || after != current {
		return macFailure("join_mac_anchor", "the Mac leave has no original installed-file anchor")
	}
	if step.Before.Exists {
		before := filepath.Join(e.dir, "anchors", step.ID+"-before")
		_, state, err := macReadFile(before, macFileLimit(step.ID))
		if err != nil || state != step.Before {
			return macFailure("join_mac_anchor", "the Mac leave has no original previous-file anchor")
		}
		temporary := filepath.Join(filepath.Dir(step.Path), ".leave-"+e.ledger.GUID+"-"+step.ID)
		if err := os.Link(before, temporary); err != nil {
			return err
		}
		defer os.Remove(temporary)
		if err := os.Rename(temporary, step.Path); err != nil {
			return err
		}
	} else if err := os.Remove(step.Path); err != nil {
		return err
	}
	return macSyncDir(filepath.Dir(step.Path))
}

func readMacLedger(dir string) (*macLedger, error) {
	data, _, err := macReadFile(filepath.Join(dir, "ledger.json"), 4<<20)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, os.ErrNotExist
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	var ledger macLedger
	if decoder.Decode(&ledger) != nil || decoder.Decode(new(any)) != io.EOF || ledger.Version != 1 || ledger.Nonce == 0 || ledger.Account == "" || len(ledger.Steps) > 32 {
		return nil, macFailure("join_mac_ledger", "the original root-owned Mac ledger is invalid")
	}
	return &ledger, nil
}

func macFileLimit(id string) int64 {
	if id == "helper" {
		return 256 << 20
	}
	return 1 << 20
}
