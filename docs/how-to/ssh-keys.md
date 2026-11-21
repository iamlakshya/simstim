# Use a named SSH key in a sandbox

Give a sandbox a deploy key for one repository. The supervisor keeps the private
key in memory on the host. The guest can list its public key and ask it to sign.
The private key never enters the guest or its environment.

## Configure the key

Add a secret whose command prints an unencrypted private key. Store the key in
your password manager. Keep its value out of the configuration file.

```toml
[secrets.DEPLOY_KEY]
run = "op read op://development/deploy/private-key"
kind = "ssh-key"

[sandbox]
ssh_keys = ["DEPLOY_KEY"]
```

Every policy layer can select names in `sandbox.ssh_keys`. Lists add up.
A narrower layer removes an inherited name with `!DEPLOY_KEY`.
The local grant gate checks the key's kind, host restriction and tier before
fetching its value for an actual cold start. A host cannot choose another local
secret by returning a name in a session row.

The key grants SSH authentication. It grants no network access. Allow the exact
server and port through the separate network policy.

<!-- test: help (needs a saved host and a session) -->
```sh
simstim policy allow code.example.com:22 --in box/work
```

Use a key that can write to one repository only. The push door cannot inspect
SSH traffic or restrict the branch that an SSH server accepts.

## Change the selection

Select or remove a key for the next sandbox start.

<!-- test: help (needs a saved host, a session and a configured key) -->
```sh
simstim policy allow ssh-key DEPLOY_KEY --in box/work
simstim policy deny ssh-key DEPLOY_KEY --in box/work
```

These commands change names only. They do not fetch a private key or change the
running agent. Start the sandbox again through the client to fetch the selected
keys under a fresh grant. A cold resume first retains the original native
absence and reserves the complete memory cap. It fetches keys only after those
checks succeed. Cancellation joins that preparation before releasing it.

A warm resume starts only the selected agent in the existing sandbox. It keeps
the existing key set. A memory option leaves its current cap unchanged and
shows a warning. A failed warm check never falls back to a cold start.

`show` lists the keys in the actual running agent as `ssh-key`. A different
selection appears as `ssh-key next`, marked for the next start. Saved policy
alone cannot prove that a private key is present.

## Use the agent

The guest endpoint is `/run/simstim/ssh-agent.sock`. Named-key starts set
`SSH_AUTH_SOCK` to that endpoint. They also set `GIT_SSH_COMMAND` to use the
installed guest helper as an OpenSSH proxy command. The helper sends a CONNECT
request to the existing loopback proxy. The same egress rules and address checks
still decide the connection. No extra listening port is opened.

Explicit `sandbox.env` values for `SSH_AUTH_SOCK`, `GIT_SSH_COMMAND` or `GIT_SSH`
take precedence. A custom SSH command must use the guest proxy in a process or
gVisor wall. OpenSSH does not use the HTTP proxy environment variables itself.

Run `ssh-add -L` inside the sandbox to list the named public keys. The endpoint
refuses key uploads, removal, locking and all extensions except the verified
OpenSSH session binding. Each signature records the key fingerprint. When the
SSH client sends a valid session binding, the audit also records the server's
host-key fingerprint. That binding grants no network or host permission.

A sandbox with no named keys has no agent endpoint. Its shutdown closes active
agent connections and removes the endpoint. The laptop's own SSH agent remains
separate. `new --ssh-agent` is restricted to an unwalled session on an `own` host.
