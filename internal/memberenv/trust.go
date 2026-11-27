package memberenv

import (
	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/proto"
	"strings"
)

// Trust adds the sandbox public CA paths for this actual agent kind.
func Trust(input []string, kind, caPath, bundlePath string) ([]string, error) {
	values := map[string]string{
		"SSL_CERT_FILE":       bundlePath,
		"REQUESTS_CA_BUNDLE":  bundlePath,
		"GIT_SSL_CAINFO":      bundlePath,
		"NODE_EXTRA_CA_CERTS": caPath,
	}
	if agent, ok := agents.Find(kind); ok {
		for _, key := range agent.CAEnv {
			// A directory trust variable can never be redirected to a file.
			if !trustEnvKey(key) || key == "SSL_CERT_DIR" {
				return nil, &proto.Error{Code: "wall_trust", Message: "the agent has an invalid CA file variable", Hint: "Choose an agent with valid CA file variables."}
			}
			if key != "NODE_EXTRA_CA_CERTS" {
				values[key] = bundlePath
			}
		}
	}
	env := make([]string, 0, len(input)+len(values))
	for _, entry := range input {
		key, _, _ := strings.Cut(entry, "=")
		if _, replaced := values[key]; !replaced {
			env = append(env, entry)
		}
	}
	// Fixed ordering makes the holder's environment stable across starts.
	for _, key := range []string{"SSL_CERT_FILE", "REQUESTS_CA_BUNDLE", "GIT_SSL_CAINFO", "NODE_EXTRA_CA_CERTS"} {
		env = append(env, key+"="+values[key])
		delete(values, key)
	}
	if agent, ok := agents.Find(kind); ok {
		for _, key := range agent.CAEnv {
			if value, exists := values[key]; exists {
				env = append(env, key+"="+value)
				delete(values, key)
			}
		}
	}
	return env, nil
}

func trustEnvKey(key string) bool {
	if key == "" {
		return false
	}
	for i, r := range key {
		if r != '_' && (r < 'A' || r > 'Z') && (i == 0 || r < '0' || r > '9') {
			return false
		}
	}
	return true
}

// Proxy rewrites only the canonical sandbox proxy identity to its actual URI.
func Proxy(env []string, canonical, actual string) []string {
	if actual == "" || actual == canonical {
		return env
	}
	out := make([]string, len(env))
	for i, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if ok && value == canonical {
			out[i] = key + "=" + actual
		} else {
			out[i] = entry
		}
	}
	return out
}
