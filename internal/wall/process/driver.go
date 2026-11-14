package process

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/iamlakshya/simstim/internal/files"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/wall"
)

// Driver builds the namespace, mount, Landlock and seccomp process wall.
type Driver struct{}

// Name identifies this driver in trusted manifests.
func (Driver) Name() string { return "process" }

// Level is the process wall's strength.
func (Driver) Level() wall.Level { return wall.Process }

// Probe performs a real launcher's namespace and confinement smoke test.
func (Driver) Probe(ctx context.Context) wall.Probe { return Probe(ctx) }

// Prepare records the sandbox's non-secret specification and starts nothing.
func (Driver) Prepare(ctx context.Context, spec wall.Spec) (wall.Sandbox, error) {
	if runtime.GOOS != "linux" {
		return nil, setupError("the process wall needs Linux", nil)
	}
	if err := ctx.Err(); err != nil {
		return nil, setupError("sandbox preparation was cancelled", err)
	}
	if spec.SessionDir == "" || !filepath.IsAbs(spec.SessionDir) || spec.Helper == "" || !filepath.IsAbs(spec.Helper) {
		return nil, setupError("sandbox paths must be absolute", nil)
	}
	if spec.Image != nil || spec.Root == wall.Keep {
		return nil, setupError("the process wall needs a fresh system root and no image", nil)
	}
	if err := os.MkdirAll(spec.SessionDir, 0700); err != nil {
		return nil, setupError("cannot make sandbox folder", err)
	}
	requestedDir := spec.SessionDir
	canonical, err := resolveSessionDir(requestedDir)
	if err != nil {
		return nil, setupError("cannot resolve the private session folder", err)
	}
	spec.Mounts = append([]wall.Mount(nil), spec.Mounts...)
	for i, mount := range spec.Mounts {
		if mount.Identity != "" {
			continue
		}
		relative, err := filepath.Rel(requestedDir, mount.Host)
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			spec.Mounts[i].Host = filepath.Join(canonical, relative)
		}
	}
	spec.SessionDir = canonical
	b, err := json.Marshal(spec)
	if err != nil {
		return nil, setupError("cannot encode sandbox specification", err)
	}
	root, err := os.OpenRoot(spec.SessionDir)
	if err != nil {
		return nil, setupError("cannot open sandbox folder", err)
	}
	defer root.Close()
	f, err := root.OpenFile("process.json.new", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, setupError("cannot write sandbox specification", err)
	}
	created, statErr := f.Stat()
	if statErr != nil {
		return nil, setupError("cannot identify new sandbox specification", errors.Join(statErr, f.Close()))
	}
	if err = writePreparedSpec(f, b); err != nil {
		return nil, setupError("cannot save sandbox specification", errors.Join(err, removePreparedTemp(root, created)))
	}
	if err = root.Rename("process.json.new", "process.json"); err != nil {
		return nil, setupError("cannot publish sandbox specification", errors.Join(err, removePreparedTemp(root, created)))
	}
	return newSandbox(spec)
}

// Open reads a prepared sandbox after the supervisor or host restarted.
func (Driver) Open(ctx context.Context, dir string) (wall.Sandbox, error) {
	if err := ctx.Err(); err != nil {
		return nil, setupError("sandbox open was cancelled", err)
	}
	canonical, err := resolveSessionDir(dir)
	if err != nil {
		return nil, setupError("cannot resolve the private session folder", err)
	}
	dir = canonical
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, setupError("cannot open sandbox folder", err)
	}
	defer root.Close()
	f, err := files.OpenRegular(root, "process.json")
	if err != nil {
		return nil, setupError("cannot read sandbox specification", err)
	}
	spec, err := readPreparedSpec(f)
	err = errors.Join(err, f.Close())
	if err != nil {
		return nil, setupError("cannot decode sandbox specification", err)
	}
	if filepath.Clean(spec.SessionDir) != filepath.Clean(dir) {
		return nil, setupError("sandbox specification names a different folder", nil)
	}
	return newSandbox(spec)
}

func writePreparedSpec(file io.WriteCloser, data []byte) error {
	n, err := file.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	return errors.Join(err, file.Close())
}

func removePreparedTemp(root *os.Root, created os.FileInfo) error {
	current, err := root.Lstat("process.json.new")
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if created == nil || !current.Mode().IsRegular() || !os.SameFile(created, current) {
		return fmt.Errorf("new sandbox specification changed before cleanup")
	}
	return root.Remove("process.json.new")
}

func readPreparedSpec(file *os.File) (wall.Spec, error) {
	var spec wall.Spec
	before, err := file.Stat()
	if err != nil {
		return spec, err
	}
	if !before.Mode().IsRegular() || before.Size() < 0 {
		return spec, fmt.Errorf("sandbox specification is not a sized regular file")
	}
	// The actual pinned file size bounds this streaming read. There is no
	// unrelated new size cap and a concurrent append cannot extend parsing.
	section := io.NewSectionReader(file, 0, before.Size())
	decoder := json.NewDecoder(section)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&spec); err != nil {
		return spec, err
	}
	// EOF permits only JSON whitespace after the ONE specification. Inspect
	// bytes directly so another huge JSON value is never decoded/allocated.
	rest := io.MultiReader(decoder.Buffered(), section)
	var buffer [4096]byte
	for {
		n, err := rest.Read(buffer[:])
		for _, value := range buffer[:n] {
			if value != ' ' && value != '\t' && value != '\r' && value != '\n' {
				return spec, fmt.Errorf("sandbox specification has trailing data")
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return spec, err
		}
	}
	after, err := file.Stat()
	if err != nil {
		return spec, err
	}
	if after.Size() != before.Size() || !os.SameFile(before, after) {
		return spec, fmt.Errorf("sandbox specification changed while decoding")
	}
	return spec, nil
}

// resolveSessionDir allows an ordinary alias in the trusted storage prefix,
// while checking that the resolved name still identifies the rooted object.
func resolveSessionDir(dir string) (string, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return "", fmt.Errorf("open private session root: %w", err)
	}
	defer root.Close()
	f, err := root.Open(".")
	if err != nil {
		return "", fmt.Errorf("open private session identity: %w", err)
	}
	defer f.Close()
	want, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("read private session identity: %w", err)
	}
	canonical, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", fmt.Errorf("resolve private session root: %w", err)
	}
	got, err := os.Stat(canonical)
	if err != nil || !os.SameFile(want, got) {
		return "", fmt.Errorf("private session root changed while resolving")
	}
	return canonical, nil
}

func setupError(message string, cause error) error {
	if cause != nil {
		message = fmt.Sprintf("%s: %v", message, cause)
	}
	return &proto.Error{Code: "wall_setup", Message: message, Hint: "Run host prepare for this host, then resume the session."}
}
