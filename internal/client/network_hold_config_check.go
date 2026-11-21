package client

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

func (c *networkHoldConfigV1) check(ctx context.Context) error {
	if c == nil {
		return networkHoldConfigErrorV1()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.checkLocked(ctx, false)
}

func (c *networkHoldConfigV1) checkLocked(ctx context.Context, published bool) error {
	if ctx == nil || c.closed || c.owner == nil || c.operation == nil || c.snapshot == nil || c.root == nil ||
		c.owner.request != grantRequestOf(ctx) || c.owner.settings != c.operation || c.path != ConfigDir() || filepath.Join(c.path, "config.toml") != settingsPath() {
		return networkHoldConfigErrorV1()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	request := c.owner.request
	request.mu.Lock()
	valid := request.networkHold == c.owner && request.purpose == grantNetworkHold && request.operation == grantOpNetworkHold && request.lifetimeErrorLocked() == nil
	request.mu.Unlock()
	if !valid {
		return networkHoldConfigErrorV1()
	}
	if err := c.checkDirectoryLocked(ctx); err != nil {
		return err
	}
	if published {
		// Only the actual native transaction's output can take this branch.
		// Its own mutation invalidates the old settings path; it grants no retry.
		if c.active == nil || c.active.output == nil || c.active.configuration != c || c.active.root != c.root {
			return networkHoldConfigErrorV1()
		}
		return c.owner.conn.CheckSavedSource(ctx)
	}
	if err := c.checkCurrentActionLocked(ctx); err != nil {
		return err
	}
	if err := c.operation.Revalidate(ctx); err != nil {
		return err
	}
	if c.snapshot.absent {
		if c.root != nil {
			if _, err := c.root.Lstat("config.toml"); !errors.Is(err, os.ErrNotExist) {
				return networkHoldConfigErrorV1()
			}
		}
		return ctx.Err()
	}
	file, ok := c.snapshot.file.(*os.File)
	if !ok || c.root == nil {
		return networkHoldConfigErrorV1()
	}
	if _, err := cachefs.ValidateOwnedFile(c.root, "config.toml", file, c.snapshot.before.Mode().Perm()); err != nil {
		return networkHoldConfigErrorV1()
	}
	hash := sha256.New()
	var buffer [16 << 10]byte
	if _, err := io.CopyBuffer(hash, io.NewSectionReader(file, 0, c.snapshot.size), buffer[:]); err != nil {
		return err
	}
	if string(hash.Sum(nil)) != string(c.digest[:]) {
		return networkHoldConfigErrorV1()
	}
	return c.operation.Revalidate(ctx)
}

func (c *networkHoldConfigV1) checkDirectoryLocked(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	named, err := cachefs.OpenPrivateRoot(c.path, false)
	if err != nil {
		return networkHoldConfigErrorV1()
	}
	defer named.Close()
	if !networkHoldDirectoryMatchesV1(named, c.rootInfo) || !networkHoldDirectoryMatchesV1(c.root, c.rootInfo) {
		return networkHoldConfigErrorV1()
	}
	return ctx.Err()
}

func networkHoldDirectoryMatchesV1(root *os.Root, original os.FileInfo) bool {
	if root == nil || original == nil {
		return false
	}
	file, err := root.Open(".")
	if err != nil {
		return false
	}
	info, err := file.Stat()
	closeErr := file.Close()
	return err == nil && closeErr == nil && info.IsDir() && os.SameFile(original, info) && info.Mode() == original.Mode()
}
