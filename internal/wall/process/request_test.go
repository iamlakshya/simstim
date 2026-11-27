package process

import "testing"

func TestV3ReservesCreatorCompletionWithoutChangingV1V2Offsets(t *testing.T) {
	for _, test := range []struct {
		version, fd int
		valid       bool
	}{{1, 6, true}, {2, 7, true}, {3, 7, false}, {3, 8, true}} {
		req := Request{V: test.version, Mounts: []MountRequest{{FD: test.fd, Guest: "/workspace"}}}
		if err := req.Validate(); (err == nil) != test.valid {
			t.Fatal("versioned native creator descriptor boundary changed", test, err)
		}
	}
}
