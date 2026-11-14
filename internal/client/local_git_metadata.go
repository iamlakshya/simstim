package client

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/iamlakshya/simstim/internal/api"
)

type localGitRole uint8

const (
	localGitNormal localGitRole = iota + 1
	localGitIdentity
	localGitDirty
	localGitHelper
)

// LocalGitMetadataOpener borrows the original canonical name/source operation.
// The application supplies its real constructor in the SAME source dependency;
// a nil opener, empty substitute settings or remote name map grants nothing.
type LocalGitMetadataOpener func(context.Context) (LocalGitMetadataSource, error)

var gitOrdinaryMetadata = []string{"PATH", "HOME", "PWD", "TMPDIR", "TZ"}
var gitIdentityMetadata = []string{"XDG_CONFIG_HOME"}
var gitWindowsMetadata = []string{"USERPROFILE", "HOMEDRIVE", "HOMEPATH", "TMP", "TEMP", "SYSTEMROOT", "WINDIR", "MSYSTEM"}
var gitHelperRuntimeMetadata = []string{"GORACE", "GOMAXPROCS", "GOMEMLIMIT", "GOGC", "GOTRACEBACK", "GODEBUG"}
var gitDirtyMetadata = []string{"GIT_INDEX_FILE", "GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL"}

func gitMetadataKey(key string, windows bool) string {
	if windows {
		return strings.ToUpper(key)
	}
	return key
}

func gitMetadataLookupKeys(windows bool) []string {
	keys := slices.Concat(gitOrdinaryMetadata, gitIdentityMetadata, gitHelperRuntimeMetadata)
	if windows {
		keys = append(keys, gitWindowsMetadata...)
	}
	return keys
}

func gitMetadataCheckKeys(windows bool) []string {
	keys := append(gitMetadataLookupKeys(windows), "LC_ALL", "GIT_ALLOW_PROTOCOL", "GIT_NO_LAZY_FETCH", "GIT_TERMINAL_PROMPT", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_NOSYSTEM")
	keys = append(keys, gitDirtyMetadata...)
	for index := range keys {
		keys[index] = gitMetadataKey(keys[index], windows)
	}
	return slices.Compact(slices.Sorted(slices.Values(keys)))
}

func gitRoleKeys(role localGitRole, windows bool) []string {
	if role == localGitHelper {
		keys := slices.Concat([]string{"PATH", "PWD", "TMPDIR", "TZ"}, gitHelperRuntimeMetadata)
		if windows {
			keys = append(keys, "SYSTEMROOT", "WINDIR", "TMP", "TEMP", "USERPROFILE")
		}
		return keys
	}
	keys := slices.Clone(gitOrdinaryMetadata)
	if windows {
		keys = append(keys, gitWindowsMetadata...)
	}
	if role == localGitIdentity {
		keys = append(keys, gitIdentityMetadata...)
	}
	return keys
}

func gitMetadataFailure() error {
	return &UserError{Code: "git_metadata", Msg: "the original native Git metadata environment cannot be checked",
		Hint: "Start the operation through its canonical local settings and source flow.", Kind: KindRefused}
}

type gitMetadataValue struct {
	key, value string
	present    bool
}

type localGitMetadata struct {
	mu      sync.Mutex
	source  LocalGitMetadataSource
	values  []gitMetadataValue
	windows bool
	closed  bool
}

func captureGitMetadata(ctx context.Context, source LocalGitMetadataSource, lookup func(string) (string, bool), windows bool) (_ *localGitMetadata, err error) {
	if source == nil || lookup == nil {
		return nil, gitMetadataFailure()
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, source.Close())
		}
	}()
	// The ONLY canonical fixed key policy is passed to the actual all-final-
	// definition visitor before the first candidate value or PATH lookup.
	if err := source.CheckKeys(ctx, gitMetadataCheckKeys(windows)); err != nil {
		return nil, err
	}
	if err := source.Revalidate(ctx); err != nil {
		return nil, err
	}
	metadata := &localGitMetadata{source: source, windows: windows}
	for _, key := range gitMetadataLookupKeys(windows) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		value, present := lookup(key)
		if present && (!utf8.ValidString(value) || strings.ContainsRune(value, 0)) {
			return nil, gitMetadataFailure()
		}
		metadata.values = append(metadata.values, gitMetadataValue{key: gitMetadataKey(key, windows), value: value, present: present})
	}
	if err := source.Revalidate(ctx); err != nil {
		return nil, err
	}
	return metadata, nil
}

func (m *localGitMetadata) value(key string) (string, bool) {
	key = gitMetadataKey(key, m.windows)
	for _, entry := range m.values {
		if entry.key == key {
			return entry.value, entry.present
		}
	}
	return "", false
}

