# Use with coding agents

Tell a coding agent, such as Claude Code or Codex, how your project uses simstim. Paste a short snippet into the project's `AGENTS.md`, and point the agent at the docs in Markdown.

## Add the snippet to `AGENTS.md`

Copy these lines into the `AGENTS.md` at the root of your project. Make the file if it does not exist.

```markdown
## Remote sessions

This project runs coding agents in simstim sessions on remote hosts.

- List the sessions: `simstim ls --json`
- List the hosts: `simstim host ls --json`
- Help for a command: `simstim <command> --help`
- Docs for agents: https://simstim.astrel.sh/llms.txt
```

The commands in the snippet start, stop and delete no session. `host ls` reads only your own config, and connects to no host. `ls` connects to each host. It leaves a stopped host that `host create` made as it is. But a host that you added with a [provider script](../../examples/providers/README.md) and [`wake_on_attach`](../reference/config.md#hoststoml) set starts through its script when it does not answer, as it does when you attach. A cloud host costs money while it runs.

## Where each agent reads it

`AGENTS.md` is plain Markdown at the root of a repository. [More than 20 tools](https://agents.md/) read it. Some read it only after a setting.

| Agent | What it reads |
| --- | --- |
| Codex | `AGENTS.md` in each folder from the project's root down to the folder it works in ([Codex](https://developers.openai.com/codex/guides/agents-md)) |
| Claude Code | `AGENTS.md` when the folder and its parents have no `CLAUDE.md`. If the project has a `CLAUDE.md`, add the line `@AGENTS.md` to it, and Claude Code reads both ([Claude Code](https://code.claude.com/docs/en/memory)) |
| Gemini CLI | `GEMINI.md`, unless you set it. Name `AGENTS.md` in `context.fileName` in `.gemini/settings.json`, or add the line `@./AGENTS.md` to `GEMINI.md` ([Gemini CLI](https://geminicli.com/docs/cli/gemini-md/#customize-the-context-file-name)) |
| Aider | Only the files that you give it. Add the line `read: AGENTS.md` to `.aider.conf.yml` ([Aider](https://aider.chat/docs/usage/conventions.html#always-load-conventions)) |
| Cursor, Zed and others | `AGENTS.md` |

For Gemini CLI, this `.gemini/settings.json` reads both files:

```json
{
  "context": {
    "fileName": ["AGENTS.md", "GEMINI.md"]
  }
}
```

## The docs in Markdown

The docs site publishes a copy of its pages for agents, as [llmstxt.org](https://llmstxt.org/) proposes:

- **`llms.txt`**, at the root of the site, lists every page with a one-line summary. The pages about how the project is built are under "Optional", so an agent can skip them.
- **A Markdown copy of each page.** Its address is the page's address without the trailing slash, plus `.md`. The copy of `/how-to/add-a-host/` is `/how-to/add-a-host.md`, and the copy of `/` is `/index.md`. Its links go to the other copies.
- **A link to the copy** in the head of each page: `<link rel="alternate" type="text/markdown">`.

Each version of the docs has its own `llms.txt`, such as `/v1.2/llms.txt`.

`simstim <command> --help` prints the command's description, usage, examples and flags. Its page in the [reference](../reference/cli/_index.md) shows the same, in another layout. An agent that has the client can read the help with no network.
