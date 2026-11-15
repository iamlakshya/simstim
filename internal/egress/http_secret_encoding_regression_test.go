package egress

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestBoundResponseMultipleCodingFieldsCannotHideAnEcho(t *testing.T) {
	encoded := encodedBodyFixture(t, "gzip", strings.NewReader("echo|"+secretRouteReal+"|kept"))
	f := newSecretRouteFixture(t, 2, 2, secretRouteArgs(t, secretRoutePolicy(t)), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header()["Content-Encoding"] = []string{"identity", "gzip"}
		_, _ = w.Write(encoded)
	}))
	response, err := f.client.Get("https://api.example.com/allowed/echo")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != 200 || response.Header.Get("Content-Encoding") != "" || strings.Contains(string(body), secretRouteReal) || string(body) != "echo|"+f.sentinel()+"|kept" {
		t.Fatal("a multi-line coding hid real response bytes from the existing secret filter")
	}
}
