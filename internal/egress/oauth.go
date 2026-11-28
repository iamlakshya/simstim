package egress

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

const (
	// AuthFormatCodexChatGPT identifies Codex's ChatGPT OAuth bundle (~/.codex/auth.json).
	AuthFormatCodexChatGPT = "codex-chatgpt"

	// oauthRefreshSkew refreshes an OAuth access token when it is within 5
	// minutes of its JWT exp claim.
	oauthRefreshSkew = 5 * time.Minute

	// maxAuthFileBytes bounds reads of auth.json and OAuth token responses.
	maxAuthFileBytes = 64 << 10

	oauthRefreshTimeout = 15 * time.Second
)

// OAuthConfig holds OAuth subscription token state and refresh metadata for a
// bound gateway credential (decision 0059). Every secret field uses api.Secret
// so it is redacted on accidental formatting and wiped in memory on close.
type OAuthConfig struct {
	Format        string     `json:"format"`
	TokenURL      string     `json:"token_url"`
	ClientID      string     `json:"client_id"`
	AccountHeader string     `json:"account_header,omitempty"`
	AccountID     string     `json:"account_id,omitempty"`
	RefreshToken  api.Secret `json:"refresh_token,omitempty"`
	IDToken       api.Secret `json:"id_token,omitempty"`
	ExpiresAt     int64      `json:"expires_at,omitempty"`
	// AuthFile points to the host user's pre-existing agent login file (such as
	// ~/.codex/auth.json) ONLY when the token was loaded from the host. It is
	// always empty for laptop-pushed secrets, which stay strictly in memory (0032).
	AuthFile string `json:"auth_file,omitempty"`
	// LockFile is the sibling advisory lock file used with AuthFile so parallel
	// sessions on the host never race on OpenAI's rotating refresh tokens.
	LockFile string `json:"lock_file,omitempty"`
}

// OAuthTokens is the parsed token bundle from a Codex ChatGPT auth.json file.
type OAuthTokens struct {
	AccessToken  api.Secret
	RefreshToken api.Secret
	IDToken      api.Secret
	AccountID    string
	ExpiresAt    int64
}

// Clear wipes every secret field in t.
func (t *OAuthTokens) Clear() {
	t.AccessToken = ""
	t.RefreshToken = ""
	t.IDToken = ""
}

