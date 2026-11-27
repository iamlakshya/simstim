package egress

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"testing"
)

func TestPhysicalModelOriginFailureDoesNotCountItsLocalRefusalBody(t *testing.T) {
	for _, guestH2 := range []bool{false, true} {
		fixture := newPhysicalModelLifetimeFixture(t, guestH2, true, http.NotFoundHandler())
		fixture.env.srv.dialer.DialTCP = func(context.Context, netip.AddrPort) (net.Conn, error) { return nil, io.ErrUnexpectedEOF }
		request, err := http.NewRequestWithContext(t.Context(), "POST", "https://api.openai.com/v1/responses", strings.NewReader(`{"model":"model-a","input":[]}`))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/json")
		response, err := fixture.client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if err != nil || response.StatusCode != http.StatusBadGateway || len(body) == 0 {
			t.Fatal("local refusal was lost", err)
		}
		waitHTTPState(t, func() bool { return fixture.env.srv.modelUsageSnapshot().Total.Requests == 1 }, "the original failure finaliser")
		usage := fixture.env.srv.modelUsageSnapshot()
		if usage.Total.EstimatedRequests != 1 || usage.Total.TotalTokens != 0 {
			t.Fatal("unwritten request or local refusal became model API body bytes", usage.Total)
		}
	}
}
