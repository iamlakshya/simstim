package client

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/files"
)

type gitMetadataTestSource struct {
	failure error
	changed bool
	closed  bool
	checked []string
}

func (s *gitMetadataTestSource) CheckKeys(_ context.Context, keys []string) error {
	s.checked = slices.Clone(keys)
	return s.failure
}
func (s *gitMetadataTestSource) Revalidate(context.Context) error {
	if s.changed || s.closed {
		return errors.New("original source changed")
	}
	return nil
}
func (s *gitMetadataTestSource) Close() error { s.closed = true; return nil }

func TestGitMetadataRejectsNamespaceBeforeAnyLookup(t *testing.T) {
	failure := errors.New("canonical namespace collision")
	source := &gitMetadataTestSource{failure: failure}
	reads := 0
	_, err := captureGitMetadata(t.Context(), source, func(string) (string, bool) { reads++; return "must not read", true }, false)
	if !errors.Is(err, failure) || reads != 0 || !source.closed {
		t.Fatalf("namespace gate: reads=%d closed=%v err=%v", reads, source.closed, err)
	}
	if !slices.Contains(source.checked, "PATH") || !slices.Contains(source.checked, "GIT_INDEX_FILE") {
		t.Fatal("closed policy did not supply all role keys")
	}
}

func TestGitMetadataRolesExcludeCredentialsAndPreservePresence(t *testing.T) {
	for _, windows := range []bool{false, true} {
		source := &gitMetadataTestSource{}
		marker := "unique-managed-value-must-never-cross"
		values := map[string]string{"PATH": "ordinary search path", "HOME": "ordinary home with spaces \u03bb", "TMPDIR": "ordinary temp", "SYSTEMROOT": "ordinary Windows root", "GORACE": "", "AWS_SECRET_ACCESS_KEY": marker, "SSH_AUTH_SOCK": marker, "GPG_TTY": marker, "HTTPS_PROXY": marker, "GIT_CONFIG_GLOBAL": marker}
		reads := []string{}
		metadata, err := captureGitMetadata(t.Context(), source, func(key string) (string, bool) { reads = append(reads, key); v, ok := values[key]; return v, ok }, windows)
		if err != nil {
			t.Fatal(err)
		}
		for _, role := range []localGitRole{localGitNormal, localGitIdentity, localGitHelper, localGitDirty} {
			env, err := metadata.environment(t.Context(), role, filepath.Join(t.TempDir(), "private-index"), api.GitIdentity{Known: true, Name: "ordinary author", Email: "ordinary@example.invalid"})
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range env {
				if strings.Contains(entry, marker) {
					t.Fatal("credential marker crossed a closed role")
				}
			}
			if slices.Contains(env, "XDG_CONFIG_HOME=") {
				t.Fatal("absent optional metadata became present empty")
			}
			if role == localGitHelper && !slices.Contains(env, "GORACE=") {
				t.Fatal("present empty runtime metadata was lost")
			}
			if role == localGitIdentity && slices.Contains(env, "GIT_CONFIG_NOSYSTEM=1") {
				t.Fatal("identity role lost native static configuration")
			}
			if role == localGitNormal && !slices.Contains(env, "GIT_CONFIG_GLOBAL="+os.DevNull) {
				t.Fatal("normal role gained global configuration")
			}
		}
		for _, key := range []string{"AWS_SECRET_ACCESS_KEY", "SSH_AUTH_SOCK", "GPG_TTY", "HTTPS_PROXY", "GIT_CONFIG_GLOBAL"} {
			if slices.Contains(reads, key) {
				t.Fatal("unexpected environment value was looked up")
			}
		}
		source.changed = true
		if err := metadata.Revalidate(t.Context()); err == nil {
			t.Fatal("changed original source was renewed")
		}
		metadata.Close()
	}
}

func TestGitMetadataWireRefusesAliasesUnexpectedNamesAndRoleMismatch(t *testing.T) {
	metadata, err := captureGitMetadata(t.Context(), &gitMetadataTestSource{}, func(key string) (string, bool) {
		if key == "SYSTEMROOT" {
			return "ordinary system root", true
		}
		return "", false
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	defer metadata.Close()
	env, err := metadata.environment(t.Context(), localGitNormal, "", api.GitIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	for _, extra := range []string{"AWS_SECRET_ACCESS_KEY=do-not-print", "path=alias", "SYSTEMROOT=duplicate", "GIT_AUTHOR_NAME=wrong-role"} {
		bad := append(slices.Clone(env), extra)
		err := validateGitRoleEnvironment(localGitNormal, bad, true)
		if err == nil || strings.Contains(err.Error(), "do-not-print") {
			t.Fatal("unexpected or aliased role environment was accepted or printed")
		}
	}
	if err := validateGitRoleEnvironment(localGitIdentity, env, true); err == nil {
		t.Fatal("normal environment was accepted as static identity")
	}
}

func gitMetadataTestRequest(t *testing.T, executable, directory string, args ...string) localGitRequest {
	t.Helper()
	metadata, err := captureGitMetadata(t.Context(), &gitMetadataTestSource{}, os.LookupEnv, runtime.GOOS == "windows")
	if err != nil {
		t.Fatal(err)
	}
	defer metadata.Close()
	normal, err := metadata.environment(t.Context(), localGitNormal, "", api.GitIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	helper, err := metadata.environment(t.Context(), localGitHelper, "", api.GitIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	identity := "frame-only"
	if file, openErr := os.Open(executable); openErr == nil {
		identity, err = files.Identity(file)
		file.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	return localGitRequest{Executable: executable, ExecutableID: identity, Directory: directory, Args: args, Role: localGitNormal, Env: normal, HelperEnv: helper, HelperDirectory: directory}
}

func gitFixtureMetadataEnvironment(t *testing.T) []string {
	t.Helper()
	metadata, err := captureGitMetadata(t.Context(), &gitMetadataTestSource{}, os.LookupEnv, runtime.GOOS == "windows")
	if err != nil {
		t.Fatal(err)
	}
	defer metadata.Close()
	environment, err := metadata.environment(t.Context(), localGitNormal, "", api.GitIdentity{})
	if err != nil {
		t.Fatal(err)
	}
	return environment
}
