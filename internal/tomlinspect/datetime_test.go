package tomlinspect

import (
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
)

func TestPinnedNativeDatesMatchFullDecoder(t *testing.T) {
	for _, token := range []string{
		"1979-05-27T07:32:00Z", "1979-05-27t07:32:00z", "1979-05-27 07:32:00Z",
		"1979-05-27T07:32:00.999999999123456789Z", "1979-05-27T07:32:00.000000000999Z",
		"1979-05-27T07:32:00+00:00", "1979-05-27T07:32:00-00:00",
		"1979-05-27T07:32:00+24:00", "1979-05-27T07:32:00+00:60",
		"1979-05-27T07:32:00+24:60", "1979-05-27T07:32:00+25:00",
		"1979-05-27T07:32:00", "1979-05-27T07:32:00.123456789123456789",
		"1979-05-27", "07:32:00", "07:32:00.999999999999999999", "07:32",
		"1979-05-27T07:32Z", "1979-05-27T07:32+00:00", "1979-05-27T07:32",
		"1979-05-27T7:32:00Z", "1979-5-27T07:32:00Z", "1979-05-7T07:32:00Z",
		"7:32", "7:32:00", "1979-05-27T07:3:00Z", "1979-05-27T07:32:0Z",
		"2000-02-29", "1900-02-29", "0000-02-29", "1979-05-27T24:00:00Z",
		"1979-05-27T07:32:60Z", "1979-05-27T07:32.123Z", "07:32.123",
		"1979-05-27T07:32:00.Z", "1979-05-27T07:32:00.1.2Z",
		"1979-05-27T07:32:00." + strings.Repeat("9", 12000) + "Z",
		"1979-05-27T07:32:00." + strings.Repeat("0", 12000) + ".1Z",
	} {
		checkNativeDate(t, token)
	}
}

func TestPinnedNativeDatesKeepExistingInputBound(t *testing.T) {
	prefix, suffix := "1979-05-27T07:32:00.", "+24:60"
	token := prefix + strings.Repeat("9", MaxInput-len("v=\n")-len(prefix)-len(suffix)) + suffix
	checkNativeDate(t, token)
}

func checkNativeDate(t *testing.T, token string) {
	t.Helper()
	var want map[string]any
	_, oracleErr := toml.Decode("v="+token+"\n", &want)
	s, err := newSource(t.Context(), strings.NewReader(token), int64(len(token)))
	if err != nil {
		t.Fatal(err)
	}
	got, err := dateValue(numberSpan{source: s, span: Span{End: int64(len(token))}})
	if (oracleErr == nil) != (err == nil) {
		t.Fatalf("native date acceptance differs at length %d: oracle %v, cursor %v", len(token), oracleErr, err)
	}
	if err != nil {
		return
	}
	date, ok := want["v"].(time.Time)
	if !ok {
		t.Fatal("date oracle did not return native time.Time")
	}
	wantName, wantOffset := date.Zone()
	gotName, gotOffset := got.Zone()
	if !got.Equal(date) || gotName != wantName || gotOffset != wantOffset || got.Nanosecond() != date.Nanosecond() {
		t.Fatalf("native date value or zone differs at length %d: oracle %v (%s,%d), cursor %v (%s,%d)", len(token), date, wantName, wantOffset, got, gotName, gotOffset)
	}
}
