# gitops-agent

A small daemon that keeps a host's docker compose services in sync with a
git repo. Point it at a repo containing a `services.toml` manifest and a
`services/<name>/compose.yml` per service, and it polls the repo, decrypts
each service's secrets, and runs `docker compose up -d` for whatever
changed. Disable or delete a service and it gets torn down on the next
cycle. No agent-side API, no cluster, no CRDs -- just a git repo, compose
files, and a daemon that reconciles the two.

It was built to run a handful of self-hosted services on a single
always-on host (a Raspberry Pi, in the original deployment) from a git
repo, without needing to SSH in every time something changes.

## How it works

Each cycle:

1. **Sync.** `internal/gitsync` fetches the repo and hard-resets the local
   clone to `origin/<branch>`. This is a hard reset, not a merge: the
   remote is always the source of truth, so a stray local edit, a local
   commit, or a force-pushed remote history is discarded rather than
   fought with. A `git pull` that fails to merge would otherwise wedge the
   reconcile loop forever, logging the same error every cycle without
   deploying anything; a hard reset always makes progress. Untracked files
   in the clone are preserved across the reset (they're snapshotted first
   and restored if the reset removes them) in case anything ends up there
   that isn't meant to be wiped.

2. **Reload the manifest.** `services.toml` is re-read from the freshly
   synced clone on every cycle, not just at startup -- enabling a service
   is a commit, not an SSH session. If the manifest fails to parse (bad
   TOML, a duplicate service name), the error is logged and the agent
   keeps deploying the last-known-good list; a follow-up commit that fixes
   it is picked up next cycle.

3. **Tear down.** Before deploying anything, the agent checks every
   service it has previously deployed against the current manifest. Any
   service that's no longer `enabled = true` -- because it was flipped to
   `false`, or its `[[services]]` block was deleted entirely -- gets
   `docker compose down --remove-orphans` and is dropped from the agent's
   state. This runs before the deploy pass in the same cycle, so a service
   renamed in one commit (removed under the old name, added under a new
   one, same ports) doesn't collide with itself.

4. **Decide what needs deploying.** A service is deployed when: its
   directory changed between the previous and current commit (compared via
   git's tree hashes for that directory -- a single hash comparison, not a
   file walk), or it hasn't been deployed since the agent started, or it's
   time for a periodic full reconcile. The full reconcile exists because
   drift isn't visible in git: stop a container by hand and no commit
   looks any different. Every `full_reconcile_interval_seconds` (default
   3600), every enabled service gets deployed regardless of whether
   anything changed. If the tree-hash comparison itself can't be done
   (unknown commit, unreadable tree), the agent deploys anyway --  a
   redundant `docker compose up` is cheap and idempotent, a skipped one
   leaves the host silently wrong.

5. **Decrypt.** For each service being deployed, `internal/sopsdecrypt`
   decrypts `<service>/secrets.enc.env`, if present, to
   `/run/gitops-agent/<service>/secrets.env` -- tmpfs, not the git
   checkout. A service with no `secrets.enc.env` is a no-op here, not an
   error.

6. **Deploy.** `docker compose -f <service>/compose.yml up -d
   --remove-orphans`, via `internal/deploy`. One service's failure is
   logged and does not stop the others; the agent tries again next cycle.

7. **Persist state and status.** Which services are currently deployed,
   and from where, is written to a small JSON file (`internal/state`) so a
   deleted `[[services]]` entry can still be torn down later -- the
   manifest no longer has its path, but the agent remembers it. A status
   snapshot (`internal/statusserver`) is written to disk and served over
   HTTP.

### Polling cadence

Commits tend to arrive in bursts: you push a change, notice something's
off, push a fix. A fixed five-minute poll makes that second push cost five
minutes of waiting. So the cadence is adaptive (`internal/schedule`):

- Normally, poll every `pull_interval_seconds` (default 300), with up to
  ±10% jitter so the agent doesn't hit GitHub on a metronome.
- If the last poll found new commits, switch to
  `active_interval_seconds` (default 15) and stay there until
  `active_window_seconds` (default 900) have passed with nothing new. Then
  decay back to the idle cadence.

Only the cadence *transition* is logged, not every tick -- at the active
interval that would otherwise be one line every 15 seconds in the
journal.

