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

The `-log-level` flag sets how much the agent logs. Use `debug`, `info`
(the default), `warn` or `error`. The environment variable
`GITOPS_AGENT_LOG_LEVEL` sets the same thing; the flag wins. `debug` also
shows each poll that found nothing new and the output of `docker compose`.

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

### `[notify]` — optional section, sends email

The agent can send a plain email when a service starts failing and when it
recovers. See [Notifications](#notifications) for how it behaves. The
section is off unless `smtp_host` is set. With `smtp_host` empty or
missing, the agent sends nothing and logs nothing about it.

| Key | Default | Notes |
|---|---|---|
| `smtp_host` | *(empty: off)* | The mail server, as a host name or IP address, without a port. |
| `smtp_port` | `587` | `465` when `tls = "implicit"`, `25` when `tls = "none"`. |
| `tls` | `"starttls"` | `"starttls"` starts in clear text and upgrades. The agent fails the send if the server does not offer the upgrade. `"implicit"` is TLS from the first byte (usually port 465). `"none"` is for a relay on this machine only: the agent rejects it unless `smtp_host` is `localhost` or a loopback IP address. |
| `from` | *(required)* | The sender address, for example `gitops-agent@example.org` or `GitOps <gitops-agent@example.org>`. |
| `to` | *(required)* | A list of 1 to 10 recipient addresses. |
| `username` | *(empty: no login)* | The SMTP login. When set, `password_file` is required, and so is `tls = "starttls"` or `"implicit"`. The agent never sends a password without TLS. |
| `password_file` | *(empty)* | Absolute path of a file that holds the password on one line. See below for who owns it. |
| `min_interval_seconds` | `3600` | The shortest time between two "failing" emails for the same service. It protects your inbox if a service keeps flapping. A recovery email is never held back. |
| `include_error` | `false` | Add the first line of the error (at most 200 characters) to the email. Read the warning below before you turn it on. |
| `status_url` | *(empty)* | The address of your [status page](status-page.md). When set, the email contains it as a link. It must be `http` or `https` and must not contain a user name or password. |

`config.toml` is world-readable (mode `0644`), so the password never goes
in it. Put it in its own file instead:

```
sudo install -m 600 -o gitops-agent -g gitops-agent /dev/null /etc/gitops-agent/smtp-password
sudo sh -c 'cat > /etc/gitops-agent/smtp-password'   # type the password, press Ctrl-D
```

The file must belong to the user that runs the agent (`User=` in the
systemd unit), because the agent does not run as root. It must be a
regular file with mode `0600`. The agent reads it once at startup. If the
file is missing, is not a regular file, or is readable by group or others,
the agent logs an error, keeps deploying, and sends no email until you fix
the file and restart it. The agent never writes the password to the log.

`include_error` is off by default for a reason. Error text from
`docker compose` can quote part of a line from a decrypted `secrets.env`
file. With `include_error = true`, that text can leave the host in an email
that other systems store and forward. Turn it on only if you trust the
route to your mailbox.

Example:

```toml
[notify]
smtp_host = "smtp.example.org"
tls = "starttls"
from = "gitops-agent@example.org"
to = ["you@example.org"]
username = "gitops-agent@example.org"
password_file = "/etc/gitops-agent/smtp-password"
status_url = "https://status.example.org/"
```

### The smallest valid file

You can omit `active_interval_seconds`, `active_window_seconds`,
`full_reconcile_interval_seconds`, `[state]`, `[status]`, and `[notify]`. A file with
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
| `name` | Required. 1 to 64 characters: letters, digits, `.`, `_` and `-`, starting with a letter or digit. It must be unique in the file. The agent uses this name as a directory name for the tmpfs secrets directory (see [Secrets](secrets.md)) and as a key in the state file, so it must be a single safe segment. A name like `../x` or `a/b` is rejected. |
| `path` | Required. The directory that holds `compose.yml` for this service, and `secrets.enc.env` if the service has secrets. The path is relative to the root of the repository and must stay inside it. Absolute paths, paths that climb out with `..`, and the repository root itself are rejected. Two services must not share a directory: `a`, `./a` and `a/` count as the same path. Two services must also not end in the same directory name when docker compose lowercases it and drops characters other than letters, digits, `_` and `-`: `a/app`, `b/app` and `b/App` would share one compose project. The agent takes the compose project name from the directory alone, so this rule assumes no `name:` key, `COMPOSE_PROJECT_NAME` or `-p` override. Leading `_` and `-` are dropped from that name, and a directory name that leaves nothing, such as `___`, is rejected. Control characters are rejected. The agent stores the path in its cleaned form. |
| `enabled` | Whether the service must run. `false`, or a removed `[[services]]` block, stops the service on the next cycle. |

If a rule fails, the agent rejects the whole file. It logs
`services manifest ... is broken, keeping last-known-good list` with the
reason and, where there is one, the service name, and keeps using the last valid manifest. On a
fresh start there is no earlier manifest, so nothing is deployed until the
file is fixed. The agent checks the text of the path only. It does not
follow symlinks (see [Security](security.md)).

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

## Notifications

The status page tells you what is wrong when you look. Email tells you
when you did not look.

The agent sends one email when a service goes from working to failing, and
one when it works again. It does not send an email on every cycle while a
service stays broken. A failure of the git sync is reported in the same
way, as `git sync`. If several services change state in the same cycle,
you get one email that lists all of them.

- A service counts as failing when its last deploy, secrets decrypt, or
  teardown failed. It counts as working again after the next success.
- Between two "failing" emails for the same service the agent waits at
  least `min_interval_seconds`. A failure inside that time is not lost:
  the email goes out when the time has passed, if the service is still
  failing. If the service recovers first, you get no email for it at all.
- The agent keeps this state in memory. After a restart, a service that is
  still failing causes one new email. If the agent restarts in a loop, you
  get one email per restart, up to the rate limit of a fresh process.
- Sending runs in the background with a 30 second limit. A slow or broken
  mail server never delays a deploy. If a send fails, the agent writes one
  warning to the journal (`notification not sent`, with the step and a
  short reason) and does not try again. The next state change sends
  a new email. Because a lost "failing" email is not retried, a later
  "recovered" email can arrive for a failure you never received (for
  example when the send failed or the send queue was full).
- The email says which service, at which stage (`sync`, `decrypt`,
  `deploy`, `teardown`), the commit, the time and, if you set it, the link
  to the status page. It contains error text only if `include_error` is
  `true`.
- Not covered: a broken `services.toml` (the agent keeps the last good
  list and logs a warning), and a failure to remove decrypted secrets.
  Both show on the status page and in the journal.

The agent never opens a connection to a mail server the repository chooses.
The server, the recipients and the password all come from the host's
`config.toml`.
