package gitpolicy

import (
	"bufio"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// BundleHeader contains only the bounded text header of a Git bundle.
// This is not a pack parser. Native Git must still verify and fsck the file.
type BundleHeader struct {
	Prerequisites []string
	Heads         map[string]string
	ObjectFormat  string
}

// ReadBundleHeader recognises only bounded native bundle v2/v3 header text.
// Its buffered reader may read ahead but never interprets pack contents.
func ReadBundleHeader(input io.Reader, limit int64) (BundleHeader, error) {
	header := BundleHeader{Heads: map[string]string{}, ObjectFormat: "sha1"}
	reader := bufio.NewReader(io.LimitReader(input, limit+1))
	var used int64
	read := func() (string, error) {
		line, err := reader.ReadString('\n')
		used += int64(len(line))
		if used > limit {
			return "", fmt.Errorf("bundle header exceeds its bound")
		}
		if err != nil {
			return "", fmt.Errorf("unfinished bundle header: %w", err)
		}
		return strings.TrimSuffix(line, "\n"), nil
	}
	version, err := read()
	if err != nil {
		return header, err
	}
	if version != "# v2 git bundle" && version != "# v3 git bundle" {
		return header, fmt.Errorf("unsupported Git bundle header")
	}
	for {
		line, err := read()
		if err != nil {
			return header, err
		}
		if line == "" {
			return header, nil
		}
		if strings.HasPrefix(line, "@") {
			if version != "# v3 git bundle" {
				return header, fmt.Errorf("bundle v2 has a capability")
			}
			if value, ok := strings.CutPrefix(line, "@object-format="); ok {
				if value != "sha1" && value != "sha256" {
					return header, fmt.Errorf("unsupported bundle object format")
				}
				header.ObjectFormat = value
			}
			continue
		}
		prerequisite := strings.HasPrefix(line, "-")
		if prerequisite {
			line = line[1:]
		}
		oid, ref, ok := strings.Cut(line, " ")
		if !ok || !validOID(oid) {
			return header, fmt.Errorf("invalid bundle header object ID")
		}
		if prerequisite {
			header.Prerequisites = append(header.Prerequisites, oid)
			continue
		}
		if ref == "" || !utf8.ValidString(ref) || strings.ContainsAny(ref, "\x00\r\n") {
			return header, fmt.Errorf("invalid bundle header ref")
		}
		if _, found := header.Heads[ref]; found {
			return header, fmt.Errorf("duplicate bundle header ref")
		}
		header.Heads[ref] = oid
	}
}

func validOID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'f' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}
