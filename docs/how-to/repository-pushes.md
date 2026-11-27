# Repository push boundaries

A push grant names one exact repository, such as `github.com/org/repo`. It opens only HTTPS discovery for `git-receive-pack` and the matching update request. It gives no general network access to that host. Network denies, locks and address checks still apply.

Discovery checks the current grant each time. It does not reserve or spend a push-once grant. The update request checks its complete command prefix before reserving that grant. The original connection permit spends it when the checked origin connection opens. Removing or expiring the grant also revokes its active request.

Every observed `git-receive-pack` request needs push authority, even when a broad network grant already permits the host. Each update must name a branch below `refs/heads/<name>/<session-id>/`. Deletes, tags and other branches are refused. The command prefix has the existing 64 MiB inspection limit. The pack data follows unchanged. Signed requests require the supported PGP certificate framing; the receiving forge verifies the signature.

The laptop checks repository visibility with its local credential before allowing a push grant. Public mode records an explicit local `--public` acknowledgement. An empty mode records no acknowledgement. A host's saved rule or metadata cannot supply that local consent. Visibility checks and the original local source are checked again before the grant is sent.

Known code hosts and repositories with a push grant use the existing HTTP interceptor. Other allowed TLS flows still splice after the SNI check. The proxy cannot inspect Git inside those opaque flows. A splice-only host cannot use a repository push grant.
