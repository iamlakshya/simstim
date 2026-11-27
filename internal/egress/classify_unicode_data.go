package egress

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"io"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"
)

// The complete original prototype file has one representation and one parser. It is
// Unicode15 data under the licence in LICENSES/Unicode-3.0.txt, as NOTICE records.
//
//go:embed rules/confusables-15.0.0.txt.gz
var classifierConfusableCompressed []byte

const classifierConfusableDataSHA256 = "2b10130885c3370b101c52d7baedc452ab7f0e257b86c1e52ee657ecfc29ce64"
const classifierConfusableEncodedSHA256 = "f015464fcf0a0050dc412f3aaa184f0f71ca640720305dc10e388daeb261ca82"
const classifierConfusableRawBytes = 718293
const classifierConfusableDigest = "3c277c7df96bf4df882397ea2b5979abca852ad4798b8053e48b3c42512207a8"

var loadClassifierConfusables = sync.OnceValues(func() (map[rune]string, error) {
	raw, err := decodeClassifierConfusables(classifierConfusableCompressed)
	if err != nil {
		return nil, err
	}
	return parseClassifierConfusables(raw)
})

func decodeClassifierConfusables(encoded []byte) (string, error) {
	digest := sha256.Sum256(encoded)
	if hex.EncodeToString(digest[:]) != classifierConfusableEncodedSHA256 {
		return "", errClassifierUnicode
	}
	return decodeClassifierConfusablePayload(encoded)
}

// This serialization layer accepts exactly one complete member of the
// pinned original size. It neither normalises nor scans prototype grammar.
func decodeClassifierConfusablePayload(encoded []byte) (string, error) {
	source := bytes.NewReader(encoded)
	reader, err := gzip.NewReader(source)
	if err != nil {
		return "", errClassifierUnicode
	}
	reader.Multistream(false)
	decoded := make([]byte, classifierConfusableRawBytes+1)
	n := 0
	var readErr error
	for n < len(decoded) {
		got, err := reader.Read(decoded[n:])
		n += got
		if err != nil {
			readErr = err
			break
		}
		if got == 0 {
			readErr = io.ErrNoProgress
			break
		}
	}
	closeErr := reader.Close()
	if n != classifierConfusableRawBytes || readErr != io.EOF || closeErr != nil || source.Len() != 0 {
		return "", errClassifierUnicode
	}
	digest := sha256.Sum256(decoded[:n])
	if hex.EncodeToString(digest[:]) != classifierConfusableDataSHA256 {
		return "", errClassifierUnicode
	}
	return string(decoded[:n]), nil
}

func parseClassifierConfusables(raw string) (map[rune]string, error) {
	digest := sha256.Sum256([]byte(raw))
	if hex.EncodeToString(digest[:]) != classifierConfusableDataSHA256 {
		return nil, errClassifierUnicode
	}
	result := make(map[rune]string)
	for _, line := range strings.Split(raw, "\n") {
		line, _, _ = strings.Cut(line, "#")
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Split(line, ";")
		if len(fields) != 3 || strings.TrimSpace(fields[2]) != "MA" {
			return nil, errClassifierUnicode
		}
		source, err := classifierDataScalar(strings.TrimSpace(fields[0]))
		if err != nil {
			return nil, err
		}
		if _, exists := result[source]; exists {
			return nil, errClassifierUnicode
		}
		var target strings.Builder
		scalars := strings.Fields(fields[1])
		if len(scalars) == 0 {
			return nil, errClassifierUnicode
		}
		for _, encoded := range scalars {
			scalar, err := classifierDataScalar(encoded)
			if err != nil {
				return nil, err
			}
			target.WriteRune(scalar)
		}
		result[source] = target.String()
	}
	if len(result) != 6311 {
		return nil, errClassifierUnicode
	}
	return result, nil
}

func classifierDataScalar(encoded string) (rune, error) {
	scalar, err := strconv.ParseUint(encoded, 16, 32)
	if err != nil || !utf8.ValidRune(rune(scalar)) {
		return 0, errClassifierUnicode
	}
	return rune(scalar), nil
}
