package image

import (
	_ "embed"
	"path"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"
)

//go:embed review.toml
var savedReviewData string

type savedReviewRule struct {
	Reason   string   `toml:"reason"`
	Paths    []string `toml:"paths"`
	Check    string   `toml:"check"`
	Source   string   `toml:"source"`
	patterns []*regexp.Regexp
}

var savedReviewRules = sync.OnceValues(func() ([]savedReviewRule, error) {
	var data struct {
		Version int               `toml:"version"`
		Rules   []savedReviewRule `toml:"rule"`
	}
	metadata, err := toml.Decode(savedReviewData, &data)
	if err != nil || len(metadata.Undecoded()) != 0 || data.Version != 1 || len(data.Rules) == 0 {
		return nil, saveFailure("image_bad_layer", "the embedded image review data is invalid")
	}
	for index := range data.Rules {
		rule := &data.Rules[index]
		if rule.Reason == "" || !strings.HasPrefix(rule.Source, "https://") || strings.ContainsAny(rule.Source, " \t\n\r\x00") || rule.Check != "" && rule.Check != "image_path" && rule.Check != "privileged_mode" || (len(rule.Paths) == 0) == (rule.Check == "") {
			return nil, saveFailure("image_bad_layer", "an embedded image review rule is invalid")
		}
		for _, pattern := range rule.Paths {
			if !strings.HasPrefix(pattern, "/") || strings.ContainsAny(pattern, "\\\r\n\x00") {
				return nil, saveFailure("image_bad_layer", "an image review path is invalid")
			}
			expression := regexp.QuoteMeta(pattern)
			expression = strings.ReplaceAll(expression, `\*\*`, "\x00")
			expression = strings.ReplaceAll(expression, `\*`, "[^/]*")
			expression = strings.ReplaceAll(expression, "\x00", ".*")
			compiled, err := regexp.Compile("^(?:" + expression + ")$")
			if err != nil {
				return nil, err
			}
			rule.patterns = append(rule.patterns, compiled)
		}
	}
	return data.Rules, nil
})

func (filter *savedRootFilter) review(changes []string, modes map[string]int64) ([]SaveFinding, error) {
	rules, err := savedReviewRules()
	if err != nil {
		return nil, err
	}
	var findings []SaveFinding
	for _, name := range changes {
		if filter.exclude(name) != "" {
			continue
		}
		for _, rule := range rules {
			matched := false
			for _, pattern := range rule.patterns {
				if pattern.MatchString(name) {
					matched = true
					break
				}
			}
			switch rule.Check {
			case "image_path":
				for _, directory := range filter.paths {
					if savedBelow(name, directory) && name != directory {
						matched = true
						break
					}
				}
			case "privileged_mode":
				matched = modes[name]&0o6000 != 0
			}
			if matched {
				findings = append(findings, SaveFinding{Path: path.Clean(name), Reason: rule.Reason})
			}
		}
	}
	return squashSaveFindings(findings), nil
}

func squashSaveFindings(findings []SaveFinding) []SaveFinding {
	sortSaveFindings(findings)
	findings = slices.Compact(findings)
	output := findings[:0]
	for _, finding := range findings {
		if len(output) != 0 && output[len(output)-1].Path == finding.Path {
			output[len(output)-1].Reason += "; " + finding.Reason
		} else {
			output = append(output, finding)
		}
	}
	return output
}
