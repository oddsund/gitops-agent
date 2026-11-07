# Secrets

Secrets live in the repository encrypted with
[sops](https://github.com/getsops/sops). The agent decrypts them on the
host at deploy time. They are never in plaintext in the repository, and on
the host they are in plaintext only in a tmpfs mount.

## Why the sops library, not the CLI

`internal/sopsdecrypt` uses sops as a Go library
(`github.com/getsops/sops/v3/decrypt`). It does not call the `sops`
command. Thus the host does not need a `sops` binary next to the agent.
That is one moving part fewer on a machine that must run one static
binary.

## The key

The agent uses the SSH key at `[sops].ssh_key_path` — the same key it uses
for git — directly as the sops age identity. Recent versions of sops and
age accept SSH ed25519 and RSA keys as age identities, with no conversion
on the decryption side.

To encrypt a file for an SSH public key, first derive the age recipient
string from it. The [`ssh-to-age`](https://github.com/Mic92/ssh-to-age)
tool does this. Then give that string to sops:

```bash
sops --age "$(ssh-to-age -i deploy_key.pub)" \
  --encrypt --input-type dotenv --output-type dotenv \
  secrets.env > services/demoapp/secrets.enc.env
```

## Where the decrypted file goes

At deploy time the agent decrypts `<service>/secrets.enc.env` to
`/run/gitops-agent/<name>/secrets.env`.

`<name>` is exactly the `name` from the `[[services]]` entry of that
service in `services.toml`. That string is the link between the decrypted
file and the service.

`compose.yml` then reads the file with an **absolute** `env_file:` entry:

```yaml
services:
  demoapp:
    image: ghcr.io/example/demoapp:latest
    env_file:
      - /run/gitops-agent/demoapp/secrets.env
```

The path must be absolute, because `/run/gitops-agent/<name>` is outside
the git checkout.

## Lifetime

`/run/gitops-agent` is tmpfs. The systemd unit creates it with
`RuntimeDirectory=gitops-agent`. Decrypted secrets are gone after a reboot
or after `systemctl stop gitops-agent`.

Running containers do not notice. Compose reads `env_file:` when it
creates a container, not continuously.

CAUTION: If you run `docker compose up` by hand while the agent is
stopped, the decrypted file is not there. A service that needs it fails to
start.

## Removing a service

The agent deletes `/run/gitops-agent/<name>/secrets.env` for a service in
two cases:

- The agent has just stopped the service's containers because it is no
  longer enabled, or because it is gone from `services.toml`.
- The service is in `services.toml` with `enabled = false`. The agent
  checks this on every cycle. A file that is already gone is not an error.

The agent removes the file only after `docker compose down` succeeds. If
`down` fails, the containers may still be running, so the file stays and
the agent tries again on the next cycle. The agent also removes the
`/run/gitops-agent/<name>` directory, but only if it is empty.

The agent never scans `/run/gitops-agent`. It removes files only for
service names it knows from `services.toml` or from its own deploy state.

The agent does not remove the file in these cases:

- You delete `secrets.enc.env` from a service that stays enabled. The old
  `secrets.env` stays until the service is disabled or removed, or until
  the host reboots. To clear it at once, delete it by hand.
- You remove a service from `services.toml` and the agent fails to delete
  the file during that one cycle. The agent logs the error and does not
  try again for that name. The file is gone at the next reboot or
  `systemctl stop gitops-agent`.
