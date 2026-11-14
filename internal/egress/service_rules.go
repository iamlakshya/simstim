package egress

import (
	_ "embed"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"
	"github.com/iamlakshya/simstim/internal/api"
)

//go:embed rules/github.toml
var githubServiceData string

type serviceBarrier struct {
	Hosts   []string `toml:"hosts"`
	Methods []string `toml:"methods"`
	Path    string   `toml:"path"`
	Kind    string   `toml:"kind"`
	Source  string   `toml:"source"`
}

var loadServiceBarriers = sync.OnceValues(func() ([]serviceBarrier, error) {
	var data struct {
		Barriers []serviceBarrier `toml:"barriers"`
	}
	metadata, err := toml.Decode(githubServiceData, &data)
	if err != nil {
		return nil, fmt.Errorf("decode GitHub service barriers: %w", err)
	}
	if len(metadata.Undecoded()) != 0 || len(data.Barriers) == 0 {
		return nil, errors.New("invalid GitHub service barrier data")
	}
	for _, rule := range data.Barriers {
		if rule.Kind != "grant" && rule.Kind != "visibility" || len(rule.Hosts) == 0 || len(rule.Methods) == 0 || !strings.HasPrefix(rule.Source, "https://docs.github.com/") || !validServicePattern(rule.Path) {
			return nil, errors.New("invalid service barrier kind, path or source")
		}
		for _, host := range rule.Hosts {
			canonical, err := NormaliseHost(host)
			if err != nil || canonical != host || isNumericAddr(host) {
				return nil, errors.New("service barrier requires a canonical DNS host")
			}
		}
		for _, method := range rule.Methods {
			if !api.ValidHTTPMethod(method) || method == "GET" || method == "HEAD" {
				return nil, errors.New("service write barrier requires exact mutation methods")
			}
		}
	}
	return data.Barriers, nil
})

func validServicePattern(pattern string) bool {
	if !strings.HasPrefix(pattern, "/") {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(pattern, "/"), "/")
	for i, part := range parts {
		if part == "" || part == "." || part == ".." || strings.ContainsAny(part, `\%?#`) {
			return false
		}
		if strings.Contains(part, "*") && part != "*" && !(part == "**" && i == len(parts)-1) {
			return false
		}
	}
	return true
}

// Barriers conservatively cover a trailing slash and route case aliases.
// They never grant a path or change the canonical request sent upstream.
func servicePathMatches(pattern, path string) bool {
	want := strings.Split(strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(pattern, "/"), "/")), "/")
	got := strings.Split(strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(path, "/"), "/")), "/")
	for i, part := range want {
		if part == "**" {
			return true
		}
		if i >= len(got) || got[i] == "" || part != "*" && part != got[i] {
			return false
		}
	}
	return len(want) == len(got)
}

func serviceBarrierFor(request Request) (*serviceBarrier, error) {
	rules, err := loadServiceBarriers()
	if err != nil {
		return nil, err
	}
	for i := range rules {
		rule := &rules[i]
		if slices.Contains(rule.Hosts, request.Host) && slices.Contains(rule.Methods, request.Method) && servicePathMatches(rule.Path, request.Path) {
			return rule, nil
		}
	}
	return nil, nil
}
