package client

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const routeConfigFileLimitV1 = 1 << 20
const routeConfigTotalLimitV1 = 4 << 20
const routeConfigCountLimitV1 = 128

type routeConfigFileV1 struct {
	path string
	file *os.File
	info os.FileInfo
	data []byte
}
type routeConfigDirectoryV1 struct {
	path string
	root *os.Root
	info os.FileInfo
}
type routeIncludeGlobV1 struct {
	pattern string
	matches []string
}

// explicitRouteReadsetV1 is an original native read restriction, not route,
// registration or lifecycle permission. No public constructor accepts a DTO.
type explicitRouteReadsetV1 struct {
	files       []*routeConfigFileV1
	globs       []routeIncludeGlobV1
	total       int
	closed      bool
	missing     []string
	roots       []string
	directories []*routeConfigDirectoryV1
}

func routeSourceErrorV1(message string) error {
	return &UserError{Msg: message, Hint: "Use an existing regular SSH config with Host/Include/ProxyJump entries and no executable directives; keep its original files unchanged while adding the host.", Code: "joined_route_source", Kind: KindRefused}
}

// captureExplicitRouteReadsetV1 validates the entire supported closure BEFORE
// any OpenSSH resolver may run. Match exec cannot be disabled by LocalCommand.
func captureExplicitRouteReadsetV1(ctx context.Context, explicit string) (result *explicitRouteReadsetV1, failure error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if explicit == "" {
		return nil, routeSourceErrorV1("the route has no explicit original SSH config selection")
	}
	path, err := filepath.Abs(expand(explicit))
	if err != nil {
		return nil, err
	}
	source := &explicitRouteReadsetV1{}
	keep := false
	defer func() {
		if !keep {
			failure = errors.Join(failure, source.close())
		}
	}()
	source.roots = []string{path}
	for _, ambient := range []string{filepath.Join(home(), ".ssh", "config"), systemConfig()} {
		if _, err := os.Lstat(ambient); err == nil {
			source.roots = append(source.roots, ambient)
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		} else {
			source.missing = append(source.missing, ambient)
		}
	}
	visiting := map[string]bool{}
	for _, root := range source.roots {
		if err = source.captureFile(ctx, root, 0, visiting); err != nil {
			return nil, err
		}
	}
	if err = source.check(ctx); err != nil {
		return nil, err
	}
	keep = true
	return source, nil
}
func (s *explicitRouteReadsetV1) captureFile(ctx context.Context, path string, depth int, visiting map[string]bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if depth > 16 || len(s.files) >= routeConfigCountLimitV1 {
		return routeSourceErrorV1("the explicit SSH Include closure exceeds its bound")
	}
	path = filepath.Clean(path)
	if err := s.captureDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	if visiting[path] {
		return routeSourceErrorV1("the explicit SSH Include closure has a cycle")
	}
	for _, file := range s.files {
		if file.path == path {
			return nil
		}
	}
	before, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !before.Mode().IsRegular() || before.Size() > routeConfigFileLimitV1 {
		return routeSourceErrorV1("an SSH config source is linked, non-regular or too large")
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	opened, err := file.Stat()
	if err != nil {
		file.Close()
		return err
	}
	if !os.SameFile(before, opened) {
		file.Close()
		return routeSourceErrorV1("an SSH config source changed while opening")
	}
	if err = routeConfigNativeSafeV1(file, opened); err != nil {
		file.Close()
		return err
	}
	data, err := io.ReadAll(io.LimitReader(file, routeConfigFileLimitV1+1))
	if err != nil {
		file.Close()
		return err
	}
	if len(data) > routeConfigFileLimitV1 || s.total+len(data) > routeConfigTotalLimitV1 {
		file.Close()
		return routeSourceErrorV1("the explicit SSH config sources exceed their byte bound")
	}
	original := &routeConfigFileV1{path, file, opened, data}
	s.files = append(s.files, original)
	s.total += len(data)
	visiting[path] = true
	defer delete(visiting, path)
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 4096), routeConfigFileLimitV1)
	for scanner.Scan() {
		key, args, err := routeConfigLineV1(scanner.Text())
		if err != nil {
			return err
		}
		if key == "" {
			continue
		}
		if err = checkRouteConfigDirectiveV1(key, args); err != nil {
			return err
		}
		if key != "include" {
			continue
		}
		for _, pattern := range args {
			if strings.ContainsAny(pattern, "%$\x00\r\n") {
				return routeSourceErrorV1("an Include has unsupported dynamic expansion")
			}
			if strings.HasPrefix(pattern, "~/") {
				pattern = filepath.Join(home(), pattern[2:])
			} else if !filepath.IsAbs(pattern) {
				pattern = filepath.Join(home(), ".ssh", pattern)
			}
			pattern = filepath.Clean(pattern)
			prefix := pattern
			if wildcard := strings.IndexAny(prefix, "*?["); wildcard >= 0 {
				prefix = prefix[:wildcard]
			}
			directory := filepath.Dir(prefix)
			for {
				if _, err := os.Lstat(directory); err == nil {
					break
				} else if !errors.Is(err, os.ErrNotExist) {
					return err
				}
				parent := filepath.Dir(directory)
				if parent == directory {
					return routeSourceErrorV1("the Include has no stable parent directory")
				}
				directory = parent
			}
			if err = s.captureDirectory(directory); err != nil {
				return err
			}
			matches, err := filepath.Glob(pattern)
			if err != nil {
				return routeSourceErrorV1("an SSH Include pattern is malformed")
			}
			slices.Sort(matches)
			if len(s.globs) >= 256 {
				return routeSourceErrorV1("the explicit Include pattern count exceeds its bound")
			}
			s.globs = append(s.globs, routeIncludeGlobV1{pattern, slices.Clone(matches)})
			for _, included := range matches {
				if err = s.captureFile(ctx, included, depth+1, visiting); err != nil {
					return err
				}
			}
		}
	}
	if err = scanner.Err(); err != nil {
		return err
	}
	return original.check()
}
func (f *routeConfigFileV1) check() error {
	current, err := os.Lstat(f.path)
	if err != nil {
		return err
	}
	opened, err := f.file.Stat()
	if err != nil {
		return err
	}
	if !current.Mode().IsRegular() || !os.SameFile(current, f.info) || !os.SameFile(opened, f.info) {
		return routeSourceErrorV1("an original SSH config source was replaced")
	}
	if err = routeConfigNativeSafeV1(f.file, opened); err != nil {
		return err
	}
	data, err := io.ReadAll(io.NewSectionReader(f.file, 0, routeConfigFileLimitV1+1))
	if err != nil {
		return err
	}
	if !bytes.Equal(data, f.data) {
		return routeSourceErrorV1("an original SSH config source was edited")
	}
	return nil
}
func (s *explicitRouteReadsetV1) check(ctx context.Context) error {
	if s == nil || s.closed {
		return routeSourceErrorV1("the original explicit SSH read set ended")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, path := range s.missing {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			return routeSourceErrorV1("a previously absent ambient SSH source appeared")
		}
	}
	for _, directory := range s.directories {
		current, err := os.Lstat(directory.path)
		if err != nil {
			return err
		}
		file, err := directory.root.Open(".")
		if err != nil {
			return err
		}
		opened, err := file.Stat()
		if err == nil {
			err = routeDirectoryNativeSafeV1(file, opened)
		}
		err = errors.Join(err, file.Close())
		if err != nil {
			return err
		}
		if !current.IsDir() || !os.SameFile(current, directory.info) || !os.SameFile(opened, directory.info) {
			return routeSourceErrorV1("an original SSH source directory was replaced")
		}
	}
	for _, file := range s.files {
		if err := file.check(); err != nil {
			return err
		}
	}
	for _, glob := range s.globs {
		matches, err := filepath.Glob(glob.pattern)
		if err != nil {
			return err
		}
		slices.Sort(matches)
		if !slices.Equal(matches, glob.matches) {
			return routeSourceErrorV1("the original SSH Include glob membership changed")
		}
	}
	return ctx.Err()
}
func (s *explicitRouteReadsetV1) close() error {
	if s == nil || s.closed {
		return nil
	}
	var failure error
	for _, file := range s.files {
		failure = errors.Join(failure, file.file.Close())
	}
	for _, directory := range s.directories {
		failure = errors.Join(failure, directory.root.Close())
	}
	if failure == nil {
		s.closed = true
	}
	return failure
}

