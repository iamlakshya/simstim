# Open a web server that runs in a session

When an agent starts a dev server on the host, forward its port to your laptop:

<!-- test: help (needs a session with a server) -->
```sh
simstim ports my-server/app
simstim forward my-server/app 3000
```

`ports` lists the TCP ports the session's processes listen on. `forward` makes each port reachable at `127.0.0.1` on your laptop, through an SSH connection of its own, until you press Ctrl+C. With no port, it forwards all of them. Write `8080:3000` to use another local port.

Nothing is opened to the network: the port is bound to `127.0.0.1` on your laptop only, and nothing new listens on the host.

## Walled sessions

For a walled session, no TCP port opens on the host:

- `ports` marks guest-reported listeners as `[claimed]` and ports currently exposed by the supervisor as `[granted]`.
- `forward` asks the session's supervisor to grant each named port on a private mode-0600 Unix socket on the host (`$XDG_RUNTIME_DIR/simstim/p/<id>/<port>.sock`), then forwards `127.0.0.1:<local>` to that socket with `ssh -L`. Each connection travels over a `port` stream to the guest's loopback address. When `forward` exits, it revokes the grants it opened and removes the Unix socket.
- If you run `forward` with no port on a walled session, it lists the claimed ports and asks which to forward. With input off or without a terminal, it forwards none.
- The first forward of a walled session prints a browser note: `localhost` shares cookies with other local apps across ports, so use `http://<id>.localhost:<port>` for a separate origin.

`forward` refuses to start when your SSH config has a `RemoteForward` or a `Tunnel` for the host, because either one lets the host reach your laptop. It also refuses `ForkAfterAuthentication yes`, which would keep the forward running after you press Ctrl+C. The error names the line. Remove it from the host's block in your config, then run `forward` again.
