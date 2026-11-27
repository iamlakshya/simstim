package pullexport

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/gitpolicy"
	"github.com/iamlakshya/simstim/internal/proto"
)

type capturedSpool struct {
	result CaptureResult
	header gitpolicy.BundleHeader
	heads  []proto.BundleRef
	size   int64
	full   bool
}

func (e *export) captureSpool(caller context.Context, full bool, snapshot []proto.BundleRef, defaultRef string, work <-chan struct{}) (capturedSpool, error) {
	if err := e.life.Err(); err != nil {
		return capturedSpool{}, err
	}
	e.removeSpool()
	directory, err := e.manager.options.Directory(e.primary)
	if err != nil {
		return capturedSpool{}, err
	}
	file, err := os.CreateTemp(directory, "pull-*.bundle")
	if err != nil {
		return capturedSpool{}, fmt.Errorf("make owned pull spool: %w", err)
	}
	e.mu.Lock()
	e.file, e.path = file, file.Name()
	e.mu.Unlock()
	writer := &cappedBundle{export: e, file: file}
	progress, finish := e.captureProgress(caller, work)
	result, err := e.source.Capture(e.life, CaptureArgs{Full: full, Heads: snapshot, DefaultRef: defaultRef, Progress: progress}, writer)
	finish()
	if err != nil {
		return capturedSpool{}, err
	}
	captured := capturedSpool{result: result, size: writer.count, full: full || result.Full}
	if result.Empty {
		if writer.count != 0 || full && len(snapshot) > 0 {
			return capturedSpool{}, exportError("pull_protocol", "the guest returned inconsistent empty work")
		}
		return captured, nil
	}
	if writer.count == 0 {
		return capturedSpool{}, exportError("pull_protocol", "the guest returned an unfinished empty bundle")
	}
	if err = file.Sync(); err != nil {
		return capturedSpool{}, fmt.Errorf("sync pull spool: %w", err)
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return capturedSpool{}, fmt.Errorf("rewind pull spool: %w", err)
	}
	captured.header, err = gitpolicy.ReadBundleHeader(file, 1<<20)
	if err != nil {
		return capturedSpool{}, exportError("pull_protocol", "the captured bundle has an invalid header")
	}
	if len(captured.header.Prerequisites) == 0 {
		captured.full = true
	}
	captured.heads, err = capturedHeads(e.primary, captured.header, result.DefaultRef)
	if err != nil {
		return capturedSpool{}, err
	}
	if captured.full && len(captured.header.Prerequisites) > 0 {
		return capturedSpool{}, exportError("pull_protocol", "full history retained a prerequisite")
	}
	if full && len(snapshot) > 0 && (!sameHeads(snapshot, captured.heads) || result.DefaultRef != defaultRef) {
		return capturedSpool{}, exportError("pull_protocol", "full history changed its captured heads or default branch")
	}
	return captured, nil
}

func capturedHeads(primary string, header gitpolicy.BundleHeader, current string) ([]proto.BundleRef, error) {
	prefix := "refs/heads/" + brand.Name + "/" + primary + "/"
	var heads []proto.BundleRef
	for name, oid := range header.Heads {
		if strings.HasPrefix(name, prefix) && name != prefix && api.ValidGitOID(oid) {
			heads = append(heads, proto.BundleRef{Name: name, OID: oid})
		}
	}
	sort.Slice(heads, func(i, j int) bool { return heads[i].Name < heads[j].Name })
	selected := false
	for _, head := range heads {
		selected = selected || head.Name == current
	}
	if len(heads) == 0 || !selected {
		return nil, exportError("pull_branch", "the guest has no captured current session branch")
	}
	return heads, nil
}

func (e *export) captureProgress(caller context.Context, work <-chan struct{}) (func(), func()) {
	var taskLive atomic.Bool
	taskLive.Store(true)
	progress := func() {
		if caller.Err() != nil || !taskLive.Load() {
			return
		}
		e.mu.Lock()
		if taskLive.Load() && e.workDone == work && e.capturing && !e.closing && e.life.Err() == nil {
			e.last = e.manager.options.Now()
		}
		e.mu.Unlock()
	}
	return progress, func() { taskLive.Store(false) }
}
