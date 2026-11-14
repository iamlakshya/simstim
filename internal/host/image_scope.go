package host

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/image"
)

type imageScopeInfo struct{ InvocationID, ControlGroup, ActiveState, MemoryMax, Result string }
type imageOOMCounters struct{ killed, groups uint64 }
type imageScopeWatch struct {
	unit     string
	expected imageScopeInfo
	events   *os.File
	maximum  *os.File
	before   imageOOMCounters
}
type imageOOMEvidence struct {
	positive bool
	err      error
}

func imageScopeProperties(ctx context.Context, unit string) (imageScopeInfo, error) {
	query, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	command := exec.CommandContext(query, "systemctl", "--user", "show", unit, "--property=ActiveState", "--property=MemoryMax", "--property=ControlGroup", "--property=InvocationID", "--property=Result")
	var output imageOutput
	command.Stdout = &output
	if err := command.Run(); err != nil {
		return imageScopeInfo{}, fmt.Errorf("read owned image scope properties: %w", err)
	}
	values := map[string]string{}
	for _, line := range strings.Split(output.String(), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok {
			values[key] = value
		}
	}
	return imageScopeInfo{InvocationID: values["InvocationID"], ControlGroup: values["ControlGroup"], ActiveState: values["ActiveState"], MemoryMax: values["MemoryMax"], Result: values["Result"]}, nil
}

func sameImageInvocation(expected, current imageScopeInfo) bool {
	return validImageInvocation(expected.InvocationID) && expected.InvocationID == current.InvocationID
}
func sameImageScope(expected, current imageScopeInfo) bool {
	return sameImageInvocation(expected, current) && expected.ControlGroup != "" && expected.ControlGroup == current.ControlGroup
}
func validImageInvocation(id string) bool { return len(id) == 32 && image.ValidDigest("sha256:"+id+id) }

func pinImageScope(ctx context.Context, unit string, expected imageScopeInfo) (*imageScopeWatch, error) {
	root, err := os.OpenRoot("/sys/fs/cgroup")
	if err != nil {
		return nil, fmt.Errorf("open cgroup filesystem: %w", err)
	}
	defer root.Close()
	return pinImageScopeAt(ctx, unit, expected, root, imageScopeProperties)
}

func pinImageScopeAt(ctx context.Context, unit string, expected imageScopeInfo, root *os.Root, inspect func(context.Context, string) (imageScopeInfo, error)) (*imageScopeWatch, error) {
	current, err := inspect(ctx, unit)
	if err != nil {
		return nil, err
	}
	if !sameImageScope(expected, current) || current.ActiveState != "active" || current.MemoryMax != "1073741824" {
		return nil, fmt.Errorf("image scope ownership or memory boundary changed before conversion")
	}
	group := current.ControlGroup
	if !path.IsAbs(group) || path.Clean(group) != group || group == "/" || strings.ContainsRune(group, 0) {
		return nil, fmt.Errorf("image scope reported an invalid cgroup path")
	}
	events, err := root.Open(strings.TrimPrefix(group, "/") + "/memory.events")
	if err != nil {
		return nil, fmt.Errorf("pin owned image OOM counters: %w", err)
	}
	maximum, err := root.Open(strings.TrimPrefix(group, "/") + "/memory.max")
	if err != nil {
		events.Close()
		return nil, fmt.Errorf("pin owned image memory boundary: %w", err)
	}
	value, err := readImageMemoryMax(maximum)
	if err != nil || value != "1073741824" {
		events.Close()
		maximum.Close()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("the image scope has no verified 1 GiB kernel memory boundary: got %q", value)
	}
	before, err := readImageOOM(events)
	if err != nil {
		events.Close()
		maximum.Close()
		return nil, err
	}
	return &imageScopeWatch{unit: unit, expected: current, events: events, maximum: maximum, before: before}, nil
}

func readImageMemoryMax(file *os.File) (string, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("rewind image memory boundary: %w", err)
	}
	data, err := io.ReadAll(io.LimitReader(file, 65))
	if err != nil {
		return "", fmt.Errorf("read image memory boundary: %w", err)
	}
	if len(data) > 64 {
		return "", fmt.Errorf("image memory boundary exceeds its kernel value bound")
	}
	return strings.TrimSpace(string(data)), nil
}

