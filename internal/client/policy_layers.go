package client

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/proto"
)

// ReadPolicySettings refuses missing layers caused by a broken config. The
// ordinary read-only settings loader retains its warning-and-defaults policy.
func ReadPolicySettings() (Settings, error) {
	settings, _ := ReadSettings()
	if err := ValidateSettings(settings); err != nil {
		return Settings{}, err
	}
	return settings, nil
}

// NormaliseOrigin names a repository without credentials, scheme or .git.
// The path keeps its case: forge repository names need not be case-insensitive.
func NormaliseOrigin(origin string) (string, error) {
	s := strings.TrimSpace(origin)
	if strings.Contains(s, "://") {
		u, err := url.Parse(s)
		if err != nil || u.Hostname() == "" || u.RawQuery != "" || u.Fragment != "" {
			return "", badOrigin()
		}
		s = strings.ToLower(u.Hostname()) + "/" + strings.TrimPrefix(u.Path, "/")
	} else if at := strings.LastIndex(s, "@"); at >= 0 {
		s = s[at+1:]
		host, path, ok := strings.Cut(s, ":")
		if !ok {
			return "", badOrigin()
		}
		s = strings.ToLower(host) + "/" + path
	}
	s = strings.TrimSuffix(strings.TrimSuffix(s, "/"), ".git")
	host, path, ok := strings.Cut(s, "/")
	if !ok || !strings.Contains(host, ".") || path == "" || strings.ContainsAny(s, "\x00\r\n\\?#") || strings.HasPrefix(path, "/") || strings.Contains(path, "../") {
		return "", badOrigin()
	}
	return strings.ToLower(host) + "/" + path, nil
}

func badOrigin() error {
	return &UserError{Msg: "the repository needs a usable origin remote", Hint: "Set remote.origin.url, or name an origin such as forge.example/team/app.", Code: "repo_origin", Kind: KindUsage}
}

// RepoOrigin resolves a local folder's origin or a named remote. It never
// contacts the remote and uses only a read-only git config operation.
func RepoOrigin(ctx context.Context, repo string) (string, error) {
	if repo == "" {
		return "", nil
	}
	if stat, err := os.Stat(repo); err == nil && stat.IsDir() {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "git", "-C", repo, "-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null", "config", "--get", "remote.origin.url")
		cmd.WaitDelay = time.Second
		out, err := cmd.Output()
		if err != nil {
			return "", badOrigin()
		}
		return NormaliseOrigin(string(out))
	}
	return NormaliseOrigin(repo)
}

// ResolvePolicyLayers materialises the five laptop layers. The host adds
// built-in rules and its trusted-source repository file. Matching repository
// sections have a stable broadest-first order, followed by the chosen profile.
func ResolvePolicyLayers(settings Settings, h Host, origin, profile string, session api.LayerValues) ([]api.Layer, error) {
	all := api.Layer{Kind: api.LayerAll, LayerValues: settings.LayerValues, Lock: policyLock(settings.Lock)}
	hostValues := h.LayerValues
	hostValues.Sandbox.Secrets = append(slices.Clone(h.Secrets), hostValues.Sandbox.Secrets...)
	host := api.Layer{Kind: api.LayerHost, Name: h.Name, LayerValues: hostValues, Lock: policyLock(h.Lock)}
	layers := []api.Layer{all, host}
	if origin != "" {
		normal, err := NormaliseOrigin(origin)
		if err != nil {
			return nil, err
		}
		var names []string
		for name := range settings.Repos {
			if name == normal || strings.HasSuffix(name, "/*") && strings.HasPrefix(normal, strings.TrimSuffix(name, "*")) {
				names = append(names, name)
			}
		}
		sort.Slice(names, func(i, j int) bool {
			if len(names[i]) == len(names[j]) {
				return names[i] < names[j]
			}
			return len(names[i]) < len(names[j])
		})
		for _, name := range names {
			layers = append(layers, api.Layer{Kind: api.LayerRepo, Name: name, LayerValues: repoLayerValues(settings.Repos[name])})
		}
	}
	if profile != "" {
		selected := profile
		session.Sandbox.Profile = &selected
	}
	preview := append(slices.Clone(layers), api.Layer{Kind: api.LayerSession, LayerValues: session})
	merged, _, err := egress.MergeLayers(preview)
	if err != nil {
		return nil, policyError(err)
	}
	if selected := merged.Sandbox.Profile; selected != nil {
		profile = *selected
	}
	if profile != "" {
		values, ok := settings.Profiles[profile]
		if !ok {
			return nil, &UserError{Msg: "no profile named " + fmt.Sprintf("%q", CleanLine(profile)), Hint: "Name a profile from config.toml's [profiles] table.", Code: "profile_not_found", Kind: KindNotFound}
		}
		if values.Sandbox.Profile != nil {
			return nil, &UserError{Msg: "a profile may not select another profile", Hint: brand.Name + " config check", Code: "layer_key", Kind: KindUsage}
		}
		layers = append(layers, api.Layer{Kind: api.LayerProfile, Name: profile, LayerValues: values})
	}
	layers = append(layers, api.Layer{Kind: api.LayerSession, LayerValues: session})
	_, clean, err := egress.MergeLayers(layers)
	if err != nil {
		return nil, policyError(err)
	}
	return clean, nil
}

