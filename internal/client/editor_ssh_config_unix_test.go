//go:build !windows

package client

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/iamlakshya/simstim/internal/brand"
)

func editorSSHProbeFixtureV1(t *testing.T, body string) string {
	t.Helper()
	directory := t.TempDir()
	name := filepath.Join(directory, "ssh")
	if err := os.WriteFile(name, []byte("#!/bin/sh\n"+body), 0o700); err != nil {
		t.Fatal(err)
	}
	return name
}

func safeEditorSSHProbeV1(t *testing.T) *editorSSHProbeV1 {
	t.Helper()
	name := editorSSHProbeFixtureV1(t, "[ \"$5\" = -G ] || exit 2\n[ \"$6\" = 'probe$x.check."+brand.Name+"' ] || exit 2\nprintf '%s\\n' 'hostname contains invalid characters' >&2\nexit 255\n")
	proof, err := runEditorSSHProbeV1(t.Context(), name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = proof.close() })
	return proof
}

func TestEditorSSHProbeRefusesAcceptanceUnsupportedAndUnrelatedFailures(t *testing.T) {
	stateFDTestHome(t)
	for name, body := range map[string]string{
		"accepted unsafe hostname": "exit 0\n",
		"unsupported probe":        "printf '%s\\n' 'unknown option G' >&2\nexit 255\n",
		"unrelated failure":        "printf '%s\\n' 'configuration cannot be read' >&2\nexit 255\n",
		"wrong error status":       "printf '%s\\n' 'hostname contains invalid characters' >&2\nexit 1\n",
	} {
		t.Run(name, func(t *testing.T) {
			if proof, err := runEditorSSHProbeV1(t.Context(), editorSSHProbeFixtureV1(t, body)); err == nil || proof != nil {
				t.Fatal("unsafe or unverified parser behaviour admitted configuration")
			}
			if _, err := os.Stat(filepath.Join(StateDir(), editorSSHHostKeyNameV1)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("an unsafe parser probe changed persistent key state", err)
			}
		})
	}
}

