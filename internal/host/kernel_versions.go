package host

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

type kernelVersionV1 struct {
	numbers    [3]uint64
	prerelease string
	candidate  uint64
}

func parseKernelVersionV1(text string) (kernelVersionV1, error) {
	var version kernelVersionV1
	text = strings.TrimSpace(text)
	numbers, position, err := parseKernelReleasePrefixV1(text)
	if err != nil {
		return version, err
	}
	version.numbers = numbers
	prerelease, candidate, err := parseRuntimePrereleaseV1(text, text[position:])
	if err != nil {
		return version, err
	}
	version.prerelease, version.candidate = prerelease, candidate
	return version, nil
}

func parseKernelReleasePrefixV1(text string) ([3]uint64, int, error) {
	var numbers [3]uint64
	position := 0
	for index := range numbers {
		value, next, err := parseUintRunV1(text, position)
		if err != nil {
			return numbers, 0, err
		}
		numbers[index], position = value, next
		if index == 2 {
			break
		}
		if position >= len(text) || text[position] != '.' {
			if index == 1 {
				break
			}
			return numbers, 0, errors.New("kernel version has no stable series")
		}
		position++
	}
	return numbers, position, nil
}

func parseUintRunV1(text string, start int) (uint64, int, error) {
	end := start
	for end < len(text) && isASCIIDigitV1(text[end]) {
		end++
	}
	if start == end {
		return 0, 0, errors.New("kernel version has no numeric release")
	}
	value, err := strconv.ParseUint(text[start:end], 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("parse kernel release %q: %w", text, err)
	}
	return value, end, nil
}

func parseRuntimePrereleaseV1(full, suffix string) (string, uint64, error) {
	for _, marker := range []string{"pre", "rc"} {
		if !strings.HasPrefix(suffix, "-"+marker) {
			continue
		}
		start := len(marker) + 1
		end := start
		for end < len(suffix) && isASCIIDigitV1(suffix[end]) {
			end++
		}
		if end == start || end < len(suffix) && !isPrereleaseDelimiterV1(suffix[end]) {
			continue
		}
		candidate, err := strconv.ParseUint(suffix[start:end], 10, 64)
		if err != nil {
			return "", 0, fmt.Errorf("parse kernel prerelease %q: %w", full, err)
		}
		return marker, candidate, nil
	}
	return "", 0, nil
}

func isPrereleaseDelimiterV1(value byte) bool {
	return value == '-' || value == '+' || value == '.'
}

// parseCanonicalKernelVersionV1 requires exact X.Y.Z[-preN|-rcN] syntax so
// catalogue typos or trailing vendor suffixes never silently decode as final.
func parseCanonicalKernelVersionV1(text string) (kernelVersionV1, error) {
	var version kernelVersionV1
	if text == "" || strings.TrimSpace(text) != text {
		return version, errors.New("catalogue kernel version is empty or padded")
	}
	releaseText, suffix, hasSuffix := strings.Cut(text, "-")
	parts := strings.Split(releaseText, ".")
	if len(parts) != len(version.numbers) {
		return version, errors.New("catalogue kernel version must have three numeric parts")
	}
	for index, part := range parts {
		if !isCanonicalDecimalV1(part) {
			return version, errors.New("catalogue kernel version has a non-canonical number")
		}
		value, err := strconv.ParseUint(part, 10, 64)
		if err != nil {
			return version, fmt.Errorf("parse catalogue kernel version %q: %w", text, err)
		}
		version.numbers[index] = value
	}
	if !hasSuffix {
		return version, nil
	}
	marker, candidate, err := parseCanonicalPrereleaseV1(text, suffix)
	if err != nil {
		return version, err
	}
	version.prerelease, version.candidate = marker, candidate
	return version, nil
}

func parseCanonicalPrereleaseV1(full, suffix string) (string, uint64, error) {
	for _, marker := range []string{"pre", "rc"} {
		digits, ok := strings.CutPrefix(suffix, marker)
		if !ok {
			continue
		}
		if !isCanonicalDecimalV1(digits) {
			return "", 0, errors.New("catalogue kernel prerelease is non-canonical")
		}
		candidate, err := strconv.ParseUint(digits, 10, 64)
		if err != nil {
			return "", 0, fmt.Errorf("parse catalogue kernel prerelease %q: %w", full, err)
		}
		if marker == "rc" && candidate == 0 {
			return "", 0, errors.New("catalogue kernel rc candidate must be positive")
		}
		return marker, candidate, nil
	}
	return "", 0, errors.New("catalogue kernel version has an unknown suffix")
}

func parseUbuntuReleaseV1(text string) (kernelVersionV1, string, string, bool) {
	text = strings.TrimSpace(text)
	releaseText, rest, ok := strings.Cut(text, "-")
	if !ok {
		return kernelVersionV1{}, "", "", false
	}
	release, err := parseCanonicalKernelVersionV1(releaseText)
	if err != nil || release.prerelease != "" {
		return kernelVersionV1{}, "", "", false
	}
	abi, flavour, ok := strings.Cut(rest, "-")
	if !ok || !isCanonicalDecimalV1(abi) || !validKernelFlavourV1(flavour) {
		return kernelVersionV1{}, "", "", false
	}
	return release, abi, flavour, true
}

func parseUbuntuPackageABIV1(revision string) (string, bool) {
	abi, upload, ok := strings.Cut(revision, ".")
	if !ok || !isCanonicalDecimalV1(abi) || upload == "" || !isASCIIDigitV1(upload[0]) {
		return "", false
	}
	return abi, true
}

