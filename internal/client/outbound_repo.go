package client

import (
	"strings"
	"unicode/utf8"
)

func badRepoError(msg string) error {
	return &UserError{
		Msg:  msg,
		Hint: "Use a repository origin such as github.com/org/repo or https://github.com/org/repo.git.",
		Code: "bad_repo",
		Kind: KindUsage,
	}
}

func badRepoPatternError(msg string) error {
	return &UserError{
		Msg:  msg,
		Hint: "Use a repository pattern such as github.com/org/repo or github.com/org/*.",
		Code: "bad_repo",
		Kind: KindUsage,
	}
}

func hasInvalidRepoRune(s string) bool {
	for _, r := range s {
		if r <= 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

func isASCIIAlnum(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9')
}

func allASCIIDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func validRepoHost(host string) bool {
	var state repoHostState
	for _, r := range host {
		if !state.add(r) {
			return false
		}
	}
	return state.valid()
}

func validRepoSegments(path string) bool {
	var state repoSegmentsState
	for _, r := range path {
		if !state.add(r) {
			return false
		}
	}
	return state.segmentValid()
}

func splitRepoOrigin(raw string) (host, repoPath string, err error) {
	if scheme, rest, hasScheme := strings.Cut(raw, "://"); hasScheme {
		switch strings.ToLower(scheme) {
		case "https", "http", "ssh", "git", "git+ssh":
		default:
			return "", "", badRepoError("unsupported repository URL scheme")
		}
		authority, pathPart, ok := strings.Cut(rest, "/")
		if !ok || authority == "" || pathPart == "" {
			return "", "", badRepoError("repository URL must include a host and path")
		}
		if strings.Count(authority, "@") > 1 {
			return "", "", badRepoError("repository URL authority is ambiguous")
		}
		if user, hostPart, hasUser := strings.Cut(authority, "@"); hasUser {
			if user == "" {
				return "", "", badRepoError("repository URL user is empty")
			}
			authority = hostPart
		}
		if strings.Contains(authority, ":") {
			return "", "", badRepoError("repository URL must not include a port")
		}
		return authority, pathPart, nil
	}

	colon := strings.IndexByte(raw, ':')
	slash := strings.IndexByte(raw, '/')
	if colon >= 0 {
		if slash >= 0 && slash < colon {
			return "", "", badRepoError("repository path must not contain ':'")
		}
		left, right := raw[:colon], raw[colon+1:]
		if strings.Count(left, "@") != 1 {
			return "", "", badRepoError("repository address with ':' must have the form user@host:path")
		}
		user, hostPart, _ := strings.Cut(left, "@")
		if user == "" || hostPart == "" || right == "" {
			return "", "", badRepoError("repository address must include user, host and path")
		}
		if strings.HasPrefix(right, "/") || strings.HasPrefix(right, ":") || strings.HasPrefix(right, "~") {
			return "", "", badRepoError("repository SCP path is ambiguous")
		}
		if firstSeg, _, hasMore := strings.Cut(right, "/"); hasMore && allASCIIDigits(firstSeg) {
			return "", "", badRepoError("repository SCP path resembles a port number")
		}
		return hostPart, right, nil
	}

	if strings.Contains(raw, "@") {
		return "", "", badRepoError("repository name without a scheme must not include '@'")
	}
	hostPart, pathPart, ok := strings.Cut(raw, "/")
	if !ok || hostPart == "" || pathPart == "" {
		return "", "", badRepoError("repository name must have the form host/path")
	}
	return hostPart, pathPart, nil
}

// NormalizeRepo canonicalises a Git remote URL or host/path according to
// isolation spec 18.2: it drops the scheme, any URL userinfo and a trailing
// .git, turns user@host:path into host/path, lower-cases the host and keeps
// the case of the path. It rejects wildcards, '..', queries, fragments,
// backslashes, ports and invalid or ambiguous URLs without echoing raw input
// or credentials in its error.
func NormalizeRepo(raw string) (string, error) {
	if !utf8.ValidString(raw) {
		return "", badRepoError("repository origin is not valid UTF-8")
	}
	if raw == "" {
		return "", badRepoError("repository name is empty")
	}
	if hasInvalidRepoRune(raw) {
		return "", badRepoError("repository URL contains whitespace or control characters")
	}
	if strings.Contains(raw, `\`) {
		return "", badRepoError("repository URL must use '/' separators")
	}
	if strings.ContainsAny(raw, "?#") {
		return "", badRepoError("repository URL must not include a query or fragment")
	}
	if strings.ContainsAny(raw, "*[]") {
		return "", badRepoError("repository name must not contain wildcards")
	}
	host, repoPath, err := splitRepoOrigin(raw)
	if err != nil {
		return "", err
	}
	host = strings.ToLower(host)
	if !validRepoHost(host) {
		return "", badRepoError("repository host name is invalid")
	}
	if !validRepoSegments(repoPath) {
		return "", badRepoError("repository path must not contain '..' or empty segments")
	}
	repoPath = strings.TrimSuffix(repoPath, ".git")
	if !validRepoSegments(repoPath) {
		return "", badRepoError("repository path must not contain '..' or empty segments")
	}
	return host + "/" + repoPath, nil
}

// ValidateRepoPattern checks a repository allowlist pattern from hosts.toml
// or [repos] (exact canonical repository or trailing /* prefix) without
// echoing raw credentials in its error.
func ValidateRepoPattern(pattern string) (prefix string, wildcard bool, err error) {
	if pattern == "" {
		return "", false, badRepoPatternError("repository pattern is empty")
	}
	if hasInvalidRepoRune(pattern) {
		return "", false, badRepoPatternError("repository pattern contains whitespace or control characters")
	}
	if strings.Contains(pattern, `\`) {
		return "", false, badRepoPatternError("repository pattern must use '/' separators")
	}
	if strings.ContainsAny(pattern, "?#[]") {
		return "", false, badRepoPatternError("repository pattern must not include queries, fragments or character classes")
	}
	if base, ok := strings.CutSuffix(pattern, "/*"); ok {
		if strings.Contains(base, "*") {
			return "", false, badRepoPatternError("repository pattern may use '*' only at the end after '/'")
		}
		if strings.HasSuffix(base, ".git") {
			return "", false, badRepoPatternError("repository wildcard pattern must not end with .git/*")
		}
		if !strings.Contains(base, "/") {
			if strings.ContainsAny(base, ":@") {
				return "", false, badRepoPatternError("repository host pattern is invalid")
			}
			host := strings.ToLower(base)
			if !validRepoHost(host) {
				return "", false, badRepoPatternError("repository host pattern is invalid")
			}
			return host, true, nil
		}
		normBase, err := NormalizeRepo(base)
		if err != nil {
			return "", false, badRepoPatternError("repository pattern prefix is invalid")
		}
		return normBase, true, nil
	}
	if strings.Contains(pattern, "*") {
		return "", false, badRepoPatternError("repository pattern may use '*' only at the end after '/'")
	}
	exact, err := NormalizeRepo(pattern)
	if err != nil {
		return "", false, badRepoPatternError("repository pattern is invalid")
	}
	return exact, false, nil
}

// MatchRepoPattern reports whether repo (canonicalised with NormalizeRepo)
// matches pattern on '/' boundaries (isolation spec 18.2). A pattern ending in
// /* matches its path and every descendant path below it, including nested
// groups, and never matches across a non-'/' character.
func MatchRepoPattern(pattern, repo string) bool {
	prefix, wildcard, err := ValidateRepoPattern(pattern)
	if err != nil {
		return false
	}
	normRepo, err := NormalizeRepo(repo)
	if err != nil {
		return false
	}
	return matchCanonicalRepoPattern(canonicalRepo(prefix), wildcard, canonicalRepo(normRepo))
}

// A canonicalRepo has already had its scheme, userinfo and ONE trailing
// .git removed. Do not feed it into raw origin normalisation again.
type canonicalRepo string

func matchCanonicalRepoPattern(prefix canonicalRepo, wildcard bool, repo canonicalRepo) bool {
	if !wildcard {
		return repo == prefix
	}
	return repo == prefix || strings.HasPrefix(string(repo), string(prefix)+"/")
}
