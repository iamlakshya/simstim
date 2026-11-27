package client

import "strings"

// ShellWord is w as a word that a shell reads back as w, without the
// control and hidden characters that a terminal would act on. It is quoted
// with single quotes when a shell would split it or read it as something
// else. A hint that repeats a word of the user's, such as an SSH target,
// uses it.
func ShellWord(w string) string {
	w = CleanPath(w)
	if w != "" && strings.Trim(w, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_./:=@%+,") == "" {
		return w
	}
	return ShellQuote(w)
}

// SSHCommand is the ssh command line that reaches target, as a hint shows
// it: the target quoted, after "--" when it starts with a dash, so that ssh
// cannot read it as an option.
func SSHCommand(target string) string {
	if strings.HasPrefix(target, "-") {
		return "ssh -- " + ShellWord(target)
	}
	return "ssh " + ShellWord(target)
}

// pathWord is a path as a word of a command: quoted, and after "./" when
// it starts with a dash, so that no program reads it as an option.
func pathWord(p string) string {
	if strings.HasPrefix(p, "-") {
		p = "./" + p
	}
	return ShellWord(p)
}