type codexAuthFile struct {
	AuthMode     string `json:"auth_mode"`
	OpenAIAPIKey string `json:"OPENAI_API_KEY"`
	Tokens       *struct {
		IDToken      string `json:"id_token"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		AccountID    string `json:"account_id"`
	} `json:"tokens"`
}

// ParseCodexAuthJSON parses Codex's ChatGPT auth.json format and returns its
// access/refresh/id tokens, ChatGPT account ID and JWT expiry timestamp.
func ParseCodexAuthJSON(raw []byte) (OAuthTokens, error) {
	var doc codexAuthFile
	if err := json.Unmarshal(raw, &doc); err != nil {
		return OAuthTokens{}, fmt.Errorf("parse Codex auth JSON: %w. Try: sign in again with codex login --device-auth", err)
	}
	if doc.AuthMode != "" && !strings.EqualFold(doc.AuthMode, "chatgpt") {
		return OAuthTokens{}, fmt.Errorf("codex auth JSON has auth_mode %q, not \"chatgpt\". Try: use OPENAI_API_KEY for API keys or run codex login --device-auth for ChatGPT", doc.AuthMode)
	}
	if doc.Tokens == nil || strings.TrimSpace(doc.Tokens.AccessToken) == "" {
		return OAuthTokens{}, errors.New("codex auth JSON has no ChatGPT access_token. Try: sign in with codex login --device-auth")
	}
	acc := strings.TrimSpace(doc.Tokens.AccessToken)
	ref := strings.TrimSpace(doc.Tokens.RefreshToken)
	idt := strings.TrimSpace(doc.Tokens.IDToken)
	acctID := strings.TrimSpace(doc.Tokens.AccountID)
	if acctID == "" {
		acctID = ParseJWTAccountID(idt)
	}
	if acctID == "" {
		acctID = ParseJWTAccountID(acc)
	}
	return OAuthTokens{
		AccessToken:  api.Secret(acc),
		RefreshToken: api.Secret(ref),
		IDToken:      api.Secret(idt),
		AccountID:    acctID,
		ExpiresAt:    ParseJWTExpiry(acc),
	}, nil
}

// ReadCodexAuthFile safely reads a pre-existing regular non-symlink auth.json
// file under os.OpenRoot (bounded to 64 KiB) and parses its ChatGPT tokens.
func ReadCodexAuthFile(path string) (OAuthTokens, error) {
	raw, err := readRegularFileBounded(path, maxAuthFileBytes)
	if err != nil {
		return OAuthTokens{}, err
	}
	defer clear(raw)
	return ParseCodexAuthJSON(raw)
}

func readRegularFileBounded(path string, limit int64) ([]byte, error) {
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", dir, err)
	}
	defer func() { _ = root.Close() }()
	fi, err := root.Lstat(base)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", path, err)
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	if fi.Size() > limit {
		return nil, fmt.Errorf("%s exceeds %d bytes", path, limit)
	}
	f, err := root.Open(base)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if int64(len(data)) > limit {
		clear(data)
		return nil, fmt.Errorf("%s exceeds %d bytes", path, limit)
	}
	return data, nil
}

// ParseJWTExpiry extracts the numeric "exp" claim (Unix seconds) from a JWT's
// payload segment without verifying its signature. Returns 0 when absent or invalid.
func ParseJWTExpiry(jwt string) int64 {
	claims, ok := decodeJWTPayload(jwt)
	if !ok {
		return 0
	}
	switch v := claims["exp"].(type) {
	case float64:
		if v > 0 {
			return int64(v)
		}
	case json.Number:
		if n, err := v.Int64(); err == nil && n > 0 {
			return n
		}
	}
	return 0
}

// ParseJWTAccountID extracts https://api.openai.com/auth.chatgpt_account_id
// from a ChatGPT ID or access JWT payload, or "" when absent.
func ParseJWTAccountID(jwt string) string {
	claims, ok := decodeJWTPayload(jwt)
	if !ok {
		return ""
	}
	authObj, ok := claims["https://api.openai.com/auth"].(map[string]any)
	if !ok {
		return ""
	}
	id, _ := authObj["chatgpt_account_id"].(string)
	return strings.TrimSpace(id)
}

func decodeJWTPayload(jwt string) (map[string]any, bool) {
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 || parts[1] == "" {
		return nil, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, false
	}
	var claims map[string]any
	if err := json.Unmarshal(raw, &claims); err != nil {
		return nil, false
	}
	return claims, true
}

func writeCodexAuthFileAtomic(path string, tokens OAuthTokens) error {
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	root, err := os.OpenRoot(dir)
	if err != nil {
		return fmt.Errorf("open auth directory: %w", err)
	}
	defer func() { _ = root.Close() }()
	fi, err := root.Lstat(base)
	if err != nil {
		// Never create a new secret file on disk if the user logged out or deleted it.
		return fmt.Errorf("auth file removed: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return errors.New("auth file is not a regular file")
	}

	existing := map[string]any{}
	if raw, rerr := readRegularFileBounded(path, maxAuthFileBytes); rerr == nil {
		_ = json.Unmarshal(raw, &existing)
		clear(raw)
	}
	existing["auth_mode"] = "chatgpt"
	if _, hasKey := existing["OPENAI_API_KEY"]; !hasKey {
		existing["OPENAI_API_KEY"] = nil
	}
	tokObj, _ := existing["tokens"].(map[string]any)
	if tokObj == nil {
		tokObj = map[string]any{}
	}
	tokObj["access_token"] = string(tokens.AccessToken)
	if tokens.RefreshToken != "" {
		tokObj["refresh_token"] = string(tokens.RefreshToken)
	}
	if tokens.IDToken != "" {
		tokObj["id_token"] = string(tokens.IDToken)
	}
	if tokens.AccountID != "" {
		tokObj["account_id"] = tokens.AccountID
	}
	existing["tokens"] = tokObj
	existing["last_refresh"] = time.Now().UTC().Format(time.RFC3339Nano)

	out, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		return fmt.Errorf("encode auth JSON: %w", err)
	}
	defer clear(out)
	out = append(out, '\n')

	tmp := "." + base + ".tmp." + randomHex(6)
	if err := root.WriteFile(tmp, out, 0o600); err != nil {
		return fmt.Errorf("write temporary auth file: %w", err)
	}
	if err := root.Rename(tmp, base); err != nil {
		_ = root.Remove(tmp)
		return fmt.Errorf("replace auth file: %w", err)
	}
	return nil
}