func validKernelFlavourV1(text string) bool {
	if text == "" || len(text) > 64 {
		return false
	}
	prevHyphen := true
	for index := range len(text) {
		ch := text[index]
		if ch == '-' {
			if prevHyphen || index == len(text)-1 {
				return false
			}
			prevHyphen = true
			continue
		}
		if (ch < 'a' || ch > 'z') && !isASCIIDigitV1(ch) {
			return false
		}
		prevHyphen = false
	}
	return true
}

func isASCIIDigitV1(value byte) bool {
	return value >= '0' && value <= '9'
}

func isCanonicalDecimalV1(text string) bool {
	if text == "" || len(text) > 1 && text[0] == '0' {
		return false
	}
	for index := range len(text) {
		if !isASCIIDigitV1(text[index]) {
			return false
		}
	}
	return true
}

func prereleaseRankV1(marker string) int {
	switch marker {
	case "pre":
		return 0
	case "rc":
		return 1
	default:
		return 2
	}
}

func (version kernelVersionV1) compare(other kernelVersionV1) int {
	for index, value := range version.numbers {
		if value < other.numbers[index] {
			return -1
		}
		if value > other.numbers[index] {
			return 1
		}
	}
	if prereleaseRankV1(version.prerelease) < prereleaseRankV1(other.prerelease) {
		return -1
	}
	if prereleaseRankV1(version.prerelease) > prereleaseRankV1(other.prerelease) {
		return 1
	}
	if version.candidate < other.candidate {
		return -1
	}
	if version.candidate > other.candidate {
		return 1
	}
	return 0
}

type debianVersionV1 struct{ epoch, upstream, revision string }

func parseDebianVersionV1(text string) (debianVersionV1, error) {
	version := debianVersionV1{epoch: "0", revision: "0"}
	if text == "" || len(text) > 512 {
		return version, errors.New("kernel package version is empty or too long")
	}
	if epoch, rest, found := strings.Cut(text, ":"); found {
		if epoch == "" || strings.Trim(epoch, "0123456789") != "" {
			return version, errors.New("kernel package epoch is invalid")
		}
		version.epoch, text = trimDecimalZeroesV1(epoch), rest
	}
	if text == "" || !isASCIIDigitV1(text[0]) {
		return version, errors.New("kernel package version must begin with a number")
	}
	if last := strings.LastIndexByte(text, '-'); last >= 0 {
		version.upstream, version.revision = text[:last], text[last+1:]
	} else {
		version.upstream = text
	}
	if version.upstream == "" || version.revision == "" {
		return version, errors.New("kernel package version has an empty part")
	}
	if !validDebianVersionPartV1(version.upstream, true) || !validDebianVersionPartV1(version.revision, false) {
		return version, errors.New("kernel package version has an invalid character")
	}
	return version, nil
}

func validDebianVersionPartV1(text string, allowHyphenColon bool) bool {
	for index := range len(text) {
		ch := text[index]
		if ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || isASCIIDigitV1(ch) || ch == '.' || ch == '+' || ch == '~' {
			continue
		}
		if (ch == '-' || ch == ':') && allowHyphenColon {
			continue
		}
		return false
	}
	return true
}

func compareDebianVersionsV1(left, right string) (int, error) {
	a, err := parseDebianVersionV1(left)
	if err != nil {
		return 0, err
	}
	b, err := parseDebianVersionV1(right)
	if err != nil {
		return 0, err
	}
	if order := compareDecimalV1(a.epoch, b.epoch); order != 0 {
		return order, nil
	}
	if order := compareDebianPartV1(a.upstream, b.upstream); order != 0 {
		return order, nil
	}
	return compareDebianPartV1(a.revision, b.revision), nil
}

func trimDecimalZeroesV1(value string) string {
	value = strings.TrimLeft(value, "0")
	if value == "" {
		return "0"
	}
	return value
}

func compareDecimalV1(left, right string) int {
	left, right = trimDecimalZeroesV1(left), trimDecimalZeroesV1(right)
	if len(left) < len(right) {
		return -1
	}
	if len(left) > len(right) {
		return 1
	}
	return strings.Compare(left, right)
}

func debianCharacterOrderV1(value byte) int {
	if value == '~' {
		return -1
	}
	if value == 0 {
		return 0
	}
	if value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' {
		return int(value)
	}
	return int(value) + 256
}

func compareDebianPartV1(left, right string) int {
	i, j := 0, 0
	for i < len(left) || j < len(right) {
		var order int
		order, i, j = compareDebianNonDigitsV1(left, right, i, j)
		if order != 0 {
			return order
		}
		var leftDigits, rightDigits string
		leftDigits, i = nextDebianDigitsV1(left, i)
		rightDigits, j = nextDebianDigitsV1(right, j)
		if order := compareDecimalV1(leftDigits, rightDigits); order != 0 {
			return order
		}
	}
	return 0
}

func nonDigitAtV1(text string, index int) byte {
	if index < len(text) && !isASCIIDigitV1(text[index]) {
		return text[index]
	}
	return 0
}

func compareDebianNonDigitsV1(left, right string, i, j int) (int, int, int) {
	for i < len(left) && !isASCIIDigitV1(left[i]) || j < len(right) && !isASCIIDigitV1(right[j]) {
		a, b := nonDigitAtV1(left, i), nonDigitAtV1(right, j)
		x, y := debianCharacterOrderV1(a), debianCharacterOrderV1(b)
		if x < y {
			return -1, i, j
		}
		if x > y {
			return 1, i, j
		}
		if a != 0 {
			i++
		}
		if b != 0 {
			j++
		}
	}
	return 0, i, j
}

func nextDebianDigitsV1(text string, start int) (string, int) {
	end := start
	for end < len(text) && isASCIIDigitV1(text[end]) {
		end++
	}
	return text[start:end], end
}
