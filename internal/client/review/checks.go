package review

import (
	"bytes"
	"encoding/json"
	"sort"
	"unicode"
)

// CheckHiddenText reports whether data contains bidirectional controls,
// zero-width characters, or Unicode tag characters (spec 7.1).
func CheckHiddenText(data []byte) bool {
	for _, r := range string(data) {
		if isHiddenRune(r) {
			return true
		}
	}
	return false
}

func isHiddenRune(r rune) bool {
	if r == '\t' || r == '\n' || r == '\r' {
		return false
	}
	if r == 0x200B || r == 0x200C || r == 0x200D || r == 0xFEFF {
		return true
	}
	if r >= 0xE0000 && r <= 0xE007F {
		return true
	}
	return unicode.In(r, unicode.Bidi_Control, unicode.Cf, unicode.Other_Default_Ignorable_Code_Point)
}

// CheckGitmodulesCR reports whether .gitmodules content holds a carriage return
// (CVE-2025-48384), which must always be refused.
func CheckGitmodulesCR(data []byte) bool {
	return bytes.IndexByte(data, '\r') >= 0
}

// CheckPackageJSONScripts reports whether package.json changed its "scripts"
// section between oldData and newData (or defines scripts when oldData is nil).
func CheckPackageJSONScripts(oldData, newData []byte) bool {
	oldScripts := extractScripts(oldData)
	newScripts := extractScripts(newData)
	if len(newScripts) == 0 && len(oldScripts) == 0 {
		return false
	}
	return !bytes.Equal(oldScripts, newScripts)
}

func extractScripts(data []byte) []byte {
	if len(data) == 0 {
		return nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		// Unparseable package.json: fail closed and flag it.
		return data
	}
	raw, ok := obj["scripts"]
	if !ok {
		return nil
	}
	var canonical any
	if err := json.Unmarshal(raw, &canonical); err != nil {
		return raw
	}
	out, err := json.Marshal(canonical)
	if err != nil {
		return raw
	}
	return out
}

// checkAddedSecrets scans added lines for known secret values and session
// sentinels. Findings never include any secret value.
func checkAddedSecrets(filePath string, added []byte, opts Options, res *Result) {
	if len(added) == 0 {
		return
	}
	if opts.SentinelPrefix != "" && bytes.Contains(added, []byte(opts.SentinelPrefix)) {
		res.Findings = append(res.Findings, Finding{
			Path:     filePath,
			Category: "secrets",
			Detail:   "contains a session sentinel",
		})
	}
	if len(opts.Secrets) == 0 {
		return
	}
	names := make([]string, 0, len(opts.Secrets))
	for name := range opts.Secrets {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		val := opts.Secrets[name]
		if len(val) < 4 {
			continue
		}
		if bytes.Contains(added, []byte(val)) {
			res.Findings = append(res.Findings, Finding{
				Path:     filePath,
				Category: "secrets",
				Detail:   "contains secret " + name,
			})
		}
	}
}
