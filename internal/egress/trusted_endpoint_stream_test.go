package egress

import (
	"context"
	"net"
	"strings"
	"testing"
)

func TestTrustedEndpointStreamMatchesActualURLAuthorityAndUnboundedPath(t *testing.T) {
	for _, raw := range []string{
		"https://API.example.test/v1/chat/completions",
		"HTTPS://api.example.test/messages#",
		"https://api.example.test:/messages",
		"https://api.example.test:" + strings.Repeat("0", 2048) + "443/base/messages",
		"https://api.example.test/" + strings.Repeat("a", 1<<20) + "/messages",
		"https://[::1]:443/custom/chat/completions",
		"https://api.example.test/a%2fb/messages",
		"https://api.example.test/%E2%98%83/messages",
		"https://api.example.test/messages?", "https://api.example.test/messages#fragment",
		"https://user@api.example.com/messages", "http://api.example.test/messages",
		"https://api.example.test:0/messages", "https://api.example.test:444/messages",
		"https://api.example.test/a//messages", "https://api.example.test/a/../messages",
		"https://api.example.test/%25/messages", "https://api.example.test/%FF/messages",
		"https://api.example.test/other", "https://api.example.test/messages/",
		"https://api.example.test:" + strings.Repeat("0", 2048) + "443:443/messages",
	} {
		url, request, _, legacyErr := judgeEndpoint(raw)
		want := legacyErr == nil && url.Scheme == "https" && request.Port == 443
		for _, chunk := range []int{1, 7, 127, 4096} {
			stream := newTrustedEndpointStreamV1(t.Context())
			var err error
			for at := 0; at < len(raw) && err == nil; at += chunk {
				_, err = stream.Write([]byte(raw[at:min(at+chunk, len(raw))]))
			}
			authority, finishErr := stream.Finish()
			if (err == nil && finishErr == nil) != want {
				t.Fatalf("stream/actual URL mismatch len=%d chunk=%d: write=%v finish=%v legacy=%v", len(raw), chunk, err, finishErr, legacyErr)
			}
			if want && authority != net.JoinHostPort(request.Host, "443") {
				t.Fatal("the streamed endpoint selected another HTTPS authority")
			}
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	stream := newTrustedEndpointStreamV1(ctx)
	if _, err := stream.WriteString("https://api.example.test/messages"); err != context.Canceled {
		t.Fatal("endpoint source cancellation was discarded", err)
	}
}
