package client

import (
	"encoding/json"
	"io"
	"net/url"
	"strings"
)

// This metadata contains no secret name or value. The H5 LOCALFORGE
// capability owns credential selection and the ACTUAL HTTPS GET. It checks
// this exact endpoint again at the first write, refuses redirects, places
// the value only in the selected header, parses this response inside the
// capability and returns only this reduced result. No response/request or
// value-bearing header may escape into the caller.
type pushForgeRequest struct{ repo, host, url string }
type pushForgeVisibility struct {
	repo   string
	public bool
}

func pushForgeRequestFor(repo string) (pushForgeRequest, error) {
	host, path, ok := strings.Cut(repo, "/")
	parts := strings.Split(path, "/")
	if !ok || host != "github.com" || len(parts) != 2 {
		return pushForgeRequest{}, &UserError{Msg: "this forge has no reviewed visibility endpoint", Hint: "Use a GitHub remote whose visibility can be checked before granting it.", Code: "grant_public", Kind: KindRefused}
	}
	endpoint := "https://api.github.com/repos/" + url.PathEscape(parts[0]) + "/" + url.PathEscape(parts[1])
	return pushForgeRequest{repo: repo, host: "api.github.com", url: endpoint}, nil
}

// decodePushForgeVisibility is called INSIDE the closed capability. A
// redirect, status other than 200, oversized/incomplete body, omitted private
// flag or another repository must fail there before this result is returned.
func decodePushForgeVisibility(request pushForgeRequest, body io.Reader) (pushForgeVisibility, error) {
	bad := func() (pushForgeVisibility, error) {
		return pushForgeVisibility{}, &UserError{Msg: "the forge did not establish this repository's visibility", Hint: "Check access and the repository name before granting it.", Code: "grant_public", Kind: KindRefused}
	}
	decoder := json.NewDecoder(body)
	open, err := decoder.Token()
	if err != nil || open != json.Delim('{') {
		return bad()
	}
	seen := make(map[string]bool)
	fullName := ""
	var private *bool
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return bad()
		}
		key, ok := token.(string)
		if !ok {
			return bad()
		}
		folded := strings.ToLower(key)
		if seen[folded] {
			return bad()
		}
		seen[folded] = true
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return bad()
		}
		switch folded {
		case "full_name":
			if key != "full_name" || json.Unmarshal(raw, &fullName) != nil {
				clear(raw)
				return bad()
			}
		case "private":
			if key != "private" || json.Unmarshal(raw, &private) != nil {
				clear(raw)
				return bad()
			}
		}
		clear(raw)
	}
	if _, err := decoder.Token(); err != nil {
		return bad()
	}
	if _, err := decoder.Token(); err != io.EOF {
		return bad()
	}
	_, path, _ := strings.Cut(request.repo, "/")
	if private == nil || !strings.EqualFold(fullName, path) {
		return bad()
	}
	return pushForgeVisibility{repo: request.repo, public: !*private}, nil
}
