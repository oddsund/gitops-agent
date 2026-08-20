# Configuration

There are two configuration files. They have different scopes and
different lifetimes.

| File | Where it lives | When it is read |
|---|---|---|
| `config.toml` | On the host | Once, at startup |
| `services.toml` | In the synced repository | Every poll cycle |

## `config.toml` — on the host

This file says where the repository is and how to reach it. To change it
is a host operation, not a commit.

The agent reads `/etc/gitops-agent/config.toml` by default. The `-config`
flag overrides that path.
[`config.example.toml`](../config.example.toml) is a full annotated
template.

### `[git]`

| Key | Default | Notes |
|---|---|---|
| `repo_url` | *(required)* | Any URL that `go-git` can clone, for example an `ssh://` or `git@host:...` remote. |
| `branch` | *(required)* | The branch to track. |
| `clone_path` | *(required)* | Where the agent clones and resets the repository. This is not where `config.toml` itself lives. |
| `pull_interval_seconds` | *(required, more than 0)* | The idle poll cadence. |
| `active_interval_seconds` | `15` | The fast cadence after a change. It must not be more than `pull_interval_seconds`. The agent rejects a larger value at startup, because a silent misconfiguration here is hard to debug. |
| `active_window_seconds` | `900` | How long the fast cadence lasts after the last change. |
| `full_reconcile_interval_seconds` | `3600` | How often the agent deploys every enabled service, whether it changed or not. This corrects drift. |

### `[sops]`

| Key | Default | Notes |
|---|---|---|
| `ssh_key_path` | *(required)* | The SSH private key. The agent uses it for git authentication and, unchanged, as the sops age identity. See [Secrets](secrets.md). |

### `[state]` — optional section, all keys optional

| Key | Default | Notes |
|---|---|---|
| `path` | `/var/lib/gitops-agent/deployed.json` | Where the agent records which services it deployed, and from where. It needs this to stop a service after you disable or remove it. |

### `[status]` — optional section, all keys optional

| Key | Default | Notes |
|---|---|---|
| `listen_addr` | `127.0.0.1:9090` | The address that the status server binds. It is loopback-only by default. See [Status page](status-page.md). |

### The smallest valid file

You can omit `active_interval_seconds`, `active_window_seconds`,
`full_reconcile_interval_seconds`, `[state]`, and `[status]`. A file with
`[git]` (`repo_url`, `branch`, `clone_path`, `pull_interval_seconds`) and
`[sops].ssh_key_path` is valid. The agent uses the defaults above for
everything else.

## `services.toml` — in the repository

This file lives at the root of the synced repository, not on the host. The
agent re-reads it from the clone every poll cycle.

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

| Key | Notes |
|---|---|
| `name` | Required. It must not be empty, and it must be unique in the file. The agent also uses this name for the tmpfs secrets directory (see [Secrets](secrets.md)) and for the state file. |
| `path` | Required. The directory that holds `compose.yml` for this service, and `secrets.enc.env` if the service has secrets. The path is relative to the root of the repository. |
| `enabled` | Whether the service must run. `false`, or a removed `[[services]]` block, stops the service on the next cycle. |

## Repository layout

A repository that this agent can drive needs at least this:

```
.
├── services.toml
└── services/
    ├── demoapp/
    │   ├── compose.yml
    │   └── secrets.enc.env      # optional, encrypted with sops
    └── otherapp/
        └── compose.yml
```

`compose.yml` is a normal docker compose file. The agent runs
`docker compose -f <path>/compose.yml up -d --remove-orphans` in that
directory, and adds no syntax of its own.

There is one thing to know. A service that reads decrypted secrets must
point at their absolute runtime path, not a relative one. See
[Secrets](secrets.md).
