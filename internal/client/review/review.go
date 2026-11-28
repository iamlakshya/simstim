// Package review flags files and changes from a session that can run code on
// the laptop or in CI, hold hidden Unicode characters, or exceed the scan cap
// (spec 6.10, 7.1 and 21.1).
package review

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"unicode"

	"github.com/BurntSushi/toml"

	"github.com/iamlakshya/simstim/internal/brand"
)

//go:embed flags.toml
var flagsTOML string

// MaxScanSize is the largest file that review reads for content checks (1 MiB).
// Larger changed files are flagged by name and size without reading them.
const MaxScanSize = 1 << 20

// emptyTreeHash is git's well-known SHA-1 of the empty tree, used when a full
// bundle has no common base in the laptop's repository.
const emptyTreeHash = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

type ruleGroup struct {
	Category string   `toml:"category"`
	Patterns []string `toml:"patterns"`
}

type flagsConfig struct {
	Rules []ruleGroup `toml:"rule"`
}

var loadedRules = func() []ruleGroup {
	var cfg flagsConfig
	if _, err := toml.Decode(flagsTOML, &cfg); err != nil {
		panic("review: invalid flags.toml: " + err.Error())
	}
	cfg.Rules = append(cfg.Rules, ruleGroup{
		Category: "this tool",
		Patterns: []string{"." + brand.Name + "/**"},
	})
	return cfg.Rules
}()

// Finding is one flagged file or issue found by review.
type Finding struct {
	Path     string // slash-separated relative path
	Category string // rule category, "hidden text", "large", or "gitmodules_cr"
	Detail   string // optional detail, such as the file's size
	HardStop bool   // true when --allow-flagged must not bypass it (CVE-2025-48384)
}

// Result is the summary of reviewing a copy or a git bundle diff.
type Result struct {
	Findings []Finding
}

// Flagged reports whether any finding was raised.
func (r Result) Flagged() bool { return len(r.Findings) > 0 }

// HardBlocked returns the first finding that cannot be bypassed, if any.
func (r Result) HardBlocked() (Finding, bool) {
	for _, f := range r.Findings {
		if f.HardStop {
			return f, true
		}
	}
	return Finding{}, false
}

// Summary formats the findings as a concise comma-separated list of paths.
func (r Result) Summary() string {
	seen := map[string]bool{}
	var parts []string
	for _, f := range r.Findings {
		label := f.Path
		if f.Detail != "" {
			label += " (" + f.Detail + ")"
		}
		if !seen[label] {
			seen[label] = true
			parts = append(parts, label)
		}
	}
	return strings.Join(parts, ", ")
}

// CheckPath reports whether relPath matches a flagged path pattern from
// flags.toml, returning its category. Matching is case-insensitive so
// case-insensitive file systems cannot slip past a rule.
func CheckPath(relPath string) (category string, ok bool) {
	p := normalizePath(relPath)
	if p == "" {
		return "", false
	}
	low := strings.ToLower(p)
	for _, grp := range loadedRules {
		for _, pat := range grp.Patterns {
			if matchPattern(strings.ToLower(pat), low) {
				return grp.Category, true
			}
		}
	}
	return "", false
}

func normalizePath(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	p = strings.TrimPrefix(p, "./")
	p = strings.TrimPrefix(p, "/")
	p = path.Clean(p)
	if p == "." || p == "/" {
		return ""
	}
	return p
}

// matchPattern matches a slash-separated path against a pattern that may end
// with "/**" (matching everything beneath a directory prefix) or be matched
// against the whole path or any trailing suffix of path segments (so nested
// .envrc or package.json also match).
func matchPattern(pattern, target string) bool {
	if prefix, ok := strings.CutSuffix(pattern, "/**"); ok {
		return target == prefix || strings.HasPrefix(target, prefix+"/") ||
			strings.Contains(target, "/"+prefix+"/")
	}
	if strings.Contains(pattern, "/") {
		return target == pattern || strings.HasSuffix(target, "/"+pattern)
	}
	// Single-segment filename rule matches at root or in any subfolder.
	return target == pattern || strings.HasSuffix(target, "/"+pattern)
}

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

// ReviewGitDiff reviews every file changed between baseRef (or the empty tree
// when baseRef is empty) and headRef in repoDir. It never checks out any file
// and never reads more than MaxScanSize bytes per file.
func ReviewGitDiff(ctx context.Context, repoDir, baseRef, headRef string) (Result, error) {
	if baseRef == "" {
		baseRef = emptyTreeHash
	}
	out, err := gitSafeRun(ctx, repoDir, "diff-tree", "-r", "--no-commit-id", "--name-status", "-z", baseRef, headRef)
	if err != nil {
		return Result{}, fmt.Errorf("list changed files: %w", err)
	}
	fields := bytes.Split(out, []byte{0})
	var res Result
	for i := 0; i+1 < len(fields); {
		status := string(fields[i])
		if status == "" {
			i++
			continue
		}
		var filePath string
		if strings.HasPrefix(status, "R") || strings.HasPrefix(status, "C") {
			if i+2 >= len(fields) {
				break
			}
			filePath = string(fields[i+2])
			i += 3
		} else {
			filePath = string(fields[i+1])
			i += 2
		}
		if status == "D" {
			continue
		}
		if err := reviewGitFile(ctx, repoDir, baseRef, headRef, filePath, &res); err != nil {
			return res, err
		}
	}
	return res, nil
}

