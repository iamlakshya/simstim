package guest

import (
	"embed"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/paths"
)

//go:embed pkgsettings.toml
var pkgSettingsFS embed.FS

// PackageSettingsConfig configures guest-side package manager settings for a
// sandbox start (isolation spec 7.10).
type PackageSettingsConfig struct {
	Level         string
	MinReleaseAge string
	Now           time.Time
}

// PackageSettingsPlan is the deterministic set of environment variables,
// guest-home config files, and stripped bypass variables for a start.
type PackageSettingsPlan struct {
	Level    string
	Days     int
	Env      map[string]string
	Files    map[string]string // relative path under guest $HOME -> file contents
	StripEnv []string
}

type pkgSettingsTable struct {
	Defaults struct {
		Level         string `toml:"level"`
		MinReleaseAge string `toml:"min_release_age"`
	} `toml:"defaults"`
	ProxyEnv []pkgProxyEntry `toml:"proxy_env"`
	Tools    []pkgToolEntry  `toml:"tools"`
}

type pkgProxyEntry struct {
	Tool   string `toml:"tool"`
	Key    string `toml:"key"`
	Value  string `toml:"value"`
	Source string `toml:"source"`
}

type pkgKV struct {
	Key   string `toml:"key"`
	Value string `toml:"value"`
}

type pkgToolEntry struct {
	Name         string   `toml:"name"`
	Source       string   `toml:"source"`
	Notes        string   `toml:"notes"`
	Env          []pkgKV  `toml:"env"`
	AgeEnv       []pkgKV  `toml:"age_env"`
	StrictEnv    []pkgKV  `toml:"strict_env"`
	StripEnv     []string `toml:"strip_env"`
	FilePath     string   `toml:"file_path"`
	FileLines    []string `toml:"file_lines"`
	FileAgeLines []string `toml:"file_age_lines"`
}

var (
	loadPkgTableOnce sync.Once
	loadedPkgTable   pkgSettingsTable
	errLoadPkgTable  error
)

func loadPackageSettingsTable() (pkgSettingsTable, error) {
	loadPkgTableOnce.Do(func() {
		raw, err := pkgSettingsFS.ReadFile("pkgsettings.toml")
		if err != nil {
			errLoadPkgTable = fmt.Errorf("guest: read pkgsettings.toml: %w", err)
			return
		}
		var tbl pkgSettingsTable
		md, err := toml.Decode(string(raw), &tbl)
		if err != nil {
			errLoadPkgTable = fmt.Errorf("guest: decode pkgsettings.toml: %w", err)
			return
		}
		if und := md.Undecoded(); len(und) > 0 {
			errLoadPkgTable = fmt.Errorf("guest: unknown keys in pkgsettings.toml: %v", und)
			return
		}
		for _, pe := range tbl.ProxyEnv {
			if pe.Key == "" || pe.Source == "" {
				errLoadPkgTable = fmt.Errorf("guest: pkgsettings.toml proxy_env entry %q missing key or source", pe.Tool)
				return
			}
		}
		for _, tool := range tbl.Tools {
			if tool.Name == "" || tool.Source == "" {
				errLoadPkgTable = fmt.Errorf("guest: pkgsettings.toml tool entry %q missing name or source", tool.Name)
				return
			}
			if tool.FilePath != "" {
				clean := filepath.ToSlash(filepath.Clean(tool.FilePath))
				if clean == "." || strings.HasPrefix(clean, "/") || clean == ".." || strings.HasPrefix(clean, "../") {
					errLoadPkgTable = fmt.Errorf("guest: pkgsettings.toml invalid file_path %q", tool.FilePath)
					return
				}
			}
		}
		loadedPkgTable = tbl
	})
	return loadedPkgTable, errLoadPkgTable
}

func packageProxyEnv(proxyURL string) map[string]string {
	if proxyURL == "" {
		return nil
	}
	tbl, err := loadPackageSettingsTable()
	if err != nil {
		return nil
	}
	out := make(map[string]string, len(tbl.ProxyEnv))
	for _, pe := range tbl.ProxyEnv {
		out[pe.Key] = strings.ReplaceAll(pe.Value, "{proxy_url}", proxyURL)
	}
	return out
}

// BuildPackageSettings computes the environment variables and home config files
// for cfg from pkgsettings.toml without touching the filesystem.
func BuildPackageSettings(cfg PackageSettingsConfig) (PackageSettingsPlan, error) {
	level, err := api.ParsePackageManagers(cfg.Level)
	if err != nil {
		return PackageSettingsPlan{}, err
	}
	days, _, err := api.ParseMinReleaseAge(cfg.MinReleaseAge)
	if err != nil {
		return PackageSettingsPlan{}, err
	}
	if level == api.PackageLevelOff {
		return PackageSettingsPlan{Level: level, Days: days}, nil
	}
	tbl, err := loadPackageSettingsTable()
	if err != nil {
		return PackageSettingsPlan{}, err
	}
	now := cfg.Now
	if now.IsZero() {
		now = time.Now()
	}
	cutoff := now.UTC().Truncate(time.Second).Add(-time.Duration(days) * 24 * time.Hour).Format(time.RFC3339)
	repl := strings.NewReplacer(
		"{days}", strconv.Itoa(days),
		"{minutes}", strconv.Itoa(days*1440),
		"{seconds}", strconv.Itoa(days*86400),
		"{cutoff_rfc3339}", cutoff,
	)

	plan := PackageSettingsPlan{
		Level: level,
		Days:  days,
		Env:   map[string]string{},
		Files: map[string]string{},
	}
	for _, tool := range tbl.Tools {
		applyToolEntry(&plan, tool, level, days, repl)
	}
	sort.Strings(plan.StripEnv)
	return plan, nil
}

