# How it works

The agent runs one reconcile cycle at a time, then waits. This page
describes a cycle, the wait between cycles, and the signals the agent
answers.

## The reconcile cycle

### 1. Sync

`internal/gitsync` fetches the repository and hard-resets the local clone
to `origin/<branch>`.

This is a hard reset, not a merge. The remote is always the source of
truth, so the agent discards a local edit, a local commit, or a
force-pushed remote history.

A `git pull` that cannot merge wedges the loop forever. It logs the same
error every cycle and deploys nothing. A hard reset always makes progress.

The agent keeps untracked files across the reset. It snapshots them
first. If the reset removes them, the agent puts them back.

### 2. Reload the manifest

The agent re-reads `services.toml` from the new clone every cycle, not
only at startup. To enable a service is a commit, not an SSH session.

If the manifest does not parse (bad TOML, a duplicate service name), the
agent logs the error and keeps the last-known-good list. It picks up a
commit that corrects the manifest on the next cycle.

### 3. Tear down

Before it deploys anything, the agent compares the services it deployed
before against the current manifest.

A service that is no longer `enabled = true` gets `docker compose down
--remove-orphans`. The agent then removes that service from its state.
This applies both when you set `enabled = false` and when you remove the
`[[services]]` block.

The teardown pass runs before the deploy pass in the same cycle. Thus a
service that one commit renames does not collide with itself.

### 4. Decide what to deploy

The agent deploys a service in three cases:

- The directory of the service changed between the previous commit and
  the current commit.
- The agent did not deploy the service since it started.
- A full reconcile is due.

For the first case, the agent compares the git tree hash of the
directory. A tree hash is already a content hash of a whole directory, so
this is one comparison, not a file walk.

The full reconcile exists because drift is not visible in git. If you
stop a container by hand, no commit looks different. Every
`full_reconcile_interval_seconds` (default 3600) the agent deploys every
enabled service.

If the agent cannot compare the tree hashes (unknown commit, unreadable
tree), it deploys the service. A redundant `docker compose up` is cheap
and idempotent. A skipped one leaves the host wrong.

### 5. Decrypt

For each service that it deploys, `internal/sopsdecrypt` decrypts
`<service>/secrets.enc.env` to
`/run/gitops-agent/<service>/secrets.env`. That directory is tmpfs, not
the git checkout.

A service without `secrets.enc.env` is not an error. The agent does
nothing for that service in this step. See [Secrets](secrets.md).

### 6. Deploy

`internal/deploy` runs `docker compose -f <service>/compose.yml up -d
--remove-orphans`.

If one service fails, the agent logs the error and continues with the
others. It tries the failed service again on the next cycle.

### 7. Persist state and status

The agent writes which services it deployed, and from where, to a small
JSON file (`internal/state`). Thus it can still stop a service after you
remove the `[[services]]` entry, because the manifest no longer holds the
path.

`internal/statusserver` writes a status snapshot to disk and serves it
over HTTP. See [Status page](status-page.md).

## Polling cadence

Commits arrive in bursts. You push a change, you see a mistake, and you
push a correction. With a fixed five-minute poll, the second push costs
five more minutes.

The cadence is adaptive (`internal/schedule`):

- The idle cadence is `pull_interval_seconds` (default 300), with up to
  ±10% jitter. The jitter keeps the agent off a metronome.
- After a poll that finds new commits, the cadence becomes
  `active_interval_seconds` (default 15). It stays there until
  `active_window_seconds` (default 900) pass with no new commits. Then it
  returns to the idle cadence.

The agent logs the change between cadences, not every poll. At the active
interval, one line for every poll is one line every 15 seconds.

## Signals

`SIGHUP` skips the rest of the current wait and starts a cycle at once:

```bash
kill -HUP $(pidof gitops-agent)
systemctl kill -s HUP gitops-agent     # under systemd
```

`SIGTERM` and `SIGINT` start a clean shutdown. The agent starts no new
cycle. When the current wait ends, the agent exits. Thus a restart does
not cut a deploy in half.