// routeConfigLineV1 recognises only complete ordinary keyword/argument syntax.
// It makes no Host matching decision; native OpenSSH retains that job.
func routeConfigLineV1(line string) (string, []string, error) {
	line = strings.TrimSpace(line)
	if line == "" || line[0] == '#' {
		return "", nil, nil
	}
	cut := strings.IndexAny(line, " \t=")
	if cut < 1 {
		return "", nil, routeSourceErrorV1("an SSH config line has no complete keyword and value")
	}
	key := strings.ToLower(line[:cut])
	rest := strings.TrimLeft(line[cut:], " \t=")
	var args []string
	var word strings.Builder
	quoted, escaped, active := false, false, false
	for _, r := range rest {
		if escaped {
			word.WriteRune(r)
			escaped = false
			active = true
			continue
		}
		if r == '\\' {
			escaped = true
			active = true
			continue
		}
		if r == '"' {
			quoted = !quoted
			active = true
			continue
		}
		if r == '\'' {
			return "", nil, routeSourceErrorV1("single-quoted SSH syntax is outside the supported explicit route subset")
		}
		if r == '#' && !quoted {
			break
		}
		if (r == ' ' || r == '\t') && !quoted {
			if active {
				args = append(args, word.String())
				word.Reset()
				active = false
			}
			continue
		}
		if r == '\x00' || r == '\r' || r == '\n' {
			return "", nil, routeSourceErrorV1("an SSH config line contains a control character")
		}
		word.WriteRune(r)
		active = true
	}
	if quoted || escaped {
		return "", nil, routeSourceErrorV1("an SSH config line has an incomplete quote or escape")
	}
	if active {
		args = append(args, word.String())
	}
	if len(args) == 0 {
		return "", nil, routeSourceErrorV1("an SSH config directive has no value")
	}
	return key, args, nil
}
func checkRouteConfigDirectiveV1(key string, args []string) error {
	switch key {
	case "match":
		return routeSourceErrorV1("Match directives are refused before explicit-route resolution; use equivalent Host entries for this route")
	case "proxycommand", "knownhostscommand", "localcommand":
		if len(args) == 1 && strings.EqualFold(args[0], "none") {
			return nil
		}
		return routeSourceErrorV1("executable SSH directives are refused before explicit-route resolution")
	case "ignoreunknown", "usekeychain", "hashknownhosts", "visualhostkey", "updatehostkeys", "addkeystoagent", "host", "include", "hostname", "user", "port", "proxyjump", "identityfile", "identityagent", "identitiesonly", "certificatefile", "userknownhostsfile", "globalknownhostsfile", "hostkeyalias", "hostkeyalgorithms", "pubkeyacceptedalgorithms", "pubkeyacceptedkeytypes", "stricthostkeychecking", "checkhostip", "verifyhostkeydns", "fingerprinthash", "canonicalizehostname", "batchmode", "connecttimeout", "connectionattempts", "serveraliveinterval", "serveralivecountmax", "tcpkeepalive", "compression", "addressfamily", "loglevel", "forwardagent", "forwardx11", "forwardx11trusted", "permitlocalcommand", "clearallforwardings", "controlmaster", "controlpath", "controlpersist", "sendenv", "gssapiauthentication", "gssapidelegatecredentials", "preferredauthentications", "passwordauthentication", "kbdinteractiveauthentication", "pubkeyauthentication", "numberofpasswordprompts":
		return nil
	default:
		return routeSourceErrorV1(fmt.Sprintf("SSH directive %q is outside the supported explicit ProxyJump source", key))
	}
}

func (s *explicitRouteReadsetV1) captureDirectory(path string) error {
	for _, directory := range s.directories {
		if directory.path == path {
			return nil
		}
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return routeSourceErrorV1("an SSH source directory is linked or non-regular")
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return err
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		root.Close()
		return routeSourceErrorV1("an SSH source directory changed while opening")
	}
	file, err := root.Open(".")
	if err != nil {
		root.Close()
		return err
	}
	err = routeDirectoryNativeSafeV1(file, opened)
	err = errors.Join(err, file.Close())
	if err != nil {
		root.Close()
		return err
	}
	s.directories = append(s.directories, &routeConfigDirectoryV1{path, root, opened})
	return nil
}