func policyLock(values *api.LayerValues) *api.Lock {
	if values == nil {
		return nil
	}
	return &api.Lock{LayerValues: *values}
}

// RefreshPolicyLayers uses the exact repository and profile names saved at
// creation. Changed origins or newly matching sections do not rename a session.
func RefreshPolicyLayers(h Host, saved []api.Layer) ([]api.Layer, error) {
	settings, err := ReadPolicySettings()
	if err != nil {
		return nil, err
	}
	return refreshPolicyLayers(settings, h, saved)
}

func refreshPolicyLayers(settings Settings, h Host, saved []api.Layer) ([]api.Layer, error) {
	next := slices.Clone(saved)
	for i, l := range next {
		switch l.Kind {
		case api.LayerAll:
			next[i].LayerValues = settings.LayerValues
			next[i].Rules = nil
			next[i].Preset = ""
			next[i].Unknown = ""
			next[i].Lock = policyLock(settings.Lock)
		case api.LayerHost:
			next[i].LayerValues = h.LayerValues
			next[i].Sandbox.Secrets = append(slices.Clone(h.Secrets), next[i].Sandbox.Secrets...)
			next[i].Rules = nil
			next[i].Preset = ""
			next[i].Unknown = ""
			next[i].Lock = policyLock(h.Lock)
		case api.LayerRepo:
			if l.Source != api.SourceRepositoryFile {
				next[i] = api.Layer{Kind: l.Kind, Name: l.Name, LayerValues: repoLayerValues(settings.Repos[l.Name])}
			}
		case api.LayerProfile:
			values, ok := settings.Profiles[l.Name]
			if !ok {
				return nil, &UserError{Msg: "the session's profile " + CleanLine(l.Name) + " no longer exists", Hint: "Restore the profile in config.toml before resuming.", Code: "profile_not_found", Kind: KindNotFound}
			}
			next[i] = api.Layer{Kind: l.Kind, Name: l.Name, LayerValues: values}
		}
	}
	_, clean, err := egress.MergeLayers(next)
	if err != nil {
		return nil, policyError(err)
	}
	return clean, nil
}

// PolicyError gives canonical policy errors the client's kind and hint.
func PolicyError(err error) error { return policyError(err) }

func policyError(err error) error {
	var pe *proto.Error
	if errors.As(err, &pe) {
		return hostError(pe)
	}
	return err
}

// PolicyConfigPaths returns the files which persistent policy edits may write.
func PolicyConfigPaths() (string, string) {
	return filepath.Join(ConfigDir(), "config.toml"), filepath.Join(ConfigDir(), "hosts.toml")
}

func repoLayerValues(repo RepoSettings) api.LayerValues {
	values := repo.LayerValues
	values.Sandbox.Include = append(slices.Clone(repo.Include), values.Sandbox.Include...)
	return values
}
