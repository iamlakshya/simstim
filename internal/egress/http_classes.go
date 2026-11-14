package egress

import (
	_ "embed"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"
)

//go:embed rules/intercept.toml
var interceptTOML string

//go:embed rules/splice.toml
var spliceTOML string

type serviceClass struct {
	Host   string `toml:"host"`
	Kind   string `toml:"kind"`
	Source string `toml:"source"`
	rule   compiledRule
}

type httpClassData struct {
	classes []serviceClass
	splice  []compiledRule
}

var loadHTTPClasses = sync.OnceValues(func() (httpClassData, error) {
	var data struct {
		Classes []serviceClass `toml:"classes"`
	}
	md, err := toml.Decode(interceptTOML, &data)
	if err != nil {
		return httpClassData{}, fmt.Errorf("decode intercepted service classes: %w", err)
	}
	if len(md.Undecoded()) != 0 {
		return httpClassData{}, errors.New("unknown intercepted service class keys")
	}
	for i := range data.Classes {
		entry := &data.Classes[i]
		if !strings.HasPrefix(entry.Source, "https://") || entry.Kind != "object" && entry.Kind != "model" {
			return httpClassData{}, errors.New("intercepted class needs a known kind and source")
		}
		entry.rule, err = compileServiceHost(entry.Host)
		if err != nil {
			return httpClassData{}, errors.New("intercepted class needs a bare host or wildcard")
		}
	}
	var presets presetsFile
	if _, err := toml.Decode(presetsTOML, &presets); err != nil {
		return httpClassData{}, fmt.Errorf("read service preset classes: %w", err)
	}
	for kind, entries := range map[string][]presetEntry{"registry": presets.Balanced.Registries, "code": presets.Balanced.CodeHosts} {
		for _, entry := range entries {
			rule, err := compileTarget(entry.Host, true)
			if err != nil {
				return httpClassData{}, fmt.Errorf("read preset class host: %w", err)
			}
			data.Classes = append(data.Classes, serviceClass{Host: entry.Host, Kind: kind, Source: entry.Source, rule: rule})
		}
	}
	var splice struct {
		Hosts []struct {
			Host   string `toml:"host"`
			Source string `toml:"source"`
		} `toml:"hosts"`
	}
	md, err = toml.Decode(spliceTOML, &splice)
	if err != nil {
		return httpClassData{}, fmt.Errorf("decode splice-only hosts: %w", err)
	}
	if len(md.Undecoded()) != 0 {
		return httpClassData{}, errors.New("unknown splice-only host keys")
	}
	var spliceRules []compiledRule
	for _, entry := range splice.Hosts {
		rule, parseErr := compileTarget(entry.Host, true)
		if parseErr != nil || rule.isNumeric || rule.path != "" || rule.port != 0 || !strings.HasPrefix(entry.Source, "https://") {
			return httpClassData{}, errors.New("splice-only host needs a bare host and source")
		}
		spliceRules = append(spliceRules, rule)
	}
	return httpClassData{classes: data.Classes, splice: spliceRules}, nil
})

func matchesServiceHost(rule compiledRule, host string) bool {
	if rule.wildcard {
		return len(host) > len(rule.host)+1 && strings.HasSuffix(host, "."+rule.host)
	}
	return rule.host == host
}

// A restricting service class must cover shared hosting suffixes, such as
// object storage. This is not an allow rule. User grants and splice exceptions
// still use the canonical target parser unchanged.
func compileServiceHost(raw string) (compiledRule, error) {
	base, wildcard := strings.CutPrefix(raw, "*.")
	host, err := NormaliseHost(base)
	if err != nil || host != base || isNumericAddr(host) {
		return compiledRule{}, errors.New("service class needs a canonical DNS host")
	}
	return compiledRule{host: host, wildcard: wildcard}, nil
}

func httpClass(host string) (kind string, spliceOnly bool, err error) {
	host, err = NormaliseHost(host)
	if err != nil {
		return "", false, err
	}
	data, err := loadHTTPClasses()
	if err != nil {
		return "", false, err
	}
	for _, rule := range data.splice {
		if matchesServiceHost(rule, host) {
			return "", true, nil
		}
	}
	for _, class := range data.classes {
		if matchesServiceHost(class.rule, host) {
			return class.Kind, false, nil
		}
	}
	return "", false, nil
}
