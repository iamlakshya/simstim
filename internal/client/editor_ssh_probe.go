package client

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
)

// editorSSHProbeV1 belongs only to the actual completed local SSH probe. It
// cannot be reconstructed from a version string or a caller's boolean answer.
type editorSSHProbeV1 struct {
	path string
	file *os.File
	info os.FileInfo
}

func editorSSHProbeErrorV1() error {
	return &UserError{Msg: "This ssh passes shell characters in host names to ProxyCommand, or could not safely reject the probe.", Hint: "Update OpenSSH to 9.6 or later, then run " + brand.Name + " ssh-config again.", Code: "ssh_config_unsafe", Kind: KindRefused}
}

func probeEditorSSHConfigV1(ctx context.Context) (*editorSSHProbeV1, error) {
	name, err := exec.LookPath("ssh")
	if err != nil {
		return nil, editorSSHProbeErrorV1()
	}
	return runEditorSSHProbeV1(ctx, name)
}

func runEditorSSHProbeV1(ctx context.Context, name string) (_ *editorSSHProbeV1, returned error) {
	if ctx == nil {
		return nil, editorSSHProbeErrorV1()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path, err := filepath.Abs(name)
	if err != nil {
		return nil, editorSSHProbeErrorV1()
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return nil, editorSSHProbeErrorV1()
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, editorSSHProbeErrorV1()
	}
	keep := false
	defer func() {
		if !keep {
			_ = file.Close()
		}
	}()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		return nil, editorSSHProbeErrorV1()
	}
	proof := &editorSSHProbeV1{path: path, file: file, info: info}
	if err := proof.check(ctx); err != nil {
		return nil, err
	}
	bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	probe := "probe$x.check." + brand.Name
	// -G does not connect. Ignore the user's config so Match/Include cannot
	// turn a parser safety check into a separate configured command.
	cmd := exec.CommandContext(bounded, path, "-F", os.DevNull, "-o", "CanonicalizeHostname=no", "-G", probe)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	cmd.Stdout = io.Discard
	var diagnostic editorSSHProbeOutputV1
	cmd.Stderr = &diagnostic
	cmd.WaitDelay = sshWaitDelay
	err = cmd.Run() // this call owns and joins the actual probe child
	if stopped := bounded.Err(); stopped != nil {
		return nil, stopped
	}
	var exited *exec.ExitError
	if !errors.As(err, &exited) || exited.ExitCode() != 255 || !strings.Contains(strings.ToLower(string(diagnostic.data)), "hostname contains invalid characters") {
		return nil, editorSSHProbeErrorV1()
	}
	if err := proof.check(ctx); err != nil {
		return nil, err
	}
	keep = true
	return proof, nil
}

func (p *editorSSHProbeV1) check(ctx context.Context) error {
	if p == nil || p.file == nil || p.info == nil || ctx == nil {
		return editorSSHProbeErrorV1()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	opened, err := p.file.Stat()
	if err != nil || !os.SameFile(opened, p.info) || opened.Size() != p.info.Size() || !opened.ModTime().Equal(p.info.ModTime()) {
		return editorSSHProbeErrorV1()
	}
	current, err := os.Lstat(p.path)
	if err != nil || !current.Mode().IsRegular() || !os.SameFile(current, opened) || current.Size() != opened.Size() || !current.ModTime().Equal(opened.ModTime()) {
		return editorSSHProbeErrorV1()
	}
	return nil
}

func (p *editorSSHProbeV1) close() error {
	if p == nil || p.file == nil {
		return nil
	}
	file := p.file
	p.file = nil
	return file.Close()
}

type editorSSHProbeOutputV1 struct{ data []byte }

func (w *editorSSHProbeOutputV1) Write(data []byte) (int, error) {
	const maximum = 8 << 10
	if room := maximum - len(w.data); room > 0 {
		w.data = append(w.data, data[:min(room, len(data))]...)
	}
	return len(data), nil
}
