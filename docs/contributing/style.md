# Style

## Code

- **The product name lives in `internal/brand`.** Use `brand.Name`, `brand.HostBinary` and `brand.Env(...)`; never spell the name in code, paths, environment variables or unit names. `just check-name` checks.
- **Byte-exact while attached.** Nothing may change the bytes between an agent's terminal and the user's, except the two keys the input filter owns. A change near the attach path needs a test that proves the bytes are unchanged.
- **Agents are data**, in `internal/agents`. No other code branches on an agent's name.
- **Errors say what to try next.** A user-facing error is a `client.UserError` with a message, a hint, a `Code` that names it, such as `host_not_found`, and a `Kind` that sets its exit code ([exit codes](../reference/exit-codes.md)). A test fails on a `UserError` without a `Code` or a hint. Wrap other errors with context: `fmt.Errorf("read %s: %w", path, err)`. The client prints those with the hint to make a debug bundle.
- **A question reads through `answer`** (`internal/cli/answer.go`), so that a signal or the end of the context always ends it, and a cancel that comes with the answer wins. A test fails on a direct read of `os.Stdin` anywhere else in `internal/cli`.
- **Every exported name has a doc comment.**
- **Small files and functions:** a file over 500 lines, or a function that is hard to follow, is split.
- **No new listening ports, relays or telemetry.**
- `just lint` runs `go vet`, golangci-lint (`.golangci.yml`), and the name, private-detail and script checks.

## Writing

The docs, the CLI's messages and the commit messages share one style:

- **Plain English, short sentences.** One idea per sentence. Prefer the short, common word over the long one.
- **British spelling:** colour, behaviour, licence (noun).
- **Say what to do,** in the active voice: "Run `just docs`", not "`just docs` should be run".
- **Name things as users see them.** A setting is a key in `config.toml`; a screen is what the user sees.
- **Be exact.** Numbers with units, versions, and links to sources.
- `just docs-check` checks spelling and some of the wording.

## Errors

An error prints on stderr in one form: `Error:` and the message, a blank line, then one `Try:` line.

```text
Error: no host named "bx".

Try: simstim host add bx
```

- **The message** names the bad input and says what went wrong, in one or two sentences. It starts in lower case, unless its first word is a name. The client adds the full stop.
- **The hint** is the text of the `Try:` line, and every error has exactly one. It follows these rules:
  1. It is a full command that runs as written, or one imperative sentence that ends with a full stop.
  2. A command starts with the product's name, and uses the real names from the error, such as `box`, not `<host>`. A placeholder in angle brackets appears only when the value is unknown.
  3. A command may end with a short note in brackets, one space after it, such as `simstim host add devbox (updates the helper)`.
  4. Two commands may join with ", or" when either one fixes the error.
  5. It is never a question or a fragment.
  6. A command that repeats a word the user typed quotes it for the shell. A name that no host could have, such as one with a space, a `;` or a leading dash, is not repeated.
- **The kind** says what went wrong, not how: `KindUsage` for a wrong command line or config file, `KindNotFound` for a name that does not exist, `KindNeedsAnswer` for a question that nobody can answer, `KindRefused` for a policy or a tier that says no, `KindCancelled` for a cancel, and `KindFailed` for everything else. The exit code comes from the kind, never from the text. One code has one kind everywhere.

## Commits

One logical change per commit. The message is a subject (the change, in the imperative, under about 70 characters), a blank line, and a body that says why. No trailers of any kind, and never `git commit -s` ([0010](decisions/0010-no-commit-trailers.md)).
