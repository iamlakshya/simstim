package process

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

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
	_, err = f.Write(b)
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		root.Remove("process.json.new")
		return nil, setupError("cannot save sandbox specification", err)
	}
	if err = root.Rename("process.json.new", "process.json"); err != nil {
		return nil, setupError("cannot publish sandbox specification", err)
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
	f, err := root.Open("process.json")
	if err != nil {
		return nil, setupError("cannot read sandbox specification", err)
	}
	defer f.Close()
	var spec wall.Spec
	d := json.NewDecoder(f)
	d.DisallowUnknownFields()
	if err = d.Decode(&spec); err != nil {
		return nil, setupError("cannot decode sandbox specification", err)
	}
	if filepath.Clean(spec.SessionDir) != filepath.Clean(dir) {
		return nil, setupError("sandbox specification names a different folder", nil)
	}
	return newSandbox(spec)
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
