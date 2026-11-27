package tomlinspect

import (
	"context"
	"errors"
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// This is a scalar oracle only. It cannot stand in for whole Inspect/C9/P23
// acceptance, the maximum-depth parser proof or the whole actor meter.
func TestPinnedNativeNumbersMatchFullDecoder(t *testing.T) {
	integers := []string{
		"0", "+0", "-0", "1", "-1", "9223372036854775807", "-9223372036854775808",
		"9223372036854775808", "-9223372036854775809", "18446744073709551616",
		"0x7fff_ffff_ffff_ffff", "0x8000000000000000", "0o1777777777777777777777",
		"0b0111_1111", "0x_FF", "0xFF_", "0xF__F", "0_0", "00", "+00", "1_0",
		"0x" + strings.Repeat("0", 9000) + "1", "0x" + strings.Repeat("0_", 9000) + "F",
	}
	floats := []string{
		"0.0", "-0.0", "+0.0", "nan", "+nan", "-nan", "inf", "+inf", "-inf",
		"1e0", "1e+01", "1e-01", "1_2.3_4e+0_2", "00.1", "0_0.1", "1.e2",
		"1._0", "1.0_e2", "1e_2", "1e+_2", "1.2.3", "1e2e3", "1.0_", "1e",
		"1.7976931348623157e308", "1.7976931348623159e308", "-1.7976931348623159e308",
		"5e-324", "2.4703282292062327e-324", "2.4703282292062328e-324",
		"9007199254740993.0", "1.00000000000000011102230246251565404236316680908203125",
		"1.000000000000000111022302462515654042363166809082031250000000000001",
		"1" + strings.Repeat("0", 1200) + "e-1200",
		"1234567890123456789" + strings.Repeat("1234567890", 120) + "e-1218",
		"1000000000000000000" + strings.Repeat("0", 1200) + "1e-1219",
		"1." + strings.Repeat("0", 9000) + "1",
		"1e+" + strings.Repeat("0", 9000) + "1",
	}
	for _, token := range integers {
		checkNativeNumber(t, token, false)
	}
	for _, token := range floats {
		checkNativeNumber(t, token, true)
	}
	// Deterministic long mantissas exercise fast/fallback boundaries and
	// cancelling exponents. No command, agent or external service runs.
	state := uint64(0xb9b105db5bd7cb83)
	for i := range 384 {
		length := 19 + i*7
		var token strings.Builder
		token.WriteByte('1' + byte(i%9))
		for range length {
			state ^= state << 13
			state ^= state >> 7
			state ^= state << 17
			token.WriteByte('0' + byte(state%10))
		}
		token.WriteString("e-")
		token.WriteString(strconv.Itoa(length + i%12))
		checkNativeNumber(t, token.String(), true)
	}
}

func checkNativeNumber(t *testing.T, token string, floating bool) {
	t.Helper()
	input := "v=" + token + "\n"
	var want map[string]any
	_, oracleErr := toml.Decode(input, &want)
	s, err := newSource(t.Context(), strings.NewReader(token), int64(len(token)))
	if err != nil {
		t.Fatal(err)
	}
	view := numberSpan{source: s, span: Span{End: int64(len(token))}}
	var actual any
	if floating {
		actual, err = floatValue(view)
	} else {
		actual, err = integerValue(view)
	}
	if (oracleErr == nil) != (err == nil) {
		t.Fatalf("native acceptance differs for length %d: oracle %v, cursor %v", len(token), oracleErr, err)
	}
	if err != nil {
		return
	}
	if floating {
		got, ok := want["v"].(float64)
		if !ok || math.Float64bits(got) != math.Float64bits(actual.(float64)) {
			t.Fatalf("native float bits differ at length %d: oracle %x, cursor %x", len(token), math.Float64bits(got), math.Float64bits(actual.(float64)))
		}
	} else if want["v"] != actual {
		t.Fatalf("native int differs at length %d: oracle %v, cursor %v", len(token), want["v"], actual)
	}
}

func TestNativeNumbersKeepExistingInputBound(t *testing.T) {
	// Prefix and newline are included in the unchanged one MiB limit.
	for _, test := range []struct {
		prefix, suffix string
		floating       bool
	}{
		{prefix: "0x", suffix: "1"},
		{prefix: "0b", suffix: "1"},
		{prefix: "1.", suffix: "0", floating: true},
		{prefix: "1e+", suffix: "1", floating: true},
	} {
		zeros := MaxInput - len("v=\n") - len(test.prefix) - len(test.suffix)
		token := test.prefix + strings.Repeat("0", zeros) + test.suffix
		checkNativeNumber(t, token, test.floating)
	}
}

func TestNativeNumericCursorCancellationAtCachedByte(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	s, err := newSource(ctx, strings.NewReader("123456"), 6)
	if err != nil {
		t.Fatal(err)
	}
	view := numberSpan{source: s, span: Span{End: 6}}
	if view.at(0) != '1' {
		t.Fatal("numeric cursor missed its source")
	}
	cancel()
	defer func() {
		cause, _ := recover().(error)
		if !errors.Is(cause, context.Canceled) {
			t.Fatalf("cached numeric read ignored cancellation: %v", cause)
		}
	}()
	view.at(1)
}
