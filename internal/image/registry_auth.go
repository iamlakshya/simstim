package image

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

func (r *Registry) bearer(ctx context.Context, challenge string, ref Reference) (string, error) {
	kind, rest, ok := strings.Cut(challenge, " ")
	if !ok || !strings.EqualFold(kind, "Bearer") {
		return "", fmt.Errorf("registry authentication requires a laptop login")
	}
	params := map[string]string{}
	for rest != "" {
		rest = strings.TrimSpace(rest)
		key, value, ok := strings.Cut(rest, "=")
		if !ok || !strings.HasPrefix(value, "\"") {
			return "", badLayer("invalid registry authentication challenge")
		}
		value = value[1:]
		end := strings.IndexByte(value, '"')
		if end < 0 {
			return "", badLayer("invalid registry authentication challenge")
		}
		params[strings.TrimSpace(key)] = value[:end]
		rest = strings.TrimPrefix(value[end+1:], ",")
	}
	u, err := url.Parse(params["realm"])
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
		return "", badLayer("registry token service must use HTTPS")
	}
	q := u.Query()
	q.Set("service", params["service"])
	q.Set("scope", "repository:"+ref.Repository+":pull")
	u.RawQuery = q.Encode()
	method := http.MethodGet
	var body io.Reader
	if r.Auth.IdentityToken != "" {
		method = http.MethodPost
		q.Set("grant_type", "refresh_token")
		q.Set("refresh_token", r.Auth.IdentityToken)
		q.Set("client_id", "image-pull")
		body = strings.NewReader(q.Encode())
		u.RawQuery = ""
	}
	request, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return "", err
	}
	if method == http.MethodPost {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if r.Auth.Username != "" || r.Auth.Password != "" {
		request.SetBasicAuth(r.Auth.Username, r.Auth.Password)
	}
	response, err := r.client().Do(request)
	if err != nil {
		return "", fmt.Errorf("read registry token: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("registry login was refused (HTTP %d)", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, MaxManifest+1))
	if err != nil {
		return "", err
	}
	if int64(len(data)) > MaxManifest {
		return "", badLayer("registry token response exceeds 4 MiB")
	}
	var token struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if json.Unmarshal(data, &token) != nil {
		return "", badLayer("invalid registry token response")
	}
	if token.Token == "" {
		token.Token = token.AccessToken
	}
	if token.Token == "" || strings.ContainsAny(token.Token, "\r\n") {
		return "", badLayer("registry token response has no usable token")
	}
	return token.Token, nil
}
