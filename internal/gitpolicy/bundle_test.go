package gitpolicy

import (
	"strings"
	"testing"
)

func TestBundleHeaderDoesNotConsumePackAndPreservesPrerequisites(t *testing.T) {
	seed, tip := strings.Repeat("a", 40), strings.Repeat("b", 40)
	header, err := ReadBundleHeader(strings.NewReader("# v2 git bundle\n-"+seed+" seed\n"+tip+" refs/heads/session/topic\n\nPACK-OPAQUE"), 4096)
	if err != nil || len(header.Prerequisites) != 1 || header.Prerequisites[0] != seed || header.Heads["refs/heads/session/topic"] != tip {
		t.Fatalf("%+v %v", header, err)
	}
	for _, data := range []string{"bad\n\n", "# v2 git bundle\nmissing newline", "# v2 git bundle\ninvalid ref\n\n", "# v2 git bundle\n" + tip + " ref\n" + tip + " ref\n\n"} {
		if _, err := ReadBundleHeader(strings.NewReader(data), 4096); err == nil {
			t.Fatal("malformed native header accepted")
		}
	}
}
