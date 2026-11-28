package cli

import (
	"encoding/json"
	"math"
	"strconv"
	"testing"

	"github.com/iamlakshya/simstim/internal/client"
)

func TestSessionJSONPreservesFullIntegerPrecision(t *testing.T) {
	for _, value := range []int64{1<<53 + 1, math.MaxInt64 - 1, math.MaxInt64} {
		t.Run(strconv.FormatInt(value, 10), func(t *testing.T) {
			session := client.Session{ID: "abcd2345", MemoryMax: value, MemoryCurrent: value - 1}
			encoded, err := versioned(sessionObject("box", session))
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(encoded, &fields); err != nil {
				t.Fatal(err)
			}
			for name, want := range map[string]int64{"memory_max": value, "memory_current": value - 1} {
				if got := string(fields[name]); got != strconv.FormatInt(want, 10) {
					t.Fatalf("encoded %s = %s, want exact %d: %s", name, got, want, encoded)
				}
			}
			for _, absent := range []string{"since", "signal", "reason"} {
				if _, set := fields[absent]; set {
					t.Errorf("unset %s was included: %s", absent, encoded)
				}
			}
		})
	}
}
