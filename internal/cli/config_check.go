package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

// configCheckCmd checks config.toml and hosts.toml with go-toml/v2 strict
// decoding and value rules (docs/contributing/cli-spec.md 13).
func configCheckCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Check the configuration files for mistakes",
		Long: "Checks config.toml and hosts.toml for syntax errors, unknown keys, wrong types and invalid values.\n" +
			"Each problem is printed with its file, line and column, and unknown keys suggest the nearest known key.",
		Example: "  " + brand.Name + " config check\n" +
			"  " + brand.Name + " config check --json",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			rep := runConfigCheck()
			if asJSON {
				if err := printJSON(rep); err != nil {
					return err
				}
				if !rep.OK {
					return silentError{code: 1}
				}
				return nil
			}
			printConfigCheck(cmd, rep)
			if !rep.OK {
				return silentError{code: 1}
			}
			return nil
		},
	}
	addJSON(cmd, &asJSON)
	return cmd
}

type configReport struct {
	OK       bool            `json:"ok"`
	Files    []checkedFile   `json:"files"`
	Findings []configFinding `json:"findings"`
	Layers   []string        `json:"layers"`
}

type checkedFile struct {
	Path   string `json:"path"`
	Exists bool   `json:"exists"`
}

type configFinding struct {
	Path        string   `json:"path"`
	Line        int      `json:"line,omitempty"`
	Column      int      `json:"column,omitempty"`
	Severity    string   `json:"severity"`
	Code        string   `json:"code"`
	Message     string   `json:"message"`
	Suggestions []string `json:"suggestions,omitempty"`
}

// runConfigCheck checks config.toml and hosts.toml and builds the structured report.
func runConfigCheck() configReport {
	rep := configReport{
		OK:       true,
		Files:    make([]checkedFile, 0, 2),
		Findings: []configFinding{},
		Layers:   []string{},
	}

	sPath := client.SettingsPath()
	sFile, sFindings := checkSettingsFile(sPath)
	rep.Files = append(rep.Files, sFile)
	rep.Findings = append(rep.Findings, sFindings...)

	hPath := client.HostsPath()
	hFile, hFindings := checkHostsFile(hPath)
	rep.Files = append(rep.Files, hFile)
	rep.Findings = append(rep.Findings, hFindings...)

	for _, f := range rep.Findings {
		if f.Severity == "error" {
			rep.OK = false
			break
		}
	}
	return rep
}

func printConfigCheck(cmd *cobra.Command, rep configReport) {
	byFile := map[string][]configFinding{}
	var errCount, warnCount int
	var errFiles []string
	seenErrFile := map[string]bool{}
	for _, f := range rep.Findings {
		byFile[f.Path] = append(byFile[f.Path], f)
		if f.Severity == "warning" {
			warnCount++
		} else {
			errCount++
			if !seenErrFile[f.Path] {
				seenErrFile[f.Path] = true
				errFiles = append(errFiles, f.Path)
			}
		}
	}

	for _, cf := range rep.Files {
		if !cf.Exists {
			printf("%s: none yet\n", cf.Path)
			continue
		}
		fs := byFile[cf.Path]
		if len(fs) == 0 {
			printf("%s: no problems\n", cf.Path)
			continue
		}
		sortFindings(fs)
		for _, f := range fs {
			printf("%s\n", formatFinding(f))
		}
	}

	if errCount > 0 || warnCount > 0 {
		printf("%s\n", configSummary(errCount, warnCount, len(rep.Files)))
	}
	if errCount > 0 {
		say(cmd, "\nTry: %s\n", configCheckTryHint(rep.Findings, errFiles))
	}
}

func sortFindings(fs []configFinding) {
	sort.SliceStable(fs, func(i, j int) bool {
		if fs[i].Line != fs[j].Line {
			return fs[i].Line < fs[j].Line
		}
		if fs[i].Column != fs[j].Column {
			return fs[i].Column < fs[j].Column
		}
		if fs[i].Severity != fs[j].Severity {
			return fs[i].Severity < fs[j].Severity
		}
		return fs[i].Message < fs[j].Message
	})
}

func formatFinding(f configFinding) string {
	loc := f.Path
	switch {
	case f.Line > 0 && f.Column > 0:
		loc = fmt.Sprintf("%s:%d:%d", f.Path, f.Line, f.Column)
	case f.Line > 0:
		loc = fmt.Sprintf("%s:%d", f.Path, f.Line)
	}
	prefix := ""
	if f.Severity == "warning" {
		prefix = "warning: "
	}
	msg := client.CleanLine(f.Message)
	return fmt.Sprintf("%s: %s%s%s", loc, prefix, msg, formatSuggestions(f.Suggestions))
}

func formatSuggestions(sugg []string) string {
	switch len(sugg) {
	case 0:
		return ""
	case 1:
		return fmt.Sprintf(". Did you mean %q?", client.CleanLine(sugg[0]))
	default:
		q := make([]string, len(sugg))
		for i, s := range sugg {
			q[i] = fmt.Sprintf("%q", client.CleanLine(s))
		}
		return ". Did you mean one of these: " + strings.Join(q, ", ") + "?"
	}
}

func configSummary(errs, warns, files int) string {
	var parts []string
	if errs > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", errs, pluralWord(errs, "error", "errors")))
	}
	if warns > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", warns, pluralWord(warns, "warning", "warnings")))
	}
	return fmt.Sprintf("%s in %d %s.", strings.Join(parts, " and "), files, pluralWord(files, "file", "files"))
}

func configCheckTryHint(findings []configFinding, errFiles []string) string {
	if len(errFiles) == 1 {
		if len(findings) == 1 && findings[0].Code == "unknown_key" && len(findings[0].Suggestions) == 1 {
			return fmt.Sprintf("fix line %d of %s (try %q) and run %s config check", findings[0].Line, errFiles[0], findings[0].Suggestions[0], brand.Name)
		}
		return fmt.Sprintf("fix the errors in %s and run %s config check", errFiles[0], brand.Name)
	}
	return fmt.Sprintf("fix the errors above and run %s config check", brand.Name)
}

func pluralWord(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
