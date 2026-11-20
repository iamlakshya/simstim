# Open a web server that runs in a session

When an agent starts a dev server on the host, forward its port to your laptop:

<!-- test: help (needs a session with a server) -->
```sh
simstim ports my-server/app
simstim forward my-server/app 3000
```

`ports` lists the TCP ports the session's processes listen on. `forward` makes each port reachable at `127.0.0.1` on your laptop, through an SSH connection of its own, until you press Ctrl+C. With no port, it forwards all of them. Write `8080:3000` to use another local port.

Nothing is opened to the network: the port is bound to `127.0.0.1` on your laptop only, and nothing new listens on the host.

`forward` refuses to start when your SSH config has a `RemoteForward` or a `Tunnel` for the host, because either one lets the host reach your laptop. It also refuses `ForkAfterAuthentication yes`, which would keep the forward running after you press Ctrl+C. The error names the line. Remove it from the host's block in your config, then run `forward` again.