Send `SIGHUP` to skip the rest of the current wait and reconcile
immediately (`kill -HUP $(pidof gitops-agent)`, or `systemctl kill -s HUP
gitops-agent` under systemd).

`SIGTERM`/`SIGINT` trigger a clean shutdown: the agent stops starting new
cycles and exits once the current wait ends, rather than being killed
mid-deploy.

## Install

```
go install github.com/oddsund/gitops-agent/cmd/gitops-agent@latest
```

Or download a prebuilt binary from [GitHub
Releases](https://github.com/oddsund/gitops-agent/releases). Releases are
currently built for `linux/arm64` only (the target is a Raspberry Pi 4;
open an issue if you need another platform). Each release publishes the
binary alongside a `.sha256` checksum file and a [GitHub artifact
attestation](https://docs.github.com/en/actions/security-guides/using-artifact-attestations-to-establish-provenance-for-builds)
proving which workflow run and commit produced it. Verify both before you
run anything downloaded as root:

```bash
curl -LO https://github.com/oddsund/gitops-agent/releases/latest/download/gitops-agent-linux-arm64
curl -LO https://github.com/oddsund/gitops-agent/releases/latest/download/gitops-agent-linux-arm64.sha256

sha256sum -c gitops-agent-linux-arm64.sha256
gh attestation verify gitops-agent-linux-arm64 -R oddsund/gitops-agent

chmod +x gitops-agent-linux-arm64
sudo install -o root -g root -m 0755 gitops-agent-linux-arm64 /usr/local/bin/gitops-agent
```

The checksum only proves the download wasn't corrupted or swapped in
transit; the attestation is what actually establishes that the binary
came from this repo's release workflow. `gh attestation verify` requires
the [GitHub CLI](https://cli.github.com/).

`gitops-agent -version` prints the release tag baked into the binary at
build time; a plain `go build` (or `go install ...@latest` without a
tagged module version) reports `dev`.

## Configuration

There are two config files, with different scopes and different
lifetimes.

### `config.toml` -- host-local

Read once at startup from `/etc/gitops-agent/config.toml` by default,
overridable with `-config`. This is where the repo lives and how to reach
it; changing it is a host operation, not a commit. See
[`config.example.toml`](config.example.toml) for a full annotated
template.

`[git]`

| Key | Default | Notes |
|---|---|---|
| `repo_url` | *(required)* | Any URL `go-git` can clone, e.g. an `ssh://` or `git@host:...` remote. |
| `branch` | *(required)* | Branch to track. |
| `clone_path` | *(required)* | Where the repo gets cloned/reset to. Separate from `config.toml`'s own location. |
| `pull_interval_seconds` | *(required, must be > 0)* | Idle poll cadence. |
| `active_interval_seconds` | `15` | Fast cadence after a change. Must not exceed `pull_interval_seconds` -- validated at load, since a silently-ignored misconfiguration here would be nasty to debug. |
| `active_window_seconds` | `900` | How long the fast cadence lasts after the last detected change. |
| `full_reconcile_interval_seconds` | `3600` | How often every enabled service is redeployed regardless of change detection, to correct drift. |

`[sops]`

| Key | Default | Notes |
|---|---|---|
| `ssh_key_path` | *(required)* | SSH private key used both for git authentication and, as-is, as the sops/age decryption identity. See [Secrets](#secrets) below. |

`[state]` -- optional section, all keys optional

| Key | Default | Notes |
|---|---|---|
| `path` | `/var/lib/gitops-agent/deployed.json` | Where the agent records which services it last deployed and from where, used to tear a service down after it's disabled or removed from the manifest. |

`[status]` -- optional section, all keys optional

| Key | Default | Notes |
|---|---|---|
| `listen_addr` | `127.0.0.1:9090` | Address the status HTTP server binds. Loopback by default; see [Status page](#status-page). |

Omitting `active_interval_seconds`, `active_window_seconds`,
`full_reconcile_interval_seconds`, `[state]`, or `[status]` entirely is
fine -- a minimal `config.toml` with just `[git]` (repo, branch,
clone_path, pull_interval_seconds) and `[sops].ssh_key_path` is valid and
gets sane defaults for everything else.

### `services.toml` -- repo-tracked

Lives at the root of the synced repo (not the host), and is reloaded from
the clone every poll cycle rather than only at startup.

```toml
[[services]]
name = "demoapp"
path = "services/demoapp"
enabled = true

[[services]]
name = "otherapp"
path = "services/otherapp"
enabled = false
```

- `name` -- required, must be non-empty and unique across the file. This
  is also the key used for the tmpfs secrets directory (see
  [Secrets](#secrets)) and the state file.
- `path` -- required. Directory containing that service's `compose.yml`
  and, optionally, `secrets.enc.env`, relative to the repo root.
- `enabled` -- whether the service should be running. `false` (or a
  deleted `[[services]]` block) tears the service down on the next cycle.

## Repo layout

A repo this agent can drive needs, at minimum:

```
.
├── services.toml
└── services/
    ├── demoapp/
    │   ├── compose.yml
    │   └── secrets.enc.env      # optional, sops-encrypted
    └── otherapp/
        └── compose.yml
```

`compose.yml` is a normal docker compose file; the agent just runs
`docker compose -f <path>/compose.yml up -d --remove-orphans` in that
directory. There's no gitops-agent-specific syntax in it, except that a
service which needs decrypted secrets should reference them by their
absolute runtime path (see below) rather than a relative one.

## Secrets

Secrets are stored in the repo encrypted with
[sops](https://github.com/getsops/sops), and decrypted on the host at
deploy time -- they never sit in the repo, or on disk, in plaintext except
transiently in a tmpfs mount.

`internal/sopsdecrypt` uses sops as a Go library
(`github.com/getsops/sops/v3/decrypt`) rather than shelling out to the
`sops` CLI, so the host never needs a `sops` binary installed alongside
the agent -- one less moving part on a machine that's meant to run a
single static binary.

The SSH key configured at `[sops].ssh_key_path` -- the same key used for
git access -- is used directly as sops's age decryption identity. Recent
sops/age versions support SSH ed25519 (and RSA) keys as age identities
without any conversion on the decryption side. To *encrypt* a file for a
given SSH public key, derive its age recipient string (for example with
the [`ssh-to-age`](https://github.com/Mic92/ssh-to-age) tool) and use that
as the sops recipient:

```bash
sops --age "$(ssh-to-age -i deploy_key.pub)" \
  --encrypt --input-type dotenv --output-type dotenv \
  secrets.env > services/demoapp/secrets.enc.env
```

At deploy time, `<service>/secrets.enc.env` is decrypted to
`/run/gitops-agent/<name>/secrets.env`, where `<name>` is exactly the
`name` from that service's `[[services]]` entry in `services.toml` -- that
string is what ties the decrypted file back to the service. `compose.yml`
then references it with an **absolute** `env_file:` entry:

```yaml
services:
  demoapp:
    image: ghcr.io/example/demoapp:latest
    env_file:
      - /run/gitops-agent/demoapp/secrets.env
```

An absolute path is required because `/run/gitops-agent/<name>` lives
outside the git checkout entirely.

`/run/gitops-agent` is tmpfs (backed by the systemd unit's
`RuntimeDirectory=gitops-agent`), so decrypted secrets disappear on reboot
or `systemctl stop gitops-agent`. Running containers are unaffected --
compose reads `env_file:` at container creation, not continuously -- but a
hand-run `docker compose up` while the agent is stopped won't have the
file available, and will fail if the service needs it.

## systemd

The service ships two systemd unit pairs.

- [`systemd/gitops-agent.service`](systemd/gitops-agent.service) -- the
  main unit. Runs as an unprivileged user that's a member of the `docker`
  group (needed to talk to the docker socket), with `StateDirectory=`
  and `RuntimeDirectory=` providing `/var/lib/gitops-agent` and
  `/run/gitops-agent` respectively, and `ReadWritePaths=` scoped to the
  clone directory -- everything else the unit can see is read-only. See
  the comments in the unit file itself for the sandboxing directives and
  the reasoning behind each; note that this sandboxing limits the blast
  radius of a bug in the agent, it does not defend against a malicious
  repo (see [Security](#security-model)).

- [`systemd/gitops-agent-update.service`](systemd/gitops-agent-update.service)
  and [`.timer`](systemd/gitops-agent-update.timer) -- an optional,
  separate self-update mechanism. The timer runs
  [`systemd/update.bash`](systemd/update.bash) daily (with a randomized
  delay so a fleet of hosts doesn't all hit GitHub at once), which checks
  the GitHub API for a release newer than what's installed, and if there
  is one, downloads and checksum-verifies it via
  [`scripts/lib/github-release.bash`](scripts/lib/github-release.bash),
  installs it, and restarts `gitops-agent.service`. This unit runs as
  root, since installing a new binary and restarting a system service both
  need privileges the agent's own unprivileged user doesn't have --
  keep that in mind when deciding whether to install it as-is or adapt it
  to your own update story.

Both units assume the binary lives at `/usr/local/bin/gitops-agent` and
the config at `/etc/gitops-agent/config.toml`; adjust `ExecStart=` if
yours live elsewhere.

## Status page

A failed deploy that only shows up as a `journalctl` line is easy to miss
for weeks on a host you rarely log into. `internal/statusserver` runs an
HTTP server, standard library only, no framework, serving:

- `GET /healthz` -- `200 ok` if the last completed reconcile cycle had no
  errors, `503` otherwise. Cheap to script against.
- `GET /` -- a plain HTML page: agent version, uptime, last sync, and
  per-service status. No JavaScript, no external resources, readable on a
  phone.
- `GET /status.json` -- the same data as JSON.

The same JSON is also written to `/var/lib/gitops-agent/status.json`
after every cycle (temp file plus rename, so a crash mid-write can't
corrupt it), so the last known status survives a restart and can be read
without the HTTP server running at all.

`[status].listen_addr` defaults to `127.0.0.1:9090` -- safe for a bare
`go run` or a local test, but not reachable from anywhere else. If you're
putting a reverse proxy in front of it (especially one running inside a
container, which typically can't reach the host's loopback interface),
either bind `listen_addr` to an interface the proxy can reach (e.g.
`0.0.0.0:9090`, fine behind a firewall or a private network) or run the
proxy on the host itself.

If you route to the status page under a path prefix (e.g.
`https://host/gitops-agent/` -> `http://127.0.0.1:9090/`), strip the
prefix before it reaches the agent. The index page is deliberately
link-free specifically so it doesn't need to know what prefix, if any,
it's being served under -- see the doc comment on
`statusserver.Handler`.

## Security model

The git repo is the trust boundary. Anyone who can push to the tracked
branch can get the agent to run arbitrary containers on the host --
`docker compose` config isn't sandboxed by this agent, so that includes
things like bind mounts and host networking if a `compose.yml` asks for
them. Treat write access to the repo as equivalent to running commands on
the host, and control it accordingly (branch protection, required
reviews, restricted push access -- whatever's appropriate for your setup).

Within that trust model, the agent tries to fail safe and to limit
unrelated risk:

- Decrypted secrets live only in tmpfs, under a directory the agent's
  systemd unit owns, and never touch the git checkout.
- The systemd unit is sandboxed (`ProtectSystem=strict`,
  `ProtectHome=read-only`, `NoNewPrivileges`, a restricted
  `RestrictAddressFamilies`, and more -- see the unit file's comments).
  This does not stop a deliberate attacker who already controls the repo
  -- the service user is in the `docker` group, which is root-equivalent
  by design, since anyone who can reach the docker socket can mount the
  host filesystem into a container. What it does is shrink the damage a
  *bug* in the agent itself (bad path handling, a vulnerable dependency)
  could do before it ever reaches that socket.
- Release binaries downloaded by `systemd/update.bash` are verified
  against the `.sha256` published with the release before they are
  installed, so the update path can't be used to swap in a corrupted or
  substituted binary without also controlling the release itself.
  Verifying the build attestation as well is a manual step today (see
  [Install](#install)); doing it in the updater is an open issue.

## Testing

```
go test ./...
```

`internal/sopsdecrypt/testdata` includes a throwaway, single-use test key
pair and a file encrypted against it, so the sops decryption path
(including the SSH-native age identity) is actually exercised in tests,
not mocked out. See that directory's own README for details.

## Runs well on a Raspberry Pi

The whole point of this agent is to be the one thing on an otherwise
minimal host that isn't a container: a single static Go binary, no
runtime dependencies beyond `docker compose` itself, low enough overhead
to poll every few seconds without noticing. It was written for, and is
released for, `linux/arm64` -- a Raspberry Pi 4 is a perfectly reasonable
place to run this.

## License

MIT. See [LICENSE](LICENSE).
