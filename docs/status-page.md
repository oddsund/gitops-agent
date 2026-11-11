# Status page

A failed deploy that appears only as a `journalctl` line is easy to miss
for weeks on a host that you rarely log in to. `internal/statusserver`
serves the same information over HTTP. It uses the standard library and no
framework.

## Endpoints

| Endpoint | What it gives |
|---|---|
| `GET /healthz` | `200 ok` if the last completed cycle had no errors, `503` if it had errors. This one is cheap to poll from a script. |
| `GET /` | A plain HTML page: agent version, uptime, last sync, and the state of each service. No JavaScript, no external resources, readable on a phone. |
| `GET /status.json` | The same data as JSON. |

## What the page shows for an error

A failed deploy or teardown shows the stage, the exit code and "see the
journal". It never shows the output of `docker compose`, because that output
can quote a decrypted value. Run `journalctl -u gitops-agent` for the detail.
A failed sops decrypt works the same way: the page shows a fixed summary such
as `sops decrypt failed; see the journal`, never the sops error text.
The same applies to `/healthz`, `/status.json` and the file on disk.

## Broken services.toml

When `services.toml` cannot be loaded, the page shows a `services.toml:`
line with the error under the sync block, and `/status.json` has a
`manifest_error` field. The field is absent when the file loaded. It does
not change `/healthz`, which stays `200` in both cases below: the cycle
still succeeds. Only the first line of the error is kept, cut to 300
characters.

- If an earlier load worked, the page says the agent keeps the last good
  list of services.
- If no valid list has been loaded since the agent started, the page says
  nothing is deployed, and `/status.json` has `"manifest_no_list": true`.
  `/healthz` is still `200` then, so do not rely on it to catch this.

## The file on disk

The agent also writes the same JSON to
`/var/lib/gitops-agent/status.json` after every cycle. It writes a
temporary file and renames it, so a crash in the middle of a write cannot
corrupt the file.

Thus the last known status survives a restart, and you can read it with
the HTTP server stopped.

## Behind a reverse proxy

`[status].listen_addr` is `127.0.0.1:9090` by default. That is safe for a
bare `go run` or a local test, and it is not reachable from anywhere else.

A reverse proxy in a container usually cannot reach the loopback interface
of the host. If you use one, do one of these:

- Bind `listen_addr` to an interface that the proxy can reach, for example
  `0.0.0.0:9090`. This is fine behind a firewall or on a private network.
- Run the proxy on the host itself.

If you serve the page under a path prefix, for example
`https://host/gitops-agent/` to `http://127.0.0.1:9090/`, strip the prefix
before the request reaches the agent.

The index page has no links, and that is deliberate. A link-free page does
not need to know which prefix, if any, it is served under. The doc comment
on `statusserver.Handler` has the detail.