func applyToolEntry(plan *PackageSettingsPlan, tool pkgToolEntry, level string, days int, repl *strings.Replacer) {
	for _, kv := range tool.Env {
		plan.Env[kv.Key] = repl.Replace(kv.Value)
	}
	if days > 0 {
		for _, kv := range tool.AgeEnv {
			plan.Env[kv.Key] = repl.Replace(kv.Value)
		}
	}
	if level == api.PackageLevelStrict {
		for _, kv := range tool.StrictEnv {
			plan.Env[kv.Key] = repl.Replace(kv.Value)
		}
	}
	for _, k := range tool.StripEnv {
		if !slices.Contains(plan.StripEnv, k) {
			plan.StripEnv = append(plan.StripEnv, k)
		}
	}
	if tool.FilePath != "" {
		if content := renderToolFile(tool, days, repl); content != "" {
			plan.Files[tool.FilePath] = content
		}
	}
}

func renderToolFile(tool pkgToolEntry, days int, repl *strings.Replacer) string {
	lines := make([]string, 0, len(tool.FileLines)+len(tool.FileAgeLines))
	for _, line := range tool.FileLines {
		lines = append(lines, repl.Replace(line))
	}
	if days > 0 {
		for _, line := range tool.FileAgeLines {
			lines = append(lines, repl.Replace(line))
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}

// ApplyPackageSettings writes any guest-home package manager config files under
// homeDir (when homeDir is non-empty and level is not "off") and returns the
// merged environment. Any key present in userEnv (from sandbox.env) replaces
// the value written by guest init.
func ApplyPackageSettings(homeDir string, baseEnv []string, userEnv map[string]string, cfg PackageSettingsConfig) ([]string, error) {
	plan, err := BuildPackageSettings(cfg)
	if err != nil {
		return nil, err
	}
	if homeDir != "" && len(plan.Files) > 0 {
		if err := writePackageFiles(homeDir, plan.Files); err != nil {
			return nil, err
		}
	}
	return mergePackageEnv(baseEnv, plan, userEnv), nil
}

func writePackageFiles(homeDir string, files map[string]string) error {
	cleanHome := filepath.Clean(homeDir)
	keys := make([]string, 0, len(files))
	for rel := range files {
		keys = append(keys, rel)
	}
	sort.Strings(keys)
	for _, rel := range keys {
		full := filepath.Join(cleanHome, filepath.FromSlash(rel))
		parent := filepath.Dir(full)
		if err := paths.EnsurePrivateDir(parent); err != nil {
			return fmt.Errorf("guest: create package config dir %s: %w. Try: check guest home permissions", parent, err)
		}
		root, err := os.OpenRoot(parent)
		if err != nil {
			return fmt.Errorf("guest: open package config dir %s: %w. Try: check guest home permissions", parent, err)
		}
		werr := root.WriteFile(filepath.Base(full), []byte(files[rel]), 0o600)
		_ = root.Close()
		if werr != nil {
			return fmt.Errorf("guest: write package config %s: %w. Try: check guest home permissions", rel, werr)
		}
	}
	return nil
}

func mergePackageEnv(baseEnv []string, plan PackageSettingsPlan, userEnv map[string]string) []string {
	merged := map[string]string{}
	for _, kv := range baseEnv {
		if k, v, ok := strings.Cut(kv, "="); ok && k != "" {
			merged[k] = v
		}
	}
	for _, k := range plan.StripEnv {
		delete(merged, k)
	}
	maps.Copy(merged, plan.Env)
	maps.Copy(merged, userEnv)

	keys := make([]string, 0, len(merged))
	for k := range merged {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+merged[k])
	}
	return out
}

func applyStartPackageSettings(a StartReq, baseEnv []string) ([]string, error) {
	homeDir := envLookup(baseEnv, "HOME")
	userEnv := filterProtectedUserEnv(parseEnvSlice(a.UserEnv))
	return ApplyPackageSettings(homeDir, baseEnv, userEnv, PackageSettingsConfig{
		Level:         a.PackageManagers,
		MinReleaseAge: a.MinReleaseAge,
	})
}

func filterProtectedUserEnv(env map[string]string) map[string]string {
	if len(env) == 0 {
		return nil
	}
	out := make(map[string]string, len(env))
	for k, v := range env {
		if k == "BROWSER" || strings.HasPrefix(k, brand.EnvPrefix) {
			continue
		}
		out[k] = v
	}
	return out
}

func envLookup(env []string, key string) string {
	prefix := key + "="
	val := ""
	for _, kv := range env {
		if rest, ok := strings.CutPrefix(kv, prefix); ok {
			val = rest
		}
	}
	return val
}

func parseEnvSlice(env []string) map[string]string {
	if len(env) == 0 {
		return nil
	}
	out := make(map[string]string, len(env))
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k != "" {
			out[k] = v
		}
	}
	return out
}