func reviewGitFile(ctx context.Context, repoDir, baseRef, headRef, filePath string, res *Result) error {
	size, isBlob, err := gitBlobSize(ctx, repoDir, headRef, filePath)
	if err != nil {
		return err
	}
	if !isBlob {
		return nil
	}
	isGitmodules := strings.EqualFold(path.Base(filePath), ".gitmodules")
	if size > MaxScanSize {
		// Never bypass the CVE-2025-48384 carriage-return hard-stop on large
		// .gitmodules files: stream the whole blob to check for '\r'.
		if isGitmodules {
			hasCR, cerr := gitBlobContainsCR(ctx, repoDir, headRef, filePath)
			if cerr != nil {
				return fmt.Errorf("scan %s for carriage returns: %w", filePath, cerr)
			}
			if hasCR {
				res.Findings = append(res.Findings, Finding{
					Path:     filePath,
					Category: "gitmodules_cr",
					Detail:   "contains carriage return (CVE-2025-48384)",
					HardStop: true,
				})
			}
		}
		res.Findings = append(res.Findings, Finding{
			Path:     filePath,
			Category: "large",
			Detail:   formatSize(size),
		})
		return nil
	}
	data, err := gitSafeRun(ctx, repoDir, "cat-file", "blob", headRef+":"+filePath)
	if err != nil {
		return fmt.Errorf("read %s from bundle: %w", filePath, err)
	}
	if isGitmodules && CheckGitmodulesCR(data) {
		res.Findings = append(res.Findings, Finding{
			Path:     filePath,
			Category: "gitmodules_cr",
			Detail:   "contains carriage return (CVE-2025-48384)",
			HardStop: true,
		})
	}
	if cat, ok := CheckPath(filePath); ok {
		// For package.json, only flag when scripts changed or failed to parse.
		if strings.EqualFold(path.Base(filePath), "package.json") {
			var oldData []byte
			if baseRef != emptyTreeHash {
				oldData, _ = gitSafeRun(ctx, repoDir, "cat-file", "blob", baseRef+":"+filePath)
			}
			if CheckPackageJSONScripts(oldData, data) {
				res.Findings = append(res.Findings, Finding{Path: filePath, Category: cat})
			}
		} else {
			res.Findings = append(res.Findings, Finding{Path: filePath, Category: cat})
		}
	}
	if added, err := gitAddedLines(ctx, repoDir, baseRef, headRef, filePath); err == nil && CheckHiddenText(added) {
		res.Findings = append(res.Findings, Finding{
			Path:     filePath,
			Category: "hidden text",
			Detail:   "hidden Unicode characters in added lines",
		})
	}
	return nil
}

func gitBlobContainsCR(ctx context.Context, repoDir, ref, filePath string) (bool, error) {
	args := []string{
		"-c", "core.hooksPath=/dev/null",
		"-c", "core.fsmonitor=false",
		"cat-file", "blob", ref + ":" + filePath,
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repoDir
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return false, fmt.Errorf("open git cat-file stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return false, fmt.Errorf("start git cat-file: %w", err)
	}
	buf := make([]byte, 32<<10)
	found := false
	for {
		n, rerr := stdout.Read(buf)
		if n > 0 && bytes.IndexByte(buf[:n], '\r') >= 0 {
			found = true
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			_ = cmd.Wait()
			return false, fmt.Errorf("read git cat-file output: %w", rerr)
		}
	}
	if err := cmd.Wait(); err != nil {
		return false, fmt.Errorf("git cat-file failed: %w", err)
	}
	return found, nil
}

func gitBlobSize(ctx context.Context, repoDir, ref, filePath string) (size int64, isBlob bool, err error) {
	out, err := gitSafeRun(ctx, repoDir, "ls-tree", "-l", ref, "--", filePath)
	if err != nil {
		return 0, false, fmt.Errorf("inspect %s in bundle: %w", filePath, err)
	}
	line := strings.TrimSpace(string(out))
	if line == "" {
		return 0, false, nil
	}
	// Format: <mode> <type> <object> <size>\t<file>
	head, _, _ := strings.Cut(line, "\t")
	parts := strings.Fields(head)
	if len(parts) < 4 || parts[1] != "blob" {
		return 0, false, nil
	}
	if parts[3] == "-" {
		return 0, false, nil
	}
	n, err := strconv.ParseInt(parts[3], 10, 64)
	if err != nil {
		return 0, false, fmt.Errorf("parse size of %s: %w", filePath, err)
	}
	return n, true, nil
}

func gitAddedLines(ctx context.Context, repoDir, baseRef, headRef, filePath string) ([]byte, error) {
	out, err := gitSafeRun(ctx, repoDir, "diff", "--no-ext-diff", "-U0", baseRef, headRef, "--", filePath)
	if err != nil {
		return nil, err
	}
	var added bytes.Buffer
	for _, line := range bytes.Split(out, []byte{'\n'}) {
		if len(line) > 0 && line[0] == '+' && !bytes.HasPrefix(line, []byte("+++ ")) {
			added.Write(line[1:])
			added.WriteByte('\n')
		}
	}
	return added.Bytes(), nil
}

func gitSafeRun(ctx context.Context, repoDir string, args ...string) ([]byte, error) {
	full := append([]string{"-C", repoDir, "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false"}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("git %s: %s", args[0], msg)
	}
	return out, nil
}

func formatSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d bytes", n)
	}
}
