package alerts

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
)

// Read returns the config in the file at path, and whether the file exists.
func Read(path string) (api.AlertsConfig, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return api.AlertsConfig{}, false, nil
	}
	if err != nil {
		return api.AlertsConfig{}, true, fmt.Errorf("read %s: %w", path, err)
	}
	c, err := Parse(data)
	return c, true, err
}

// Load reads, checks and fills in the file at path. A missing file means
// alerts are off: it returns nil and no error.
func Load(path string) (*Settings, error) {
	c, exists, err := Read(path)
	if err != nil || !exists {
		return nil, err
	}
	if err := checkPrivate(path); err != nil {
		return nil, err
	}
	s, err := Check(c)
	if err != nil {
		return nil, err
	}
	return s, checkTokenFiles(s)
}

func checkTokenFiles(s *Settings) error {
	for _, d := range s.Destinations {
		if d.TokenFile == "" {
			continue
		}
		if _, err := readToken(d.TokenFile); err != nil {
			return err
		}
	}
	return nil
}

// readToken reads a token file, which only its owner may read.
func readToken(path string) (string, error) {
	path = expandHome(path)
	if err := checkPrivate(path); err != nil {
		return "", err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", &Error{Msg: "cannot read the token file: " + err.Error(), Hint: "Check that " + path + " exists."}
	}
	tok := strings.TrimSpace(string(b))
	if tok == "" || strings.ContainsAny(tok, "\r\n\x00") {
		return "", &Error{Msg: "the token file " + path + " is empty or has more than one line", Hint: "Put only the token in it."}
	}
	return tok, nil
}

// Save checks c and writes it to path, mode 0600, so that a reader sees the
// old file or the new one and never half of one.
func Save(path string, c api.AlertsConfig) error {
	s, err := Check(c)
	if err != nil {
		return err
	}
	if err := checkTokenFiles(s); err != nil {
		return err
	}
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "# Alerts for %s sessions on this host: which events to send, and where.\n", brand.DisplayName)
	fmt.Fprintf(&buf, "# Written by `%s host alerts set`. You may edit it by hand; keep it mode 0600.\n\n", brand.Name)
	if err := toml.NewEncoder(&buf).Encode(c); err != nil {
		return fmt.Errorf("encode alerts.toml: %w", err)
	}
	return writeFile(path, buf.Bytes())
}

func writeFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("make %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".alerts-*")
	if err != nil {
		return fmt.Errorf("write alerts.toml: %w", err)
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("write alerts.toml: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write alerts.toml: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("write alerts.toml: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write alerts.toml: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("write alerts.toml: %w", err)
	}
	return nil
}

// Merge adds add's destinations to base, and takes each setting add sets.
func Merge(base, add api.AlertsConfig) api.AlertsConfig {
	out := base
	if len(add.Events) > 0 {
		out.Events = add.Events
	}
	if add.Delay != "" {
		out.Delay = add.Delay
	}
	if add.MinTurn != "" {
		out.MinTurn = add.MinTurn
	}
	if add.Detail != nil {
		out.Detail = add.Detail
	}
	if add.QuietWhenTyping != nil {
		out.QuietWhenTyping = add.QuietWhenTyping
	}
	out.Destinations = append(append([]api.AlertDestination(nil), base.Destinations...), add.Destinations...)
	return out
}

// Hidden stands in for a secret in a redacted config.
const Hidden = "(hidden)"

// Redact hides every secret in c: tokens, header values, and the path and
// query of each address, which for ntfy topics and chat webhooks are the
// secret. Commands are kept, so keep secrets out of them.
func Redact(c api.AlertsConfig) api.AlertsConfig {
	out := c
	out.Destinations = make([]api.AlertDestination, len(c.Destinations))
	for i, d := range c.Destinations {
		if d.Token != "" {
			d.Token = Hidden
		}
		if d.URL != "" {
			d.URL = RedactURL(d.URL)
		}
		if len(d.Headers) > 0 {
			h := make(map[string]string, len(d.Headers))
			for k := range d.Headers {
				h[k] = Hidden
			}
			d.Headers = h
		}
		out.Destinations[i] = d
	}
	return out
}

// RedactURL keeps an address's scheme and host, and hides the rest.
func RedactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return Hidden
	}
	s := u.Scheme + "://" + u.Host
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" {
		s += "/…"
	}
	return s
}

// WithDefaults fills in each setting c leaves out, so a reader sees what is
// in effect.
func WithDefaults(c api.AlertsConfig) api.AlertsConfig {
	if len(c.Events) == 0 {
		c.Events = append([]string(nil), DefaultEvents...)
	}
	if c.Delay == "" {
		c.Delay = DefaultDelay.String()
	}
	if c.MinTurn == "" {
		c.MinTurn = DefaultMinTurn.String()
	}
	if c.Detail == nil {
		off := false
		c.Detail = &off
	}
	if c.QuietWhenTyping == nil {
		on := true
		c.QuietWhenTyping = &on
	}
	return c
}
