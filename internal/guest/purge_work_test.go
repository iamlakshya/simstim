package guest

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/proto"
)

func TestPurgeWorkDecodeRejectsPathsArgvAndUntrustedHistoryV1(t *testing.T) {
	valid := proto.PurgeWorkInspectArgsV1{Version: 1, Primary: "primary2", Repositories: []proto.PurgeWorkRepositoryV1{{Name: "repo", Saved: []string{strings.Repeat("a", 40)}}}}
	raw, _ := json.Marshal(valid)
	if _, err := decodePurgeWorkV1(raw); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		`{"v":1,"primary":"primary2","repositories":[]}`,
		`{"v":1,"primary":"primary2","repositories":[{"name":"../host","saved":[]}]}`,
		`{"v":1,"primary":"primary2","repositories":[{"name":"repo","saved":["guest claim"]}]}`,
		`{"v":1,"primary":"primary2","repositories":[{"name":"repo","saved":[],"argv":["/bin/sh"]}]}`,
		`{"v":1,"primary":"primary2","repositories":[{"name":"repo","saved":[]},{"name":"repo","saved":[]}]}`,
	} {
		if _, err := decodePurgeWorkV1(json.RawMessage(raw)); err == nil {
			t.Fatal("untrusted workspace inspection reached the runner", raw)
		}
	}
}
