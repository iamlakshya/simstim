package proto

import (
	_ "embed"
	"io/fs"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"
)

// PackageManagerPayloadMax bounds the complete private helper plan.
const PackageManagerPayloadMax = 4 << 10

//go:embed pkgsettings.toml
var packageSettingsData string

// PackageEnvironmentSetting is one derived default, not a user value source.
// CaseInsensitive describes a tool's documented environment-name spelling.
type PackageEnvironmentSetting struct {
	Name            string `toml:"name"`
	Value           string `toml:"value"`
	Mode            string `toml:"mode"`
	Age             bool   `toml:"age"`
	CaseInsensitive bool   `toml:"case_insensitive"`
}

type packageFileDefinition struct {
	Paths   []string `toml:"paths"`
	Base    string   `toml:"base"`
	AgeLine string   `toml:"age_line"`
}

type packageToolDefinition struct {
	Name    string                      `toml:"name"`
	Sources []string                    `toml:"sources"`
	Env     []PackageEnvironmentSetting `toml:"env"`
	Files   []packageFileDefinition     `toml:"file"`
}

// PackageHomeFile is derived solely from the embedded fixed home-relative
// data. No private start payload supplies a path or arbitrary file contents.
type PackageHomeFile struct{ Path, Content string }

func packageSettingsFailure(message string) error {
	return &Error{Code: "wall_setup", Message: message, Hint: "Start the sandbox again with matching host and guest package settings."}
}

func packageEnvironmentName(name string) bool {
	if name == "" || len(name) > 128 {
		return false
	}
	for index, character := range name {
		if !(character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z' || character == '_' || index > 0 && character >= '0' && character <= '9') {
			return false
		}
	}
	return true
}

var canonicalPackageSettings = sync.OnceValues(func() ([]packageToolDefinition, error) {
	var data struct {
		Version int                     `toml:"version"`
		Tools   []packageToolDefinition `toml:"tool"`
	}
	metadata, err := toml.Decode(packageSettingsData, &data)
	if err != nil || len(metadata.Undecoded()) != 0 || data.Version != 1 || len(data.Tools) == 0 || len(data.Tools) > 32 {
		return nil, packageSettingsFailure("the embedded package manager data is invalid")
	}
	tools, names, paths := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, tool := range data.Tools {
		if (!packageEnvironmentName(tool.Name) || tool.Name != strings.ToLower(tool.Name) || len(tool.Name) > 32) || tools[tool.Name] || len(tool.Sources) == 0 || len(tool.Env) > 32 || len(tool.Files) > 8 {
			return nil, packageSettingsFailure("the embedded package manager row is invalid")
		}
		tools[tool.Name] = true
		for _, source := range tool.Sources {
			if !strings.HasPrefix(source, "https://") || strings.ContainsAny(source, " \t\r\n\x00") {
				return nil, packageSettingsFailure("the package manager source link is invalid")
			}
		}
		for _, setting := range tool.Env {
			if !packageEnvironmentName(setting.Name) || names[setting.Name] || setting.Mode != "guarded" && setting.Mode != "strict" || strings.ContainsAny(setting.Value, "\x00\r\n") || len(setting.Value) > 1024 {
				return nil, packageSettingsFailure("the embedded package environment setting is invalid")
			}
			names[setting.Name] = true
		}
		for _, file := range tool.Files {
			if len(file.Paths) == 0 || len(file.Paths) > 8 || len(file.Base)+len(file.AgeLine) > 8192 {
				return nil, packageSettingsFailure("the embedded package settings file is invalid")
			}
			for _, path := range file.Paths {
				if !fs.ValidPath(path) || paths[path] || strings.ContainsAny(path, "\\\x00\r\n") || len(path) > 256 {
					return nil, packageSettingsFailure("the embedded package settings path is invalid")
				}
				paths[path] = true
			}
		}
	}
	return data.Tools, nil
})

// PackageManagerPaths returns owned fixed paths for the runtime's fresh-home
// capture/restore rules. It supplies no permission to delete a foreign file.
func PackageManagerPaths() ([]string, error) {
	data, err := canonicalPackageSettings()
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, tool := range data {
		for _, file := range tool.Files {
			paths = append(paths, file.Paths...)
		}
	}
	return paths, nil
}

func renderPackageTemplate(template string, days int, started time.Time) (string, error) {
	cutoff := started.Add(-time.Duration(days) * 24 * time.Hour).UTC().Format(time.RFC3339)
	result := strings.NewReplacer("{{days}}", strconv.Itoa(days), "{{minutes}}", strconv.Itoa(days*1440), "{{seconds}}", strconv.Itoa(days*86400), "{{cutoff}}", cutoff).Replace(template)
	if strings.Contains(result, "{{") || strings.Contains(result, "}}") || strings.ContainsRune(result, 0) {
		return "", packageSettingsFailure("the embedded package settings template is invalid")
	}
	return result, nil
}

// RenderPackageManagerSettings resolves only the one validated cold-start
// plan and shared data. A zero-day age omits every age variable and file line.
// Off and nil emit no defaults or files, without reading the embedded table.
func RenderPackageManagerSettings(input *PackageManagersV1) ([]PackageEnvironmentSetting, []PackageHomeFile, error) {
	if input == nil {
		return nil, nil, nil
	}
	if err := ValidatePackageManagersV1(input); err != nil {
		return nil, nil, err
	}
	if input.Mode == "off" {
		return nil, nil, nil
	}
	data, err := canonicalPackageSettings()
	if err != nil {
		return nil, nil, err
	}
	days, err := ParsePackageReleaseAge(input.MinReleaseAge)
	if err != nil {
		return nil, nil, err
	}
	started, err := time.Parse(time.RFC3339Nano, input.StartedAt)
	if err != nil {
		return nil, nil, packageSettingsFailure("the package settings start time is invalid")
	}
	var environment []PackageEnvironmentSetting
	var files []PackageHomeFile
	for _, tool := range data {
		for _, setting := range tool.Env {
			if setting.Mode == "strict" && input.Mode != "strict" || setting.Age && days == 0 {
				continue
			}
			setting.Value, err = renderPackageTemplate(setting.Value, days, started)
			if err != nil {
				return nil, nil, err
			}
			environment = append(environment, setting)
		}
		for _, file := range tool.Files {
			content := file.Base
			if days > 0 {
				content += file.AgeLine
			}
			content, err = renderPackageTemplate(content, days, started)
			if err != nil {
				return nil, nil, err
			}
			for _, path := range file.Paths {
				files = append(files, PackageHomeFile{Path: path, Content: content})
			}
		}
	}
	slices.SortFunc(files, func(a, b PackageHomeFile) int { return strings.Compare(a.Path, b.Path) })
	return environment, files, nil
}
