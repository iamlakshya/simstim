package cli

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/client/clienttest"
)

// Host data remains exact after the public JSON writer escapes terminal
// controls. Text display cleaning must never alter script data or numbers.
func TestHostShowJSONPreservesHostData(t *testing.T) {
	d := clienttest.HostDetails()
	evil := "\x1bc\x1b]52;c;YQ\x07\u202e\u200b"
	d.Info.Distro += evil + strings.Repeat("x", 500)
	d.Info.Agents["agent"+evil] = "/bin/tool" + evil
	d.Info.AgentVersions["agent"+evil] = "1" + evil
	d.Info.MemTotal = 1<<53 + 1
	b, err := versioned(hostShowObject(d, nil))
	if err != nil {
		t.Fatal(err)
	}
	b = jsonSafe(b)
	if strings.ContainsAny(string(b), "\x1b\x07\u202e\u200b") {
		t.Fatalf("unescaped terminal data: %q", b)
	}
	var got struct{ Info client.HostInfo }
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Info, *d.Info) {
		t.Fatalf("host-show JSON changed raw info: got %+v, want %+v", got.Info, *d.Info)
	}
}
