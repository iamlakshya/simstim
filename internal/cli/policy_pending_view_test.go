package cli

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestPendingNetworkOutputKeepsDistinctOpaqueIDsForTheSameMaskedDisplay(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	first := api.PendingNetworkRequest{Version: 1, ID: strings.Repeat("a", 32), Revision: 1, Host: "private target", Redacted: true, Port: 443, Opened: now, Expires: now.Add(5 * time.Minute), Status: api.NetworkHoldOpen}
	second := first
	second.ID, second.Waiting = strings.Repeat("b", 32), 10
	original := []api.PendingNetworkRequest{first, second}
	var output bytes.Buffer
	if err := writePendingNetworkSnapshot(&output, api.NetworkHoldSnapshot{Version: 1, Revision: 1, Requests: original}, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(output.String(), "\n"), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], first.ID+"\t") || !strings.HasPrefix(lines[1], second.ID+"\t") || !strings.Contains(lines[0], "0 waiting") || !strings.Contains(lines[1], "10 waiting") {
		t.Fatalf("masked displays merged or zero-waiter hold disappeared: %q", output.String())
	}
	if !reflect.DeepEqual(original, []api.PendingNetworkRequest{first, second}) {
		t.Fatal("drawing changed opaque IDs or original hold deadlines")
	}
}

func TestPendingNetworkOutputRejectsMalformedDescriptorBeforeWriting(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	request := api.PendingNetworkRequest{Version: 1, ID: strings.Repeat("a", 32), Revision: 1, Host: "private\x1b]52;c;value\x07", Redacted: true, Port: 443, Opened: now, Expires: now.Add(5 * time.Minute), Status: api.NetworkHoldOpen}
	var output bytes.Buffer
	if err := writePendingNetworkRequest(&output, request, now); err == nil || output.Len() != 0 {
		t.Fatalf("malformed display reached terminal output: %q %v", output.String(), err)
	}
}

func TestPendingNetworkSnapshotValidatesWholeListBeforeOutput(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	request := api.PendingNetworkRequest{Version: 1, ID: strings.Repeat("a", 32), Revision: 1, Host: "private target", Redacted: true, Port: 443, Opened: now, Expires: now.Add(5 * time.Minute), Status: api.NetworkHoldOpen}
	var output bytes.Buffer
	duplicate := api.NetworkHoldSnapshot{Version: 1, Revision: 1, Requests: []api.PendingNetworkRequest{request, request}}
	if err := writePendingNetworkSnapshot(&output, duplicate, now); err == nil || output.Len() != 0 {
		t.Fatalf("a partial or duplicate snapshot reached output: %q %v", output.String(), err)
	}
	stopped := api.NetworkHoldSnapshot{Version: 1, Revision: 1, Stopped: true, Requests: []api.PendingNetworkRequest{request}}
	if err := writePendingNetworkSnapshot(&output, stopped, now); err != nil || output.Len() != 0 {
		t.Fatalf("a stopped lifetime emitted open request rows: %q %v", output.String(), err)
	}
}