func TestEditorSSHProbeRetainsActualExecutableIdentity(t *testing.T) {
	proof := safeEditorSSHProbeV1(t)
	if err := os.Rename(proof.path, proof.path+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(proof.path, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := proof.check(t.Context()); err == nil {
		t.Fatal("a substituted executable renewed the original parser probe")
	}
}

func TestEditorSSHHostKeyNativeStatePersistsAndOwnedSignerCloses(t *testing.T) {
	stateFDTestHome(t)
	if _, err := LoadSSHHostKey(t.Context()); err == nil {
		t.Fatal("proxy startup generated an absent host key")
	}
	proof := safeEditorSSHProbeV1(t)
	first, err := editorSSHHostKeyV1(t.Context(), proof)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	info, err := os.Stat(filepath.Join(StateDir(), editorSSHHostKeyNameV1))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("local host key was not published with mode 0600", err)
	}
	second, err := LoadSSHHostKey(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if !bytes.Equal(first.PublicKey().Marshal(), second.PublicKey().Marshal()) || first.PublicKey().Type() != ssh.KeyAlgoED25519 {
		t.Fatal("loading the same native state rotated or changed its host key")
	}
	message := []byte("local SSH host-key ownership regression")
	signature, err := first.Sign(rand.Reader, message)
	if err != nil || second.PublicKey().Verify(message, signature) != nil {
		t.Fatal("the owned copied key could not sign its handshake data", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := first.Sign(rand.Reader, message); err == nil {
		t.Fatal("a closed owned signer could still use its private key")
	}
}

func TestEditorSSHWriterRefusesReplacedFileAndRoot(t *testing.T) {
	for _, variant := range []string{"file", "root"} {
		t.Run(variant, func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "ssh")
			if err := os.Mkdir(directory, 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(directory, "config")
			if err := os.WriteFile(path, []byte("Host original\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			owned, err := openEditorSSHFileV1(t.Context(), directory, "config", false)
			if err != nil {
				t.Fatal(err)
			}
			defer owned.close()
			if variant == "file" {
				if err := os.Rename(path, path+".old"); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Rename(directory, directory+".old"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(directory, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(path, []byte("Host replacement\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := owned.replace(t.Context(), []byte("overwritten\n")); err == nil {
				t.Fatal("a substituted source admitted configuration publication")
			}
			if contents, err := os.ReadFile(path); err != nil || string(contents) != "Host replacement\n" {
				t.Fatal("source refusal changed the replacement", err)
			}
		})
	}
}

func TestEditorSSHWriterLockHonoursActualContext(t *testing.T) {
	directory := t.TempDir()
	first, err := openEditorSSHFileV1(t.Context(), directory, "config", false)
	if err != nil {
		t.Fatal(err)
	}
	defer first.close()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if _, err := openEditorSSHFileV1(ctx, directory, "config", false); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("contended native writer did not preserve its cancellation", err)
	}
}

func TestEditorSSHConfigUsesLiteralNameAndClosedOptions(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "ssh")
	contents, err := renderEditorSSHConfigV1(filepath.Join(directory, "known_hosts"))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"Host *." + brand.Name, "ProxyCommand " + brand.Name + " ssh-proxy %n", "User agent", "IdentityAgent none", "IdentityFile /dev/null", "ForwardAgent no", "ForwardX11 no", "PermitLocalCommand no", "ControlMaster no", "StrictHostKeyChecking yes"} {
		if !strings.Contains(string(contents), required) {
			t.Fatal("generated SSH config omitted a closed transport option", required)
		}
	}
	if strings.Contains(string(contents), "%r") {
		t.Fatal("the remote username entered a proxy shell command")
	}
}

func TestEditorSSHSetupPreparesFilesBeforeIncludeConsentAndRemovesOnlyItsInclude(t *testing.T) {
	stateFDTestHome(t)
	name := editorSSHProbeFixtureV1(t, "printf '%s\\n' 'hostname contains invalid characters' >&2\nexit 255\n")
	t.Setenv("PATH", filepath.Dir(name)+string(os.PathListSeparator)+os.Getenv("PATH"))
	sshDir := filepath.Join(home(), ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	original := []byte("# user configuration\nHost preserved\n    User original\n")
	userFile := filepath.Join(sshDir, "config")
	if err := os.WriteFile(userFile, original, 0o644); err != nil {
		t.Fatal(err)
	}
	setup, err := PrepareEditorSSHConfig(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if contents, err := os.ReadFile(userFile); err != nil || !bytes.Equal(contents, original) {
		t.Fatal("preparation published an Include without consent", err)
	}
	if _, err := os.Stat(setup.ConfigPath()); err != nil {
		t.Fatal("the concrete generated config was absent before consent", err)
	}
	line, err := setup.IncludeLine()
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := setup.InstallInclude(t.Context()); err != nil || !changed {
		t.Fatal("explicit Include publication failed", err)
	}
	if changed, err := setup.InstallInclude(t.Context()); err != nil || changed {
		t.Fatal("a repeated Include publication duplicated the line", err)
	}
	if contents, err := os.ReadFile(userFile); err != nil || string(contents) != line+string(original) {
		t.Fatal("Include publication changed another SSH setting", err)
	}
	if err := setup.Close(); err != nil {
		t.Fatal(err)
	}
	if err := RemoveEditorSSHConfig(t.Context()); err != nil {
		t.Fatal(err)
	}
	if contents, err := os.ReadFile(userFile); err != nil || !bytes.Equal(contents, original) {
		t.Fatal("removal changed another SSH setting", err)
	}
	for _, file := range []string{"config", "known_hosts"} {
		if _, err := os.Stat(filepath.Join(sshDir, brand.Name, file)); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("generated SSH file remained after removal", file, err)
		}
	}
	if _, err := os.Stat(filepath.Join(StateDir(), editorSSHHostKeyNameV1)); err != nil {
		t.Fatal("configuration removal rotated or removed the laptop's persistent key", err)
	}
	if err := RemoveEditorSSHConfig(t.Context()); err != nil {
		t.Fatal("repeated removal was not idempotent", err)
	}
}

func TestEditorSSHUnsafeProbePublishesNoKeyOrConfig(t *testing.T) {
	stateFDTestHome(t)
	name := editorSSHProbeFixtureV1(t, "exit 0\n")
	t.Setenv("PATH", filepath.Dir(name)+string(os.PathListSeparator)+os.Getenv("PATH"))
	if _, err := PrepareEditorSSHConfig(t.Context()); err == nil {
		t.Fatal("an accepting SSH parser reached setup publication")
	}
	for _, path := range []string{filepath.Join(StateDir(), editorSSHHostKeyNameV1), filepath.Join(home(), ".ssh", brand.Name, "config"), filepath.Join(home(), ".ssh", "config")} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("unsafe parser probe changed persistent state", err)
		}
	}
}