func readImageOOM(file *os.File) (imageOOMCounters, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return imageOOMCounters{}, fmt.Errorf("rewind owned image OOM counters: %w", err)
	}
	data, err := io.ReadAll(io.LimitReader(file, 64<<10+1))
	if err != nil {
		return imageOOMCounters{}, fmt.Errorf("read owned image OOM counters: %w", err)
	}
	if len(data) > 64<<10 {
		return imageOOMCounters{}, fmt.Errorf("owned image OOM counters exceed their bound")
	}
	return parseImageOOM(data)
}

func parseImageOOM(data []byte) (imageOOMCounters, error) {
	var counters imageOOMCounters
	seen := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) != 2 {
			return counters, fmt.Errorf("malformed owned image OOM counter")
		}
		if fields[0] != "oom_kill" && fields[0] != "oom_group_kill" {
			continue
		}
		if seen[fields[0]] {
			return counters, fmt.Errorf("repeated owned image OOM counter")
		}
		seen[fields[0]] = true
		count, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return counters, fmt.Errorf("parse owned image OOM counter: %w", err)
		}
		if fields[0] == "oom_kill" {
			counters.killed = count
		} else {
			counters.groups = count
		}
	}
	if !seen["oom_kill"] {
		return counters, fmt.Errorf("owned image OOM kill counter is missing")
	}
	return counters, nil
}

func imageOOMOutcome(expected, current imageScopeInfo, before, after imageOOMCounters, identityErr, counterErr error) imageOOMEvidence {
	if identityErr != nil {
		return imageOOMEvidence{err: identityErr}
	}
	if !sameImageInvocation(expected, current) || current.MemoryMax != "1073741824" {
		return imageOOMEvidence{err: fmt.Errorf("image scope identity or memory boundary changed")}
	}
	if current.Result == "oom-kill" {
		return imageOOMEvidence{positive: true}
	}
	if !sameImageScope(expected, current) {
		return imageOOMEvidence{err: fmt.Errorf("owned image cgroup identity changed")}
	}
	if counterErr != nil {
		return imageOOMEvidence{err: counterErr}
	}
	if after.killed < before.killed || after.groups < before.groups {
		return imageOOMEvidence{err: fmt.Errorf("owned image OOM counters decreased")}
	}
	return imageOOMEvidence{positive: after.killed > before.killed || after.groups > before.groups}
}

func (w *imageScopeWatch) outcome(ctx context.Context) imageOOMEvidence {
	return w.outcomeWith(ctx, imageScopeProperties)
}

func (w *imageScopeWatch) outcomeWith(ctx context.Context, inspect func(context.Context, string) (imageScopeInfo, error)) imageOOMEvidence {
	current, identityErr := inspect(ctx, w.unit)
	if current.Result == "oom-kill" {
		return imageOOMOutcome(w.expected, current, w.before, imageOOMCounters{}, identityErr, nil)
	}
	maximum, err := readImageMemoryMax(w.maximum)
	if err != nil {
		return imageOOMEvidence{err: err}
	}
	if maximum != "1073741824" {
		return imageOOMEvidence{err: fmt.Errorf("the owned image kernel memory boundary changed")}
	}
	after, counterErr := readImageOOM(w.events)
	return imageOOMOutcome(w.expected, current, w.before, after, identityErr, counterErr)
}

func cleanupImageScope(unit string, expected imageScopeInfo) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return cleanupImageScopeWith(ctx, unit, expected, imageScopeProperties, func(ctx context.Context, operation, unit string) error {
		_, err := systemctl(ctx, operation, unit)
		return err
	})
}

func cleanupImageScopeWith(ctx context.Context, unit string, expected imageScopeInfo, inspect func(context.Context, string) (imageScopeInfo, error), act func(context.Context, string, string) error) error {
	if !validImageInvocation(expected.InvocationID) {
		return nil
	}
	current, err := inspect(ctx, unit)
	if err != nil || !sameImageInvocation(expected, current) {
		return nil
	}
	if err := act(ctx, "stop", unit); err != nil {
		return UserError("image_scope", "the owned image scope could not be stopped: "+err.Error(), "Run host doctor and stop this image scope before pulling again.")
	}
	current, err = inspect(ctx, unit)
	if err == nil && sameImageInvocation(expected, current) {
		if err := act(ctx, "reset-failed", unit); err != nil {
			return fmt.Errorf("reset owned image scope: %w", err)
		}
	}
	return nil
}
