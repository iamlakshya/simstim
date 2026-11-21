package client

import (
	"context"
	"errors"
	"os"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

func (c *networkHoldConfigV1) beginTransaction(ctx context.Context) (*stateFileTxn, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active != nil {
		return nil, agentModeResourceError()
	}
	if err := c.checkLocked(ctx, false); err != nil {
		return nil, err
	}
	tx := &stateFileTxn{ctx: ctx, name: "config.toml", root: c.root, configuration: c}
	c.active = tx
	return tx, nil
}

func (c *networkHoldConfigV1) endTransaction(tx *stateFileTxn) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active == tx {
		c.active = nil
	}
}

func (c *networkHoldConfigV1) openTransactionSource(tx *stateFileTxn) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active != tx || tx.configuration != c || tx.root != c.root {
		return networkHoldConfigErrorV1()
	}
	if err := c.checkLocked(tx.ctx, false); err != nil {
		return err
	}
	if c.snapshot.absent {
		return nil
	}
	file, err := cachefs.OpenOwnedFileExisting(c.root, tx.name, c.snapshot.before.Mode().Perm())
	if err != nil {
		return networkHoldConfigErrorV1()
	}
	info, err := file.Stat()
	if err != nil || !stateSnapshotSame(c.snapshot.before, info) {
		_ = file.Close()
		return networkHoldConfigErrorV1()
	}
	tx.source, tx.before = file, info
	return nil
}

func (c *networkHoldConfigV1) checkTransactionSnapshot(ctx context.Context, tx *stateFileTxn) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active != tx || tx.configuration != c || tx.root != c.root || tx.lock == nil {
		return networkHoldConfigErrorV1()
	}
	if err := c.checkLocked(ctx, tx.output != nil); err != nil {
		return err
	}
	if err := tx.lock.check(ctx, tx.root); err != nil {
		return err
	}
	if tx.source != nil {
		info, err := tx.source.Stat()
		if err != nil || !stateSnapshotSame(tx.before, info) {
			return networkHoldConfigErrorV1()
		}
	}
	if tx.output != nil {
		_, err := cachefs.ValidateOwnedFile(c.root, tx.name, tx.output, 0600)
		return err
	}
	if tx.source == nil {
		if _, err := c.root.Lstat(tx.name); !errors.Is(err, os.ErrNotExist) {
			return networkHoldConfigErrorV1()
		}
		return ctx.Err()
	}
	_, err := cachefs.ValidateOwnedFile(c.root, tx.name, tx.source, tx.before.Mode().Perm())
	return err
}
