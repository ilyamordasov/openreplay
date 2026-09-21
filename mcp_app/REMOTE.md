# OpenReplay Remote MCP

This deployment exposes the existing OpenReplay MCP app through the MCP
Streamable HTTP transport.

## Endpoint

The production endpoint is intended to be exposed at:

```
https://<openreplay-host>/mcp
```

The Kubernetes ingress must use `pathType: Exact` for `/mcp`. OpenReplay
already owns the browser route `/mcp/authorize`; using a Prefix ingress would
steal that route from the frontend.

## Security model

Remote MCP is **single-owner** and runs with exactly one replica.

Every request to `/mcp` requires:

```
Authorization: Bearer <MCP_ACCESS_TOKEN>
```

The access token is independent from the OpenReplay JWT. It protects the MCP
surface itself. OpenReplay authentication is still performed by the MCP tools
(`login_email_password` or `login_jwt`) and is persisted by the app in
`~/.openreplay-mcp/config.json`.

For a remote deployment set:

- `OPENREPLAY_URL=https://<openreplay-host>`
- `MCP_LOCK_APP_URL=1`
- `MCP_ACCESS_TOKEN=<random secret, at least 32 characters>`

Locking the app URL prevents an authenticated MCP client from repointing the
server to an arbitrary HTTPS backend.

Because the current MCP app uses one process-wide authentication/cache state,
do not scale this deployment above one replica and do not share its access
token between unrelated users.

## Health

`GET /healthz` is intentionally unauthenticated for Kubernetes probes. It
should not be exposed by the public ingress.

## Local remote-mode run

```bash
export OPENREPLAY_URL=https://openreplay.example.com
export MCP_LOCK_APP_URL=1
export MCP_ACCESS_TOKEN="$(openssl rand -hex 32)"
npm run build
npm run serve:remote
```

Then connect a Streamable HTTP MCP client to `http://127.0.0.1:3000/mcp`
with the bearer token above.
