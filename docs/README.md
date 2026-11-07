# Documentation

To learn what the agent does, read [How it works](how-it-works.md). To run
it, read [Install](install.md).

| Document | What it covers |
|---|---|
| [How it works](how-it-works.md) | The reconcile cycle step by step, the adaptive polling cadence, and the signals the agent answers |
| [Install](install.md) | The bootstrap script, `gitops-agent install` and `gitops-agent update`, how releases are verified, the systemd units, limitations, and migration from the old provisioning |
| [Configuration](configuration.md) | Every key of `config.toml` and `services.toml`, and the repository layout the agent expects |
| [Secrets](secrets.md) | How sops encryption works here, and where decrypted files go on the host |
| [Status page](status-page.md) | The HTTP endpoints, the status file on disk, and how to put a reverse proxy in front of them |
| [Security model](security.md) | What the trust boundary is, what the systemd sandboxing does and does not do, and the trade that self-update makes |
| [Development](development.md) | Tests, the sops test fixture, and the conventions of this repository |

The [main README](../README.md) has the summary.