func (m *localGitMetadata) environment(ctx context.Context, role localGitRole, index string, identity api.GitIdentity) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || role < localGitNormal || role > localGitHelper {
		return nil, gitMetadataFailure()
	}
	if err := m.source.Revalidate(ctx); err != nil {
		return nil, err
	}
	environment := make([]string, 0, len(m.values)+12) // always explicit, even if empty
	for _, key := range gitRoleKeys(role, m.windows) {
		if value, present := m.value(key); present {
			environment = append(environment, gitMetadataKey(key, m.windows)+"="+value)
		}
	}
	environment = append(environment, "LC_ALL=C")
	if role != localGitHelper {
		environment = append(environment, "GIT_ALLOW_PROTOCOL=file", "GIT_NO_LAZY_FETCH=1", "GIT_TERMINAL_PROMPT=0")
		if role != localGitIdentity {
			environment = append(environment, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
		}
	}
	if role == localGitDirty {
		if !filepath.IsAbs(index) || !identity.Known || identity.Name == "" || identity.Email == "" || strings.ContainsAny(identity.Name+identity.Email, "\x00\r\n") {
			return nil, gitMetadataFailure()
		}
		environment = append(environment, "GIT_INDEX_FILE="+index, "GIT_AUTHOR_NAME="+identity.Name, "GIT_AUTHOR_EMAIL="+identity.Email,
			"GIT_COMMITTER_NAME="+identity.Name, "GIT_COMMITTER_EMAIL="+identity.Email)
	}
	if err := validateGitRoleEnvironment(role, environment, m.windows); err != nil {
		return nil, err
	}
	return environment, nil
}

func (m *localGitMetadata) Revalidate(ctx context.Context) error {
	if m == nil {
		return gitMetadataFailure()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return gitMetadataFailure()
	}
	return m.source.Revalidate(ctx)
}

func (m *localGitMetadata) Close() error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	m.closed = true
	return m.source.Close()
}

func helperGitMetadataEnvironment() ([]string, error) {
	// The parent already supplied an explicit metadata-only startup block.
	// Inspect the ACTUAL helper block, so a mistaken nil/ambient launch fails
	// before READY rather than silently filtering the resulting inheritance.
	environment := os.Environ()
	if err := validateGitRoleEnvironment(localGitHelper, environment, runtime.GOOS == "windows"); err != nil {
		return nil, err
	}
	return slices.Clone(environment), nil
}

func validateGitRoleEnvironment(role localGitRole, environment []string, windows bool) error {
	if role < localGitNormal || role > localGitHelper || environment == nil {
		return gitMetadataFailure()
	}
	allowed := gitRoleKeys(role, windows)
	allowed = append(allowed, "LC_ALL")
	if role != localGitHelper {
		allowed = append(allowed, "GIT_ALLOW_PROTOCOL", "GIT_NO_LAZY_FETCH", "GIT_TERMINAL_PROMPT")
		if role != localGitIdentity {
			allowed = append(allowed, "GIT_CONFIG_GLOBAL", "GIT_CONFIG_NOSYSTEM")
		}
	}
	if role == localGitDirty {
		allowed = append(allowed, gitDirtyMetadata...)
	}
	for index := range allowed {
		allowed[index] = gitMetadataKey(allowed[index], windows)
	}
	seen := map[string]string{} // fixed role keys only; never configured names
	for _, entry := range environment {
		key, value, valid := strings.Cut(entry, "=")
		canonical := gitMetadataKey(key, windows)
		_, duplicate := seen[canonical]
		if !valid || duplicate || key != canonical || !slices.Contains(allowed, canonical) || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
			return gitMetadataFailure()
		}
		seen[canonical] = value
	}
	if seen["LC_ALL"] != "C" || windows && seen["SYSTEMROOT"] == "" {
		return gitMetadataFailure()
	}
	if role == localGitHelper {
		return nil
	}
	if seen["GIT_ALLOW_PROTOCOL"] != "file" || seen["GIT_NO_LAZY_FETCH"] != "1" || seen["GIT_TERMINAL_PROMPT"] != "0" {
		return gitMetadataFailure()
	}
	if role != localGitIdentity && (seen["GIT_CONFIG_GLOBAL"] != os.DevNull || seen["GIT_CONFIG_NOSYSTEM"] != "1") {
		return gitMetadataFailure()
	}
	if role == localGitDirty {
		for _, key := range gitDirtyMetadata {
			if seen[key] == "" || strings.ContainsAny(seen[key], "\r\n") {
				return gitMetadataFailure()
			}
		}
		if !filepath.IsAbs(seen["GIT_INDEX_FILE"]) {
			return gitMetadataFailure()
		}
	}
	return nil
}

func validateGitRequestRole(request localGitRequest) error {
	if request.Role == localGitIdentity && !slices.Equal(request.Args, []string{"config", "--includes", "--null", "--get-regexp", `^user\.(name|email)$`}) {
		return gitMetadataFailure()
	}
	if request.Role == localGitDirty && (len(request.Args) == 0 || !slices.Contains([]string{"read-tree", "update-index", "write-tree", "hash-object", "commit-tree"}, request.Args[0])) {
		return gitMetadataFailure()
	}
	if request.Role == localGitHelper {
		return gitMetadataFailure()
	}
	if err := validateGitRoleEnvironment(request.Role, request.Env, runtime.GOOS == "windows"); err != nil {
		return err
	}
	return nil
}
