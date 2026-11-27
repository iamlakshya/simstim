package client

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// The private record must still describe a pinned, hardened command if it
// was damaged. Never turn a partial record into SSH's ambient defaults.
func gitSSHOptions(config gitSSHConfig, dir string) (map[string]string, error) {
	options := map[string]string{}
	seenConfig, seenLog := false, false
	for i := 0; i < len(config.Args); i += 2 {
		if i+1 >= len(config.Args) {
			return nil, badGitSSHConfig()
		}
		switch config.Args[i] {
		case "-F":
			if seenConfig || config.Args[i+1] != wrapperPath(Host{Name: config.Name}) {
				return nil, badGitSSHConfig()
			}
			seenConfig = true
		case "-E":
			if seenLog || config.Args[i+1] != filepath.Join(dir, "ssh.log") {
				return nil, badGitSSHConfig()
			}
			seenLog = true
		case "-o":
			key, value, ok := strings.Cut(config.Args[i+1], "=")
			if !ok || strings.ContainsAny(value, "\r\n\x00") {
				return nil, badGitSSHConfig()
			}
			key = strings.ToLower(key)
			if _, exists := options[key]; exists {
				return nil, badGitSSHConfig()
			}
			if !gitSSHOptionAllowed(key, value) {
				return nil, badGitSSHConfig()
			}
			options[key] = value
		default:
			return nil, badGitSSHConfig()
		}
	}
	if !seenConfig || !seenLog {
		return nil, badGitSSHConfig()
	}
	return options, nil
}

func validateGitSSHConfig(config gitSSHConfig, dir string) error {
	if !ValidHostName(config.Name) || config.Target == "" || strings.HasPrefix(config.Target, "-") || strings.ContainsAny(config.Target, "\r\n\x00") || config.Root == "" || strings.ContainsRune(config.Root, 0) {
		return badGitSSHConfig()
	}
	options, err := gitSSHOptions(config, dir)
	if err != nil {
		return err
	}
	for key, value := range map[string]string{
		"forwardagent": "no", "forwardx11": "no", "permitlocalcommand": "no", "gssapidelegatecredentials": "no",
		"stricthostkeychecking": "yes", "updatehostkeys": "no", "globalknownhostsfile": os.DevNull,
		"verifyhostkeydns": "no", "nohostauthenticationforlocalhost": "no", "checkhostip": "no",
		"fingerprinthash": "sha256", "loglevel": "ERROR", "batchmode": "yes", "clearallforwardings": "yes", "hostkeyalias": pinAlias(config.Name),
	} {
		if options[key] != value {
			return badGitSSHConfig()
		}
	}
	if !gitSSHPinPath(config.Name, options["userknownhostsfile"]) {
		return badGitSSHConfig()
	}
	return validateGitSSHControl(options, config.Name)
}

func validateGitSSHControl(options map[string]string, name string) error {
	if options["controlpath"] == "none" && options["controlmaster"] == "no" {
		return nil
	}
	if options["controlmaster"] != "auto" {
		return badGitSSHConfig()
	}
	path := options["controlpath"]
	if strings.HasPrefix(path, `"`) {
		var err error
		path, err = strconv.Unquote(path)
		if err != nil {
			return badGitSSHConfig()
		}
	}
	if filepath.Base(path) != controlName("") {
		return badGitSSHConfig()
	}
	epoch := strings.ReplaceAll(filepath.Dir(path), "%%", "%")
	base, token := filepath.Dir(epoch), filepath.Base(epoch)
	if !controlKeyRE.MatchString(token) || !privateDir(base) || !privateDir(epoch) {
		return badGitSSHConfig()
	}
	index := filepath.Join(base, "h", controlHostKey(name))
	if !privateDir(filepath.Dir(index)) || !privateDir(index) {
		return badGitSSHConfig()
	}
	entries, err := os.ReadDir(index)
	if err != nil {
		return badGitSSHConfig()
	}
	found := false
	for _, entry := range entries {
		if controlKeyRE.MatchString(entry.Name()) {
			if actual, ok := readControlMarker(filepath.Join(index, entry.Name())); ok && actual == token {
				found = true
				break
			}
		}
	}
	if !found {
		return badGitSSHConfig()
	}
	return nil
}

