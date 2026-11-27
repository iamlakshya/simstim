package client

import (
	"bytes"
	"strings"
	"testing"
)

func TestMembershipCanonicalWriterRotatesEveryActualWrite(t *testing.T) {
	stateFDTestHome(t)
	seen := map[string]bool{}
	for _, hosts := range [][]Host{{{Name: "box"}}, {{Name: "box"}}, nil, nil} {
		if err := SaveHosts(t.Context(), hosts); err != nil {
			t.Fatal(err)
		}
		generation, err := currentMembershipGeneration(t.Context())
		if err != nil || !ValidHostID(generation) || seen[generation] {
			t.Fatalf("bulk/empty/same-content write reused generation: %q,%v", generation, err)
		}
		seen[generation] = true
	}
	host := Host{Name: "box"}
	for range 2 {
		was, err := SavedEntry(host.Name)
		if err != nil {
			t.Fatal(err)
		}
		if err := UpdateHosts(t.Context(), HostChange{Kind: "fixture", Was: was, Now: &host}); err != nil {
			t.Fatal(err)
		}
		generation, err := currentMembershipGeneration(t.Context())
		if err != nil || seen[generation] {
			t.Fatalf("single-host writer skipped canonical generation: %q,%v", generation, err)
		}
		seen[generation] = true
	}
}

func TestMembershipGenerationOnlyExactFirstLineHasAuthority(t *testing.T) {
	token := strings.Repeat("0123456789abcdef", 2)
	line := membershipGenerationPrefix + token + "\n"
	if got, err := readMembershipGeneration(t.Context(), bytes.NewReader([]byte(line+"[[host]]\nname='box'\n"))); err != nil || got != token {
		t.Fatalf("canonical first line refused: %q,%v", got, err)
	}
	for _, data := range []string{
		"# older header\n" + line,
		"value=\"\"\"\n" + line + "\"\"\"\n",
		" " + line,
		"\xef\xbb\xbf" + line,
		membershipGenerationPrefix + token + "extra\n",
		membershipGenerationPrefix + token + "\r\n",
		membershipGenerationPrefix + token[:31] + "\n",
		membershipGenerationPrefix + token[:31] + "G\n",
	} {
		if _, err := readMembershipGeneration(t.Context(), bytes.NewReader([]byte(data))); err == nil {
			t.Fatalf("noncanonical/spoof marker was authority: %q", data)
		}
	}
}
