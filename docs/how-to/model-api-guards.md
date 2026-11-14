# Model API guards

A sandbox checks model requests before it sends their headers or body to a provider. The agent table names each supported protocol, path and tool type. The guard also applies when a shell calls a known model host.

Local tools pass. A new server tool type fails closed until its agent row is reviewed. Listed web search types pass under `balanced` and `open`. The `locked` preset requires a session grant for them.

Provider MCP connections, content fetched by URL and file uploads need separate capabilities. A local file ID and a `data:` URL pass the content check. The guard checks nested messages and tool results too. Batch paths remain closed.

Allow one capability for a session:

```sh
simstim policy allow api.anthropic.com --tools web_fetch --in box/session
```

The capability names are:

| Name | Opens |
| --- | --- |
| `web_fetch` | Anthropic web fetch tools |
| `web_search` | Listed search tools under `locked` |
| `code_execution` | Anthropic code execution and OpenAI code interpreter |
| `mcp` | Provider MCP servers and toolsets |
| `url_content` | Provider content fetched from an HTTP or HTTPS URL |
| `files` | `POST /v1/files` uploads |

Use an exact model host. These grants cannot use a wildcard, an address, a path or a config file layer. A plain host allowance does not open any capability. `file_search` and unknown tool types remain closed.

`--once` grants one network flow. All checks on that flow use the same permit. A WebSocket keeps its permit for the connection, including later messages. A refused HTTP body cannot spend its conditional tool grant before an origin connection exists. A fresh grant has fresh state. Removing a required grant or reaching its deadline closes the affected live origin.

An HTTP refusal has status 403, the provider's error shape and `x-should-retry: false`. Its message names the scoped grant to use when that capability can be opened. Each refused model request enters the network audit and the existing blocked alert path.

Model WebSockets use the same frame and secret checks as bound connections. They negotiate no compression. Each complete `response.create` message is checked before any of its bytes reach the origin. A refused message closes with code 1008 and a UTF-8 reason of at most 123 bytes.

The initial path and tool lists are documented baselines. Agent traffic recordings and the agent's response to a policy close remain release checks. The implementation does not claim that an SDK retries, falls back to HTTPS or has been exercised against a real provider.