func badGitSSHConfig() error { return errors.New("the private Git SSH configuration is incomplete") }

func validateGitSSHInvocation(args []string) error {
	for len(args) > 0 && strings.HasPrefix(args[0], "-") && args[0] != "--" {
		switch args[0] {
		case "-4", "-6":
			args = args[1:]
		case "-o":
			if len(args) < 2 || args[1] != "SendEnv=GIT_PROTOCOL" {
				return badGitSSHConfig()
			}
			args = args[2:]
		case "-p":
			if len(args) < 2 {
				return badGitSSHConfig()
			}
			port, err := strconv.Atoi(args[1])
			if err != nil || port < 1 || port > 65535 {
				return badGitSSHConfig()
			}
			args = args[2:]
		case "-l":
			if len(args) < 2 || args[1] == "" || strings.ContainsAny(args[1], "\r\n\x00") {
				return badGitSSHConfig()
			}
			args = args[2:]
		default:
			return badGitSSHConfig()
		}
	}
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) != 2 || args[0] == "" || strings.HasPrefix(args[0], "-") || strings.ContainsAny(args[0], "\r\n\x00") || !gitUploadPackCommand(args[1]) {
		return badGitSSHConfig()
	}
	return nil
}

func gitSSHOptionAllowed(key, value string) bool {
	switch key {
	case "forwardagent", "forwardx11", "permitlocalcommand", "gssapidelegatecredentials", "stricthostkeychecking", "updatehostkeys", "globalknownhostsfile", "verifyhostkeydns", "nohostauthenticationforlocalhost", "checkhostip", "fingerprinthash", "loglevel", "batchmode", "clearallforwardings", "hostkeyalias", "userknownhostsfile", "controlmaster", "controlpath":
		return true
	case "knownhostscommand":
		return value == "none"
	case "nohostauthenticationforproxycommand":
		return value == "no"
	case "controlpersist":
		return value == "600"
	default:
		return false
	}
}

func gitSSHPinPath(name, value string) bool {
	if value == sshPathArg(pinPath(name), runtime.GOOS) {
		return true
	}
	if strings.HasPrefix(value, `"`) {
		var err error
		value, err = strconv.Unquote(value)
		if err != nil {
			return false
		}
	}
	value = filepath.ToSlash(value)
	expected := filepath.ToSlash(pinPath(name))
	prefix := ".known_hosts-" + HostFileKey(name) + "-staged-"
	return filepath.Dir(value) == filepath.Dir(expected) && strings.HasPrefix(filepath.Base(value), prefix) && len(filepath.Base(value)) > len(prefix)
}

func gitUploadPackCommand(command string) bool {
	raw, ok := strings.CutPrefix(command, "git-upload-pack ")
	if !ok || len(raw) < 2 || raw[0] != 39 || raw[len(raw)-1] != 39 {
		return false
	}
	decoded := strings.ReplaceAll(raw[1:len(raw)-1], `'\''`, "'")
	return "'"+strings.ReplaceAll(decoded, "'", `'\''`)+"'" == raw
}

// Git may apply a named remote or URL rewrite. The producer, not Git's
// repository configuration, chooses the SSH endpoint and repository.
func gitSSHInvocationMatches(config gitSSHConfig, args []string) bool {
	for len(args) > 0 && strings.HasPrefix(args[0], "-") && args[0] != "--" {
		switch args[0] {
		case "-4", "-6":
			args = args[1:]
		case "-o":
			if len(args) < 2 || args[1] != "SendEnv=GIT_PROTOCOL" {
				return false
			}
			args = args[2:]
		default:
			return false
		}
	}
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) != 2 {
		return false
	}
	target := config.Target
	at := strings.LastIndexByte(target, '@') + 1
	if strings.HasPrefix(target[at:], "[") && strings.HasSuffix(target, "]") && strings.Contains(target[at:], ":") {
		target = target[:at] + strings.TrimSuffix(strings.TrimPrefix(target[at:], "["), "]")
	}
	command := "git-upload-pack '" + strings.ReplaceAll(config.Root, "'", `'\''`) + "'"
	return args[0] == target && args[1] == command
}
