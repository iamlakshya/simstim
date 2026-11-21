package client

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/iamlakshya/simstim/internal/brand"
)

const editorSSHGeneratedV1 = "# Managed editor SSH configuration.\n"

// EditorSSHSetup retains the actual safety probe and owned generated files.
// Include publication is a separate explicit action after the CLI's consent.
type EditorSSHSetup struct {
	probe  *editorSSHProbeV1
	key    *SSHHostKey
	config *editorSSHFileV1
	hosts  *editorSSHFileV1
	home   string
	closed bool
}

func editorSSHPathV1(path string) (string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, "\x00\r\n") {
		return "", editorSSHFileErrorV1(fmt.Errorf("SSH configuration has an unsafe path"))
	}
	return strconv.Quote(strings.ReplaceAll(filepath.ToSlash(path), "%", "%%")), nil
}

func renderEditorSSHConfigV1(hosts string) ([]byte, error) {
	quoted, err := editorSSHPathV1(hosts)
	if err != nil {
		return nil, err
	}
	return []byte(fmt.Sprintf(editorSSHGeneratedV1+"Host *.%s\n    ProxyCommand %s ssh-proxy %%n\n    User agent\n    IdentityAgent none\n    IdentityFile /dev/null\n    IdentitiesOnly yes\n    ForwardAgent no\n    ForwardX11 no\n    PermitLocalCommand no\n    ControlMaster no\n    StrictHostKeyChecking yes\n    UserKnownHostsFile %s\n", brand.Name, brand.Name, quoted)), nil
}

func renderEditorKnownHostsV1(key ssh.PublicKey) []byte {
	return []byte(editorSSHGeneratedV1 + "*." + brand.Name + " " + strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))) + "\n")
}

// PrepareEditorSSHConfig runs the safety probe before any persistent write,
// then creates the concrete key/config/pin that the user can review.
func PrepareEditorSSHConfig(ctx context.Context) (_ *EditorSSHSetup, returned error) {
	probe, err := probeEditorSSHConfigV1(ctx)
	if err != nil {
		return nil, err
	}
	setup := &EditorSSHSetup{probe: probe, home: filepath.Clean(home())}
	keep := false
	defer func() {
		if !keep {
			returned = errors.Join(returned, setup.Close())
		}
	}()
	if _, err := editorSSHPathV1(setup.home); err != nil {
		return nil, err
	}
	setup.key, err = editorSSHHostKeyV1(ctx, probe)
	if err != nil {
		return nil, err
	}
	directory := filepath.Join(setup.home, ".ssh", brand.Name)
	setup.config, err = openEditorSSHFileV1(ctx, directory, "config", true)
	if err != nil {
		return nil, err
	}
	setup.hosts, err = openEditorSSHFileV1(ctx, directory, "known_hosts", true)
	if err != nil {
		return nil, err
	}
	config, err := renderEditorSSHConfigV1(filepath.Join(directory, "known_hosts"))
	if err != nil {
		return nil, err
	}
	if err := probe.check(ctx); err != nil {
		return nil, err
	}
	if !bytes.Equal(setup.config.contents, config) {
		if err := setup.config.replace(ctx, config); err != nil {
			return nil, err
		}
	}
	known := renderEditorKnownHostsV1(setup.key.PublicKey())
	if err := probe.check(ctx); err != nil {
		return nil, err
	}
	if !bytes.Equal(setup.hosts.contents, known) {
		if err := setup.hosts.replace(ctx, known); err != nil {
			return nil, err
		}
	}
	keep = true
	return setup, nil
}

func (s *EditorSSHSetup) ConfigPath() string {
	if s == nil {
		return ""
	}
	return filepath.Join(s.home, ".ssh", brand.Name, "config")
}

func (s *EditorSSHSetup) IncludeLine() (string, error) {
	quoted, err := editorSSHPathV1(s.ConfigPath())
	if err != nil {
		return "", err
	}
	return "Include " + quoted + "\n", nil
}

