# gitops-agent

A small daemon that keeps the docker compose services of one host in sync
with a git repository.

Point it at a repository that holds a `services.toml` manifest and one
`compose.yml` for each service. The agent polls that repository, decrypts
the secrets of each service, and runs `docker compose up -d` for what
changed. Disable a service, and the agent stops it on the next cycle.

There is no API on the agent, no cluster, and no custom resources. There
is a git repository, some compose files, and a daemon that reconciles the
two.

The agent is one static Go binary. It needs `docker compose` on the host
and nothing else. It exists so that a change to a self-hosted service is a
commit instead of an SSH session. A Raspberry Pi 4 is a reasonable place
to run it, and that is the platform the releases are built for.

## What it looks like

The repository that the agent syncs holds a manifest:

```toml
[[services]]
name = "demoapp"
path = "services/demoapp"
enabled = true
```

Each service is a directory:

```
services/demoapp/
├── compose.yml
└── secrets.enc.env      # optional, encrypted with sops
```

Commit a change under `services/demoapp/`, and the agent deploys it on the
next poll. Set `enabled = false`, and the agent stops the service.

## Install

```
go install github.com/oddsund/gitops-agent/cmd/gitops-agent@latest
```

Prebuilt `linux/arm64` binaries are on the [releases page][releases].
Every release has a checksum and a build provenance attestation.
[Install](docs/install.md) explains how to verify them and how to install
the systemd units.

## Documentation

[docs/](docs/README.md) has the detail:

| Document | What it covers |
|---|---|
| [How it works](docs/how-it-works.md) | The reconcile cycle, the polling cadence, and the signals |
| [Install](docs/install.md) | Installation, release verification, and the systemd units |
| [Configuration](docs/configuration.md) | `config.toml`, `services.toml`, and the repository layout |
| [Secrets](docs/secrets.md) | sops encryption, and where decrypted files go |
| [Status page](docs/status-page.md) | The HTTP endpoints and reverse proxies |
| [Security model](docs/security.md) | The trust boundary and the systemd sandboxing |
| [Development](docs/development.md) | Tests and test fixtures |

## License

MIT. See [LICENSE](LICENSE).

[releases]: https://github.com/oddsund/gitops-agent/releases
