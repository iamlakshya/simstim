package guest

import (
	"maps"

	"github.com/iamlakshya/simstim/internal/agents"
)

// ProxyEnv returns the proxy environment variables for a walled session
// pointing at proxyURL (for example "http://127.0.0.1:3128"). It sets the
// standard upper- and lower-case proxy variables, NO_PROXY for loopback,
// NODE_USE_ENV_PROXY=1 for Node's fetch (P1.7), and any extra agent ProxyEnv.
func ProxyEnv(proxyURL string, agent agents.Agent) map[string]string {
	env := map[string]string{
		"HTTP_PROXY":         proxyURL,
		"HTTPS_PROXY":        proxyURL,
		"ALL_PROXY":          proxyURL,
		"http_proxy":         proxyURL,
		"https_proxy":        proxyURL,
		"all_proxy":          proxyURL,
		"NO_PROXY":           "localhost,127.0.0.1,::1",
		"no_proxy":           "localhost,127.0.0.1,::1",
		"NODE_USE_ENV_PROXY": "1",
	}
	maps.Copy(env, agent.ProxyEnv)
	return env
}
