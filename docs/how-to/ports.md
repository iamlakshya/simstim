# Open a web server that runs in a session

When an agent starts a dev server, choose its port and forward it to your laptop:

<!-- test: help (needs a session with a server) -->
```sh
simstim ports my-server/app
simstim forward my-server/app 3000
```

`ports` marks the sandbox's listening ports as claimed. It also shows the granted ports and reverse forwards. The launcher's selected session shows its live forwards.

For a sandbox, `forward` grants exactly the guest port that you name. The supervisor opens a private Unix socket on the host, with mode 0600 in a folder with mode 0700. SSH forwards your laptop's `127.0.0.1` to that socket. Each connection reaches only the guest's `127.0.0.1` and the named port. No TCP port opens on the host.

Write `8080:3000` to choose another laptop port. With no port, a sandbox lists its claimed ports and asks you to choose one. With input off, name a port on the command line. A guest claim never chooses a laptop port.

Pages from a sandbox at localhost share cookies with other local apps by port. Use a separate browser profile, or the session's ID in a URL such as `http://<id>.localhost:3000`.

Press Ctrl+C to end the SSH forward. Its session policy grant stays until you revoke it. These commands grant or deny one exact session port:

```sh
simstim policy allow port 3000 --in my-server/app
simstim policy deny port 3000 --in my-server/app
```

A deny closes the socket and its existing connections. `--for 1h` on an allowance ends it after an hour. Port rules require `--in` and do not take `--once`.

To let a sandbox reach one laptop service, use a reverse forward:

```sh
simstim forward my-server/app --reverse 5432
simstim forward my-server/app --reverse 5432:6543
```

The first makes the laptop's `127.0.0.1:5432` available at the guest's `127.0.0.1:5432`. The second uses guest port 6543. Reverse forwarding needs an `own` host and SSH. The guest cannot choose another laptop address or port. Ctrl+C closes the guest listener and its connections. Each connection has an audit line. Nothing listens on the host.

An unwalled session keeps the older behaviour: with no port, `forward` forwards all of its listening ports.

`forward` refuses an SSH config with `RemoteForward`, `Tunnel` or `ForkAfterAuthentication yes`. The error names the line. Remove it from the host's block, then run `forward` again.
