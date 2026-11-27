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

//go:embed rules/mint.toml
var mintServiceData string

type serviceBarrier struct {
	Hosts         []string `toml:"hosts"`
	Methods       []string `toml:"methods"`
	Path          string   `toml:"path"`
	Kind          string   `toml:"kind"`
	Source        string   `toml:"source"`
	BoundOnly     bool     `toml:"bound_only"`
	PathPrefix    bool     `toml:"path_prefix"`
	ExceptMethods []string `toml:"except_methods"`
	classifier    bool
}

var loadServiceBarriers = sync.OnceValues(func() ([]serviceBarrier, error) {
	var rules []serviceBarrier
	for _, raw := range []string{githubServiceData, mintServiceData} {
		var data struct {
			Barriers []serviceBarrier `toml:"barriers"`
		}
		metadata, err := toml.Decode(raw, &data)
		if err != nil {
			return nil, fmt.Errorf("decode service barriers: %w", err)
		}
		if len(metadata.Undecoded()) != 0 || len(data.Barriers) == 0 {
			return nil, errors.New("invalid service barrier data")
		}
		rules = append(rules, data.Barriers...)
	}
	classifier, err := loadFixedClassifier()
	if err != nil {
		return nil, err
	}
	for _, rule := range classifier.Barriers {
		rule.classifier = true
		rules = append(rules, rule)
	}
	for _, rule := range rules {
		kinds := []string{"grant", "visibility", "ref_contents", "ref_create", "ref_update", "ref_delete", "ref_merge", "ref_pull_merge", "ref_pull_merge_async", "ref_upstream", "ref_rename", "graphql"}
		if !slices.Contains(kinds, rule.Kind) || len(rule.Hosts) == 0 || len(rule.Methods) == 0 && !rule.classifier || !validServiceSource(rule.Source) || rule.BoundOnly && rule.Kind != "grant" || !validServicePattern(rule.Path) || (rule.PathPrefix || len(rule.ExceptMethods) != 0) && !rule.classifier || rule.classifier && rule.Kind != "grant" {
			return nil, errors.New("invalid service barrier kind, path or source")
		}
		for _, host := range rule.Hosts {
			canonical, err := NormaliseHost(host)
			if err != nil || canonical != host || isNumericAddr(host) {
				return nil, errors.New("service barrier requires a canonical DNS host")
			}
		}
		for _, method := range append(slices.Clone(rule.Methods), rule.ExceptMethods...) {
			if !api.ValidHTTPMethod(method) || (method == "GET" || method == "HEAD") && rule.Kind != "graphql" && !rule.classifier {
				return nil, errors.New("service write barrier requires exact mutation methods")
			}
		}
	}
	return rules, nil
})

func validServiceSource(source string) bool {
	for _, prefix := range []string{"https://docs.github.com/", "https://developers.openai.com/", "https://docs.gitlab.com/", "https://securitylabs.datadoghq.com/", "https://socket.dev/"} {
		if strings.HasPrefix(source, prefix) {
			return true
		}
	}
	return false
}

func validServicePattern(pattern string) bool {
	if !strings.HasPrefix(pattern, "/") {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(pattern, "/"), "/")
	if strings.Count(pattern, "**") > 1 {
		return false
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || strings.ContainsAny(part, `\%?#`) {
			return false
		}
		if strings.Contains(part, "*") && part != "*" && part != "**" {
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
			suffix := want[i+1:]
			if len(got) < i+len(suffix) {
				return false
			}
			for j, label := range suffix {
				at := len(got) - len(suffix) + j
				if got[at] == "" || label != "*" && label != got[at] {
					return false
				}
			}
			return true
		}
		if i >= len(got) || got[i] == "" || part != "*" && part != got[i] {
			return false
		}
	}
	return len(want) == len(got)
}

func serviceBarriersFor(request Request) ([]serviceBarrier, error) {
	rules, err := loadServiceBarriers()
	if err != nil {
		return nil, err
	}
	var found []serviceBarrier
	for _, rule := range rules {
		method := slices.Contains(rule.Methods, request.Method) || rule.classifier && len(rule.Methods) == 0
		path := servicePathMatches(rule.Path, request.Path)
		if rule.PathPrefix {
			path = strings.HasPrefix(strings.ToLower(request.Path), strings.ToLower(rule.Path))
		}
		if slices.Contains(rule.Hosts, request.Host) && method && !slices.Contains(rule.ExceptMethods, request.Method) && path {
			found = append(found, rule)
		}
	}
	return found, nil
}

func serviceBarrierFor(request Request) (*serviceBarrier, error) {
	rules, err := serviceBarriersFor(request)
	if err != nil || len(rules) == 0 {
		return nil, err
	}
	return &rules[0], nil
}
