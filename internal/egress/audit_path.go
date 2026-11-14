package egress

import (
	"strings"
	"unicode/utf8"

	"github.com/iamlakshya/simstim/internal/api"
)

func cleanAuditPath(raw string, secrets *auditSecrets) string {
	path, ok := prepareAuditPath(raw)
	if !ok {
		return omittedAuditPath
	}
	cutAt, safe := safeAuditOrigin(path)
	if !safe {
		return omittedAuditPath
	}
	parts, ok := cleanAndRedactAuditParts([]auditPart{{text: path, cutAt: cutAt, hasCut: cutAt >= 0}}, secrets)
	if !ok {
		return omittedAuditPath
	}
	for range maxScrubDecodePasses {
		next, changed, withinBudget := transformAuditParts(parts, true)
		if !withinBudget {
			return omittedAuditPath
		}
		if !changed {
			return truncateUTF8(renderAuditParts(stripAuditPartQuery(parts)), MaxAuditPathBytes)
		}
		parts, ok = cleanAndRedactAuditParts(next, secrets)
		if !ok {
			return omittedAuditPath
		}
	}
	if auditPartsHaveEscapes(parts) {
		return omittedAuditPath
	}
	return truncateUTF8(renderAuditParts(stripAuditPartQuery(parts)), MaxAuditPathBytes)
}

func cleanAuditText(text string, secrets *auditSecrets, limit int) string {
	if len(text) > maxScrubInputBytes {
		return api.CleanText("[text omitted]", limit)
	}
	parts, ok := cleanAndRedactAuditParts([]auditPart{{text: text}}, secrets)
	if !ok {
		return api.CleanText("[text omitted]", limit)
	}
	return api.CleanText(renderAuditParts(parts), limit)
}

func cleanAndRedactAuditParts(parts []auditPart, secrets *auditSecrets) ([]auditPart, bool) {
	parts, ok := redactAuditParts(parts, secrets)
	if !ok {
		return nil, false
	}
	parts, _, ok = transformAuditParts(parts, false)
	if !ok {
		return nil, false
	}
	return redactAuditParts(parts, secrets)
}

// Only source bytes are transformed. Public labels already have a chosen
// secret identity and cannot become another secret's raw input.
func transformAuditParts(parts []auditPart, decode bool) ([]auditPart, bool, bool) {
	var out auditPartBuilder
	changed := false
	for _, part := range parts {
		if !part.protected {
			old := part.text
			if part.hasCut && (part.cutAt < 0 || part.cutAt > len(old)) {
				return nil, false, false
			}
			if decode {
				var decoded bool
				part.text, decoded = decodeAuditEscapes(part.text)
				changed = changed || decoded
				if part.hasCut {
					part.cutAt = decodedAuditCut(old, part.cutAt)
				}
			} else {
				part.text = api.CleanText(part.text, len(part.text))
				changed = changed || part.text != old
				if part.hasCut {
					// Source cuts name an ASCII delimiter or escape start.
					// Decoding keeps them at rune boundaries, as do labels.
					part.cutAt = len(api.CleanText(old[:part.cutAt], part.cutAt))
				}
			}
		}
		if err := out.part(part); err != nil {
			return nil, false, false
		}
	}
	return out.take(), changed, true
}

func stripAuditPartQuery(parts []auditPart) []auditPart {
	var out auditPartBuilder
	for _, part := range parts {
		if part.hasCut {
			part.text, part.hasCut = part.text[:part.cutAt], false
			_ = out.part(part) // A prefix cannot exceed the input budget.
			break
		}
		_ = out.part(part) // The existing parts already fit the same budget.
	}
	return out.take()
}

func decodedAuditCut(raw string, cutAt int) int {
	output := 0
	for i := 0; i < len(raw); {
		if i >= cutAt {
			return output
		}
		size := 1
		if raw[i] == '%' && i+2 < len(raw) {
			_, hi := auditHex(raw[i+1])
			_, lo := auditHex(raw[i+2])
			if hi && lo {
				size = 3
			}
		}
		output++
		i += size
	}
	return output
}

func auditPartsHaveEscapes(parts []auditPart) bool {
	for _, part := range parts {
		if !part.protected {
			if _, changed := decodeAuditEscapes(part.text); changed {
				return true
			}
		}
	}
	return false
}

