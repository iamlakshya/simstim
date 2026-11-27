package client

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sync"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/cachefs"
)

// This native owner is captured before the original public export. It owns
// the existing config root, never a permission supplied by a resolution DTO.
type networkHoldConfigV1 struct {
	mu                sync.Mutex
	owner             *networkHoldOperation
	operation         *settingsOperation
	snapshot          *agentSettingsSnapshot
	path              string
	root              *os.Root
	rootInfo          os.FileInfo
	digest            [32]byte
	active            *stateFileTxn
	closed            bool
	authorized        bool
	authorizedTier    Tier
	authorizedMaximum api.NetworkHoldMaximum
}

func (*networkHoldConfigV1) Format(out fmt.State, _ rune) { _, _ = io.WriteString(out, api.Redacted) }

func networkHoldConfigErrorV1() error {
	return &UserError{Msg: "the original policy file or its native folder changed", Hint: "Check the local config file, then select the held request again with a new action.", Code: "network_hold_source", Kind: KindRefused}
}

func openNetworkHoldConfigV1(ctx context.Context, owner *networkHoldOperation) (_ *networkHoldConfigV1, failure error) {
	if ctx == nil || owner == nil || owner.request != grantRequestOf(ctx) || owner.settings == nil || owner.conn == nil {
		return nil, networkHoldConfigErrorV1()
	}
	snapshot, err := owner.settings.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	path, err := filepath.Abs(ConfigDir())
	if err != nil || filepath.Join(path, "config.toml") != settingsPath() {
		return nil, networkHoldConfigErrorV1()
	}
	result := &networkHoldConfigV1{owner: owner, operation: owner.settings, snapshot: snapshot, path: path}
	keep := false
	defer func() {
		if !keep {
			failure = errors.Join(failure, result.close())
		}
	}()
	if err := owner.conn.CheckSavedSource(ctx); err != nil {
		return nil, err
	}
	result.root, err = cachefs.OpenPrivateRoot(path, false)
	if err != nil {
		// A genuine saved connection's membership file already requires this
		// folder. Absence is changed Source, never permission to recreate it.
		return nil, networkHoldConfigErrorV1()
	}
	folder, err := result.root.Open(".")
	if err != nil {
		return nil, err
	}
	result.rootInfo, err = folder.Stat()
	closeErr := folder.Close()
	if err != nil || closeErr != nil {
		return nil, errors.Join(err, closeErr)
	}
	if !snapshot.absent {
		if snapshot.before == nil || snapshot.before.Mode().Perm()&0022 != 0 || snapshot.before.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
			return nil, networkHoldConfigErrorV1()
		}
		file, ok := snapshot.file.(*os.File)
		if !ok || result.root == nil {
			return nil, networkHoldConfigErrorV1()
		}
		if _, err := cachefs.ValidateOwnedFile(result.root, "config.toml", file, snapshot.before.Mode().Perm()); err != nil {
			return nil, networkHoldConfigErrorV1()
		}
		data, err := io.ReadAll(io.NewSectionReader(file, 0, snapshot.size))
		if err != nil || len(data) != int(snapshot.size) {
			return nil, networkHoldConfigErrorV1()
		}
		var decoded Settings
		if _, err := decodeConfig(string(data), &decoded); err != nil {
			return nil, err
		}
		original, err := owner.settings.Settings(ctx)
		if err != nil || !reflect.DeepEqual(withDefaults(decoded), original) {
			return nil, networkHoldConfigErrorV1()
		}
		result.digest = sha256.Sum256(data)
	}
	if err := result.check(ctx); err != nil {
		return nil, err
	}
	keep = true
	return result, nil
}

func (c *networkHoldConfigV1) close() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	if c.active != nil {
		return agentModeResourceError()
	}
	var failure error
	if c.root != nil {
		failure = c.root.Close()
	}
	if failure != nil {
		return failure
	}
	c.closed = true
	c.root, c.snapshot, c.operation, c.owner = nil, nil, nil, nil
	return nil
}
