package jail

import (
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// hidden names whole host folders and devices that no bind may expose.
type hidden struct {
	homes    []string
	runtimes []string
	vhost    []string
	err      error
}

func hiddenPaths() hidden {
	home, _ := os.UserHomeDir()
	login := ""
	if u, err := user.LookupId(strconv.Itoa(os.Getuid())); err == nil {
		login = u.HomeDir
	}
	nodes, _ := filepath.Glob("/dev/vhost-*")
	return protectedPaths(home, login, os.Getenv("XDG_RUNTIME_DIR"), nodes)
}

func protectedPaths(home, login, runtime string, nodes []string) hidden {
	var h hidden
	add := func(dst *[]string, p string) {
		paths, err := protectedHops(p)
		*dst = append(*dst, paths...)
		if h.err == nil {
			h.err = err
		}
	}
	for _, p := range []string{home, login} {
		add(&h.homes, p)
	}
	add(&h.runtimes, runtime)
	for _, p := range nodes {
		add(&h.vhost, p)
	}
	return h
}

// protectedHops keeps each symlink entry and its eventual destination.
// Missing entries still protect their parents. Do not clean the input
// first: a link followed by .. must walk the target's parent.
func protectedHops(p string) ([]string, error) {
	if p == "" {
		return nil, nil
	}
	if !filepath.IsAbs(p) {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("resolve a relative protected path: %w", err)
		}
		p = cwd + "/" + p
	}
	current := "/"
	queue := strings.Split(p, "/")
	var paths []string
	links := 0
	for len(queue) > 0 {
		part := queue[0]
		queue = queue[1:]
		switch part {
		case "", ".":
			continue
		case "..":
			current = filepath.Dir(current)
			continue
		}
		candidate := filepath.Join(current, part)
		info, err := os.Lstat(candidate)
		if err != nil {
			if !errors.Is(err, syscall.ENOENT) {
				return paths, fmt.Errorf("inspect a protected path: %w", err)
			}
			paths = append(paths, candidate)
			current = candidate
			continue
		}
		if info.Mode()&os.ModeSymlink == 0 {
			if len(queue) > 0 && !info.IsDir() {
				return paths, &os.PathError{Op: "resolve protected path", Path: candidate, Err: syscall.ENOTDIR}
			}
			current = candidate
			continue
		}
		paths = append(paths, candidate)
		links++
		if links > 40 {
			return paths, &os.PathError{Op: "resolve protected path", Path: candidate, Err: syscall.ELOOP}
		}
		link, err := os.Readlink(candidate)
		if err != nil {
			return paths, fmt.Errorf("read a protected symbolic link: %w", err)
		}
		if link == "" {
			return paths, &os.PathError{Op: "resolve protected path", Path: candidate, Err: syscall.EINVAL}
		}
		if filepath.IsAbs(link) {
			current = "/"
		}
		queue = append(strings.Split(link, "/"), queue...)
	}
	return append(paths, current), nil
}

// forbidden says why a bind may not show the resolved path real, or "".
func forbidden(real string, h hidden) string {
	if h.err != nil {
		return "a host path whose protection could not be checked"
	}
	switch {
	case real == "/":
		return "the root"
	case beneath(real, "/proc"):
		return "inside /proc"
	case real == "/dev":
		return "the host's device folder"
	case strings.HasPrefix(real, "/dev/vhost-"):
		return "a host vhost device"
	}
	for _, home := range h.homes {
		if beneath(home, real) {
			return "the home folder, or holds it"
		}
	}
	for _, runtime := range h.runtimes {
		if beneath(runtime, real) {
			return "$XDG_RUNTIME_DIR, or holds it"
		}
	}
	for _, v := range h.vhost {
		if beneath(v, real) {
			return v + ", or holds it"
		}
	}
	return ""
}