// Query and fragment bytes are not diagnostic input. Their delimiter scan
// has a hard ceiling even when a direct caller supplies an oversized URI.
func prepareAuditPath(raw string) (string, bool) {
	scan := raw[:min(len(raw), maxHTTPHeaderBytes)]
	if i := strings.IndexAny(scan, "?#"); i >= 0 {
		raw = raw[:i]
	} else if len(raw) > maxHTTPHeaderBytes {
		return "", false
	}
	if len(raw) > maxScrubInputBytes {
		return "", false
	}
	if raw == "" || raw == "*" {
		return raw, true
	}
	if colon := auditSchemeEnd(raw); colon >= 0 && !strings.HasPrefix(raw[colon+1:], "//") {
		return "", false
	}
	if hasAuditAuthority(raw) {
		return stripQueryAndFragment(raw), true
	}
	if raw[0] != '/' {
		return "", false
	}
	return raw, true
}

// Inspect original source independently so an early secret match cannot
// conceal a newly decoded URI or query delimiter. The returned cut names
// its first original byte, not a public label. This shadow is never logged.
func safeAuditOrigin(source string) (int, bool) {
	origin := auditOrigin{text: source, starts: make([]int, len(source))}
	for i := range origin.starts {
		origin.starts[i] = i
	}
	cutAt := -1
	for range maxScrubDecodePasses {
		var ok bool
		origin, ok = cleanAuditOrigin(origin)
		if !ok || unsafeAuditReconstruction(origin.text) {
			return 0, false
		}
		if i := strings.IndexAny(origin.text, "?#"); i >= 0 {
			if cutAt < 0 || origin.starts[i] < cutAt {
				cutAt = origin.starts[i]
			}
			origin.text, origin.starts = origin.text[:i], origin.starts[:i]
		}
		next, changed := decodeAuditOrigin(origin)
		if !changed {
			return cutAt, true
		}
		origin = next
	}
	var ok bool
	origin, ok = cleanAuditOrigin(origin)
	if !ok || unsafeAuditReconstruction(origin.text) {
		return 0, false
	}
	if i := strings.IndexAny(origin.text, "?#"); i >= 0 {
		if cutAt < 0 || origin.starts[i] < cutAt {
			cutAt = origin.starts[i]
		}
		origin.text, origin.starts = origin.text[:i], origin.starts[:i]
	}
	_, changed := decodeAuditOrigin(origin)
	return cutAt, !changed
}

type auditOrigin struct {
	text   string
	starts []int
}

func cleanAuditOrigin(source auditOrigin) (auditOrigin, bool) {
	var text strings.Builder
	starts := make([]int, 0, len(source.text))
	for i := 0; i < len(source.text); {
		_, size := utf8.DecodeRuneInString(source.text[i:])
		clean := api.CleanText(source.text[i:i+size], size)
		if len(clean) > maxScrubInputBytes-text.Len() {
			return auditOrigin{}, false
		}
		text.WriteString(clean)
		for range len(clean) {
			starts = append(starts, source.starts[i])
		}
		i += size
	}
	return auditOrigin{text: text.String(), starts: starts}, true
}

func decodeAuditOrigin(source auditOrigin) (auditOrigin, bool) {
	var text strings.Builder
	text.Grow(len(source.text))
	starts := make([]int, 0, len(source.text))
	changed := false
	for i := 0; i < len(source.text); i++ {
		value, start := source.text[i], source.starts[i]
		if value == '%' && i+2 < len(source.text) {
			hi, hiOK := auditHex(source.text[i+1])
			lo, loOK := auditHex(source.text[i+2])
			if hiOK && loOK {
				value, i, changed = hi<<4|lo, i+2, true
			}
		}
		text.WriteByte(value)
		starts = append(starts, start)
	}
	return auditOrigin{text: text.String(), starts: starts}, changed
}

func unsafeAuditReconstruction(source string) bool {
	return hasAuditAuthority(source) || auditSchemeEnd(source) >= 0
}

func auditSchemeEnd(raw string) int {
	colon := strings.IndexByte(raw, ':')
	if colon < 1 {
		return -1
	}
	for i := range colon {
		b := raw[i]
		if b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' {
			continue
		}
		if i == 0 || !(b >= '0' && b <= '9' || b == '+' || b == '-' || b == '.') {
			return -1
		}
	}
	return colon
}
