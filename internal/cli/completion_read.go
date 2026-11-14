package cli

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

// Bounded local file readers and untrusted-string sanitizers for shell
// completion (cli-spec 14.3). Nothing here opens a network connection, takes
// a lock, writes a file, or lets terminal escapes, tabs or newlines reach
// Cobra's completion output.

const (
	maxCompletionBytes = 4 << 20
	compDescMax        = 200
)

func completeConfigMapKeys(section, toComplete string) []string {
	b, ok := readCompletionFile(filepath.Join(client.ConfigDir(), "config.toml"))
	if !ok {
		return nil
	}
	var raw map[string]any
	if _, err := toml.Decode(string(b), &raw); err != nil {
		return nil
	}
	m, ok := raw[section].(map[string]any)
	if !ok {
		return nil
	}
	var keys []string
	for k := range m {
		if ck := strings.TrimSpace(cleanCompValue(k)); ck != "" && ck == k && strings.HasPrefix(ck, toComplete) {
			keys = append(keys, ck)
		}
	}
	slices.Sort(keys)
	return keys
}

func loadCompletionHosts() ([]client.Host, bool) {
	b, ok := readCompletionFile(filepath.Join(client.ConfigDir(), "hosts.toml"))
	if !ok {
		return nil, false
	}
	var f struct {
		Hosts []client.Host `toml:"host"`
	}
	if _, err := toml.Decode(string(b), &f); err != nil {
		return nil, false
	}
	var out []client.Host
	for _, h := range f.Hosts {
		if client.ValidHostName(h.Name) && cleanCompValue(h.Name) == h.Name {
			out = append(out, h)
		}
	}
	slices.SortFunc(out, func(a, b client.Host) int { return strings.Compare(a.Name, b.Name) })
	return out, true
}

func loadCompletionCache() (client.SessionCache, bool) {
	b, ok := readCompletionFile(filepath.Join(client.StateDir(), "launcher.json"))
	if !ok {
		return client.SessionCache{}, false
	}
	var c client.SessionCache
	if json.Unmarshal(b, &c) != nil {
		return client.SessionCache{}, false
	}
	return c, true
}

func readCompletionFile(path string) ([]byte, bool) {
	fi, err := os.Stat(path)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > maxCompletionBytes {
		return nil, false
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	buf := make([]byte, 0, min(int(fi.Size())+1, maxCompletionBytes+1))
	var chunk [4096]byte
	for {
		n, rerr := f.Read(chunk[:])
		if n > 0 {
			if len(buf)+n > maxCompletionBytes {
				return nil, false
			}
			buf = append(buf, chunk[:n]...)
		}
		if errors.Is(rerr, io.EOF) {
			return buf, true
		}
		if rerr != nil {
			return nil, false
		}
	}
}

func compItem(val, desc string) string {
	if desc == "" {
		return val
	}
	return val + "\t" + desc
}

func cleanASCII(s string, limit int) (string, bool) {
	for i := range len(s) {
		if s[i] < 0x20 || s[i] >= 0x7f {
			return "", false
		}
	}
	return s[:min(len(s), limit)], true
}

func cleanCompValue(s string) string {
	if out, ok := cleanASCII(s, api.SessionNameMax); ok {
		return out
	}
	return api.CleanText(stripEscapes(s), api.SessionNameMax)
}

func cleanCompDesc(s string) string {
	if out, ok := cleanASCII(s, len(s)); ok {
		flat := strings.Join(strings.Fields(out), " ")
		return flat[:min(len(flat), compDescMax)]
	}
	flat := strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ", "\t", " ").Replace(stripEscapes(s))
	return api.CleanText(strings.Join(strings.Fields(flat), " "), compDescMax)
}

func stripEscapes(s string) string {
	var b strings.Builder
	r := []rune(s)
	for i := 0; i < len(r); i++ {
		switch r[i] {
		case '\x1b':
			if i+1 < len(r) {
				switch r[i+1] {
				case '[':
					i = skipCSI(r, i+2)
				case ']', 'P', 'X', '^', '_':
					i = skipControlString(r, i+2, r[i+1] == ']')
				default:
					j := i + 1
					for j < len(r) && r[j] >= 0x20 && r[j] <= 0x2f {
						j++
					}
					if j < len(r) && r[j] >= 0x30 && r[j] <= 0x7e {
						i = j
					} else {
						i = j - 1
					}
				}
			}
		case 0x9b:
			i = skipCSI(r, i+1)
		case 0x90, 0x98, 0x9d, 0x9e, 0x9f:
			i = skipControlString(r, i+1, r[i] == 0x9d)
		default:
			b.WriteRune(r[i])
		}
	}
	return b.String()
}

func skipCSI(r []rune, j int) int {
	for j < len(r) && r[j] >= 0x30 && r[j] <= 0x3f {
		j++
	}
	for j < len(r) && r[j] >= 0x20 && r[j] <= 0x2f {
		j++
	}
	if j < len(r) && r[j] >= 0x40 && r[j] <= 0x7e {
		return j
	}
	return j - 1
}

func skipControlString(r []rune, j int, bel bool) int {
	for ; j < len(r); j++ {
		if bel && r[j] == '\x07' || r[j] == 0x9c {
			return j
		}
		if r[j] == '\x1b' && j+1 < len(r) && r[j+1] == '\\' {
			return j + 1
		}
	}
	return len(r)
}
