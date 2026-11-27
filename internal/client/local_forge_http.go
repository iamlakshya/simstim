package client

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/egress"
)

const forgeMetadataMax = 1 << 20

type forgeHTTPStatus struct{ status int }

func (*forgeHTTPStatus) Error() string { return "the exact forge endpoint did not return its metadata" }

// getVisibility is the only value-bearing network operation of LOCALFORGE.
// The peer, method, endpoint and response parser are closed here. The caller
// receives only reduced repo/public metadata; no request/header/response exits.
func (p *localForgePlan) getVisibility(ctx context.Context, endpoint pushForgeRequest, name string, value api.Secret) (pushForgeVisibility, error) {
	expected, err := pushForgeRequestFor(p.intent.repo)
	if err != nil || endpoint != expected {
		return pushForgeVisibility{}, grantPurposeError()
	}
	parsed, err := url.Parse(endpoint.url)
	if err != nil || parsed.Scheme != "https" || parsed.Host != endpoint.host || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return pushForgeVisibility{}, grantPurposeError()
	}
	header := ""
	if name != "" {
		if !p.allowsName(name) {
			return pushForgeVisibility{}, grantPurposeError()
		}
		binding := p.view.Binds[name]
		if binding.Header != "Authorization" {
			return pushForgeVisibility{}, grantPurposeError()
		}
		header, err = egress.FormatBoundCredential(binding, value)
		if err != nil {
			return pushForgeVisibility{}, err
		}
	} else if value != "" {
		return pushForgeVisibility{}, grantPurposeError()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.url, nil)
	if err != nil {
		return pushForgeVisibility{}, forgeVisibilityError()
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", brand.Name)
	if header != "" {
		request.Header.Set("Authorization", header)
	}
	address := net.JoinHostPort(endpoint.host, "443")
	if err := p.checkFresh(ctx, name); err != nil {
		return pushForgeVisibility{}, err
	}
	raw, err := (&net.Dialer{}).DialContext(ctx, "tcp", address)
	if err != nil {
		return pushForgeVisibility{}, err
	}
	defer raw.Close()
	if deadline, known := ctx.Deadline(); known {
		if err := raw.SetDeadline(deadline); err != nil {
			return pushForgeVisibility{}, err
		}
	}
	secure := tls.Client(raw, &tls.Config{ServerName: endpoint.host, MinVersion: tls.VersionTLS12, NextProtos: []string{"http/1.1"}})
	if err := secure.HandshakeContext(ctx); err != nil {
		return pushForgeVisibility{}, err
	}
	connection := &forgeHTTPConnection{Conn: secure, raw: raw, plan: p, ctx: ctx, name: name}
	closed := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { defer close(closed); _ = connection.Close() })
	defer func() {
		if !stop() {
			<-closed
		}
	}()
	defer connection.Close()
	// Request.Write and ReadResponse are the standard HTTP/1 codec. They
	// run synchronously: no transport writer/pump can retain an auth header
	// after this closed capability returns. There is no redirect mechanism.
	if err := request.Write(connection); err != nil {
		return pushForgeVisibility{}, err
	}
	request.Header.Del("Authorization")
	header, value = "", ""
	wire := &forgeResponseReader{source: connection, remaining: 64<<10 + 4096}
	reader := bufio.NewReaderSize(wire, 4096)
	response, err := http.ReadResponse(reader, request)
	if err != nil || wire.read-int64(reader.Buffered()) > 64<<10 {
		return pushForgeVisibility{}, forgeVisibilityError()
	}
	wire.remaining = int64(forgeMetadataMax+64<<10) - wire.read
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return pushForgeVisibility{}, &forgeHTTPStatus{status: response.StatusCode}
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, forgeMetadataMax+1))
	if err != nil || len(body) > forgeMetadataMax {
		clear(body)
		return pushForgeVisibility{}, forgeVisibilityError()
	}
	defer clear(body)
	visibility, err := decodePushForgeVisibility(endpoint, bytes.NewReader(body))
	if err != nil {
		return pushForgeVisibility{}, err
	}
	if err := p.checkFresh(ctx, name); err != nil {
		return pushForgeVisibility{}, err
	}
	return visibility, nil
}

// TLS has already completed. Every actual application write checks the SAME
// intent/Source/tier/receipt/settings under its short OP, covering split auth
// headers too. No lock is held across TCP/TLS startup or response reads.
type forgeHTTPConnection struct {
	net.Conn
	raw  net.Conn
	plan *localForgePlan
	ctx  context.Context
	name string
}

func (c *forgeHTTPConnection) Write(data []byte) (int, error) {
	owned, release, err := c.plan.conn.beginLocalGrant(c.ctx, grantLocalForge, grantOpPush, "read exact forge metadata")
	if err != nil {
		_ = c.raw.Close()
		return 0, err
	}
	defer release()
	if err := c.plan.checkFresh(owned, c.name); err != nil {
		_ = c.raw.Close()
		return 0, err
	}
	n, err := c.Conn.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err != nil {
		err = errors.Join(err, c.raw.Close())
	}
	return n, err
}

func (c *forgeHTTPConnection) Close() error { return c.raw.Close() }

// The bounded IO adapter supplies an ERROR at its boundary, never an
// artificial EOF which could turn a truncated metadata body into success.
type forgeResponseReader struct {
	source          io.Reader
	remaining, read int64
}

func (r *forgeResponseReader) Read(data []byte) (int, error) {
	if r.remaining <= 0 {
		return 0, forgeVisibilityError()
	}
	if int64(len(data)) > r.remaining {
		data = data[:r.remaining]
	}
	n, err := r.source.Read(data)
	r.remaining -= int64(n)
	r.read += int64(n)
	return n, err
}
