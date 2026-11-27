//go:build linux

// Command sandboxprobe is the static guest fixture used by the real sandbox
// conformance suite. It changes only its terminal and token-owned test files.
package main

import (
	"bufio"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

var fixture = []byte("\x1b]8;;https://example.com\x1b\\link\x1b]8;;\x1b\\ \x1b[38;2;255;100;0mtruecolor\x1b[0m \x1b[4:3mcurly\x1b[0m \x1b]52;c;aGVsbG8=\x1b\\\x1b[?2026h\x1b[?2026l END")

func emit(kind string, value any) {
	data, err := json.Marshal(map[string]any{"event": kind, "value": value})
	if err != nil {
		fmt.Fprintln(os.Stderr, "probe: cannot encode its result. Try: inspect the conformance fixture.")
		os.Exit(1)
	}
	fmt.Println(string(data))
}

func main() {
	if len(os.Args) < 3 || os.Args[1] != "serve" || !validToken(os.Args[2]) {
		fmt.Fprintln(os.Stderr, "probe: needs serve and the run token. Try: use the E2E conformance runner.")
		os.Exit(2)
	}
	token := os.Args[2]
	state, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		fmt.Fprintln(os.Stderr, "probe: no real agent terminal. Try: attach through the client.")
		os.Exit(1)
	}
	defer func() { _ = term.Restore(int(os.Stdin.Fd()), state) }()
	emit("ready", map[string]any{"token": token, "uid": os.Getuid(), "pid": os.Getpid(), "pgid": unix.Getpgrp()})
	reader := bufio.NewReaderSize(os.Stdin, 4096)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		parts := strings.Fields(line)
		if len(parts) == 0 {
			continue
		}
		switch parts[0] {
		case "ping":
			if len(parts) == 2 && validToken(parts[1]) {
				fmt.Println("ACK " + parts[1])
			}
		case "fidelity":
			_, _ = os.Stdout.Write(fixture)
		case "echo":
			if len(parts) == 2 && len(parts[1]) <= 8192 {
				if data, err := hex.DecodeString(parts[1]); err == nil {
					_, _ = os.Stdout.Write(append(append([]byte("BYTES<"), data...), []byte(">BYTES")...))
				}
			}
		case "counter":
			for i := 1; i <= 400; i++ {
				fmt.Printf("N%04d\n", i)
				time.Sleep(20 * time.Millisecond)
			}
		case "memory":
			emit("memory", guestMemory())
		case "escape":
			emit("escape", escapeChecks(parts[1:]))
		case "compat":
			result, applicable := compatUnshare()
			emit("compat", map[string]any{"applicable": applicable, "denied": result != 0, "result": result})
		case "write":
			result := map[string]bool{}
			for _, root := range []string{"/work", "/tmp"} {
				name := filepath.Join(root, ".e2e-probe-"+token)
				err := os.WriteFile(name, []byte(token), 0600)
				result[root] = err == nil
			}
			emit("write", result)
		case "inspect":
			work, workErr := os.ReadFile("/work/.e2e-probe-" + token)
			_, tempErr := os.Stat("/tmp/.e2e-probe-" + token)
			emit("persist", map[string]any{"work": workErr == nil && string(work) == token, "temp": tempErr == nil})
		case "fill":
			emit("disk", fillDisk(token))
		default:
			emit("error", "unknown fixture command")
		}
	}
}

func validToken(value string) bool {
	if len(value) < 1 || len(value) > 128 {
		return false
	}
	for _, c := range value {
		if c < 'a' || c > 'z' {
			if c < '0' || c > '9' {
				return false
			}
		}
	}
	return true
}

func guestMemory() map[string]any {
	result := map[string]any{"claim": true}
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		result["error"] = err.Error()
		return result
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		parts := strings.Fields(line)
		if len(parts) == 3 && (parts[0] == "MemTotal:" || parts[0] == "MemAvailable:") {
			value, err := strconv.ParseInt(parts[1], 10, 64)
			if err == nil {
				result[strings.TrimSuffix(parts[0], ":")] = value * 1024
			}
		}
	}
	var agentRSS int64
	entries, err := os.ReadDir("/proc")
	if err != nil {
		result["error"] = err.Error()
		return result
	}
	for _, entry := range entries {
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		stat, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "stat"))
		if err != nil {
			continue
		}
		close := strings.LastIndexByte(string(stat), ')')
		if close < 0 {
			continue
		}
		fields := strings.Fields(string(stat[close+1:]))
		if len(fields) < 22 {
			continue
		}
		group, groupErr := strconv.Atoi(fields[2])
		rss, rssErr := strconv.ParseInt(fields[21], 10, 64)
		if groupErr == nil && rssErr == nil && group == unix.Getpgrp() {
			agentRSS += rss * int64(os.Getpagesize())
		}
	}
	result["agent_pgrp_rss"] = agentRSS
	return result
}

func fillDisk(token string) map[string]any {
	// This is a bounded disk test, not a memory stress test. Its only buffer
	// is 1 MiB and its total writes cannot exceed 512 MiB.
	name := "/work/.e2e-probe-fill-" + token
	file, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	buffer := make([]byte, 1<<20)
	var count int64
	for count < 512<<20 {
		n, writeErr := file.Write(buffer)
		count += int64(n)
		if writeErr != nil {
			err = writeErr
			break
		}
		if n != len(buffer) {
			err = io.ErrShortWrite
			break
		}
	}
	if err == nil {
		err = file.Sync()
	}
	err = errors.Join(err, file.Close())
	result := map[string]any{"bytes": count, "enospc": errors.Is(err, unix.ENOSPC)}
	if err != nil {
		result["error"] = err.Error()
	}
	if removeErr := os.Remove(name); removeErr != nil {
		result["cleanup_error"] = removeErr.Error()
	}
	return result
}
