//go:build linux

package host

import "testing"

func TestHostFactsPressureKeepsUnknownSeparateFromObservedZero(t *testing.T) {
	for _, test := range []struct {
		text  string
		value float64
		known bool
	}{{"some avg10=0.00 avg60=0.00 avg300=0.00 total=0\n", 0, true}, {"some avg10=12.50 avg60=1.00 avg300=0.00 total=19\nfull avg10=3.00 total=1\n", 12.5, true}, {"", 0, false}, {"full avg10=0.00 total=0\n", 0, false}, {"some avg10=NaN total=0\n", 0, false}, {"some avg10=Inf total=0\n", 0, false}, {"some avg10=-1 total=0\n", 0, false}, {"some avg10=100.01 total=0\n", 0, false}, {"some avg10=1 avg10=2 total=0\n", 0, false}, {"some avg60=0\nsome avg10=1\n", 0, false}} {
		value, known := parseHostFactsPressureV1([]byte(test.text))
		if value != test.value || known != test.known {
			t.Fatalf("pressure %q: %v/%v", test.text, value, known)
		}
	}
}