func (s *EditorSSHSetup) check(ctx context.Context) error {
	if s == nil || s.closed || s.probe == nil || s.key == nil || s.config == nil || s.hosts == nil {
		return editorSSHFileErrorV1(fmt.Errorf("editor setup has no retained original files"))
	}
	return errors.Join(s.probe.check(ctx), s.config.check(ctx), s.hosts.check(ctx))
}

// InstallInclude publishes only the exact generated Include after explicit
// consent. It preserves every existing line of the user's SSH config.
func (s *EditorSSHSetup) InstallInclude(ctx context.Context) (bool, error) {
	if err := s.check(ctx); err != nil {
		return false, err
	}
	line, err := s.IncludeLine()
	if err != nil {
		return false, err
	}
	owned, err := openEditorSSHFileV1(ctx, filepath.Join(s.home, ".ssh"), "config", false)
	if err != nil {
		return false, err
	}
	defer owned.close()
	if exactEditorIncludeV1(owned.contents, line) {
		return false, nil
	}
	if err := s.check(ctx); err != nil {
		return false, err
	}
	if err := owned.replace(ctx, append([]byte(line), owned.contents...)); err != nil {
		return false, err
	}
	return true, nil
}

func exactEditorIncludeV1(contents []byte, include string) bool {
	wanted := strings.TrimSuffix(include, "\n")
	for _, line := range strings.Split(string(contents), "\n") {
		if strings.TrimSuffix(line, "\r") == wanted {
			return true
		}
	}
	return false
}

func (s *EditorSSHSetup) Close() error {
	if s == nil || s.closed {
		return nil
	}
	s.closed = true
	var failure error
	if s.hosts != nil {
		failure = errors.Join(failure, s.hosts.close())
	}
	if s.config != nil {
		failure = errors.Join(failure, s.config.close())
	}
	if s.key != nil {
		failure = errors.Join(failure, s.key.Close())
	}
	if s.probe != nil {
		failure = errors.Join(failure, s.probe.close())
	}
	return failure
}

// RemoveEditorSSHConfig does not rotate or remove the persistent host key.
// The parser safety probe precedes every removal write too.
func RemoveEditorSSHConfig(ctx context.Context) (returned error) {
	probe, err := probeEditorSSHConfigV1(ctx)
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, probe.close()) }()
	base := filepath.Join(filepath.Clean(home()), ".ssh")
	quoted, err := editorSSHPathV1(filepath.Join(base, brand.Name, "config"))
	if err != nil {
		return err
	}
	include := "Include " + quoted + "\n"
	// Use the same generated-config, pin, user-config lock order as setup.
	generated := make([]*editorSSHFileV1, 0, 2)
	defer func() {
		for index := len(generated) - 1; index >= 0; index-- {
			returned = errors.Join(returned, generated[index].close())
		}
	}()
	for _, name := range []string{"config", "known_hosts"} {
		file, err := openEditorSSHFileV1(ctx, filepath.Join(base, brand.Name), name, false)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return err
		}
		generated = append(generated, file)
		if !file.absent && !bytes.HasPrefix(file.contents, []byte(editorSSHGeneratedV1)) {
			return editorSSHFileErrorV1(fmt.Errorf("generated SSH file has unrelated contents"))
		}
	}
	user, err := openEditorSSHFileV1(ctx, base, "config", false)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer func() { returned = errors.Join(returned, user.close()) }()
	var kept strings.Builder
	for _, line := range strings.SplitAfter(string(user.contents), "\n") {
		if strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r") != strings.TrimSuffix(include, "\n") {
			kept.WriteString(line)
		}
	}
	if err := probe.check(ctx); err != nil {
		return err
	}
	for _, file := range generated {
		if err := file.check(ctx); err != nil {
			return err
		}
	}
	if kept.String() != string(user.contents) {
		if err := user.replace(ctx, []byte(kept.String())); err != nil {
			return err
		}
	}
	for _, file := range generated {
		if err := probe.check(ctx); err != nil {
			return err
		}
		if err := file.remove(ctx); err != nil {
			return err
		}
	}
	return nil
}
