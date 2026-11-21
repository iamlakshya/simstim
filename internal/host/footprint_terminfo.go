package host

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

// tic writes only into this invocation's fresh private staging directory. The
// producer publishes each checked output inode through the footprint writer.
func installFootprintedTerminfoV1(ctx context.Context, source string) error {
	stage, err := os.MkdirTemp("", "terminfo-")
	if err != nil {
		return err
	}
	// This directory was created by this invocation, and carries no user data.
	defer os.RemoveAll(stage)
	root, err := cachefs.OpenPrivateRoot(stage, false)
	if err != nil {
		return err
	}
	defer root.Close()
	input, err := cachefs.OpenFile(root, "source", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := input.WriteString(source)
	if err := errors.Join(writeErr, input.Close()); err != nil {
		return err
	}
	if out, err := exec.CommandContext(ctx, "tic", "-x", "-o", filepath.Join(stage, "compiled"), filepath.Join(stage, "source")).CombinedOutput(); err != nil {
		return fmt.Errorf("compile terminfo: %w: %s", err, strings.TrimSpace(string(out)))
	}
	compiled, err := os.ReadDir(filepath.Join(stage, "compiled"))
	if err != nil {
		return err
	}
	count := 0
	for _, bucket := range compiled {
		if !bucket.IsDir() || !validTerminfoBucketV1(bucket.Name()) {
			return placementError("the terminfo compiler returned an unknown directory")
		}
		leaves, err := os.ReadDir(filepath.Join(stage, "compiled", bucket.Name()))
		if err != nil {
			return err
		}
		for _, leaf := range leaves {
			count++
			if count > 256 || !leaf.Type().IsRegular() || !knownFootprintLeafV1("terminfo:"+bucket.Name(), leaf.Name()) {
				return placementError("the terminfo compiler returned an unknown file")
			}
			data, err := os.ReadFile(filepath.Join(stage, "compiled", bucket.Name(), leaf.Name()))
			if err != nil {
				return err
			}
			if len(data) == 0 || len(data) > 1<<20 {
				return placementError("the compiled terminfo exceeds its fixed file bound")
			}
			if err := writeFootprintedFileV1(ctx, "terminfo:"+bucket.Name(), leaf.Name(), data); err != nil {
				return err
			}
		}
	}
	if count == 0 {
		return placementError("the terminfo compiler produced no entries")
	}
	return nil
}
