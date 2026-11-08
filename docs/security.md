# Security model

## The repository is the trust boundary

Anyone who can push to the tracked branch can make the agent run any
container on the host.

The agent does not sandbox what a `compose.yml` asks for. That includes
bind mounts and host networking.

Treat write access to the repository as equal to the right to run commands
on the host. Control it the way you would control that right: branch
protection, required reviews, restricted push access, or whatever fits
your setup.

The agent checks that each service path in `services.toml` stays inside the
clone, but it does not resolve symlinks. A symlink committed to the
repository can still point outside it. Anyone who can push can already run
containers, so this check is a guard against mistakes, not a boundary.

## What the agent does inside that model

The agent cannot fix the trust boundary above. It can fail safe and limit
unrelated risk.

### Email notifications

`[notify]` is configured only in the host's `config.toml`. The repository
cannot change who gets mail, which server the agent talks to, or whether
the feature is on. Text that comes from the repository (service names) or
from docker (error text) is put in the mail body only after control
characters are removed, and is never put in a header without that cleaning.
Error text is left out unless `include_error` is on. With `include_error`
on, docker errors are the fixed summary described in
[Compose errors and secret values](#compose-errors-and-secret-values), not
docker's output. The password is read
once from `password_file`, which the agent refuses to use if group or
others can read it, and the agent never logs it. The agent logs in only
over TLS and verifies the server certificate; there is no option to turn
verification off.

### Compose errors and secret values

`docker compose` can quote a decrypted value in its error, for example when
it cannot parse an `env_file`.

- The status page, `/status.json`, `/healthz` and email never show that
  text. They show a fixed summary, for example
  `docker compose up failed with exit code 1; see the journal`.
- The journal keeps the compose stderr (the last 2 KiB) so you can fix the
  problem. Known secret values from `/run/gitops-agent/*/secrets.env` are
  replaced with `***` first. Values shorter than 6 characters are not
  replaced, because they would also hit ordinary words and numbers.
- The limit: the scrub removes whole values. If compose quotes only a
  fragment of a value, the fragment is not found. Treat the journal as
  sensitive, as you treat `docker inspect`, and do not ship it to a place
  that is less trusted than the host. A secret that reaches compose by
  another route (a literal in `compose.yml`, the host environment) is not
  known to the agent.
- Decrypt errors (from sops) are shown as they are. They contain no
  decrypted values. The one exception to consider is the MAC value in an
  integrity failure, which is derived from the decrypted values.

### Secrets stay in tmpfs

Decrypted secrets live only under the runtime directory that the systemd
unit owns. They never touch the git checkout. See [Secrets](secrets.md).

### The unit is sandboxed

The main unit sets `ProtectSystem=strict`, `ProtectHome=read-only`,
`NoNewPrivileges`, a restricted `RestrictAddressFamilies`, and more. The
unit file gives the reason for each directive.

This does not stop an attacker who already controls the repository. The
service user is in the `docker` group, and that group is root-equivalent
by design: anyone who can reach the docker socket can mount the host
filesystem into a container.

The sandboxing limits the damage from a *bug* in the agent, before that
bug can reach the socket. Examples of such a bug are bad path handling and
a dependency with a vulnerability.

### The update path verifies its download

`gitops-agent update` runs once a day from the optional update timer, and
`scripts/install.sh` installs the first binary. Both check the release in
two steps before they replace anything.

First, the checksum. The binary must match the `.sha256` file of the same
release. This catches a corrupted download.

Second, if `gh` is on the host, the build provenance.
`gh attestation verify` checks that the workflow
`.github/workflows/release.yml` in this repository built the binary, that
it ran for the release tag being installed (`refs/tags/vX.Y.Z`), and that
it ran on a GitHub-hosted runner. An attestation from any other workflow,
branch or tag in the repository is rejected. This is the stronger check.
It runs by default. A host that does not want it can opt out; see
[Install](install.md). If `GITOPS_AGENT_REPO` points at a fork, the fork's
own `release.yml` must sign the release; you trust that fork's workflow.

If a check fails, the old binary stays in place.

The checksum alone is weak. It comes from the same release as the binary.
Anyone who can replace the binary can replace the checksum. When `gh` is
missing, the provenance check is skipped (the agent logs this) and the
update rests on the checksum alone. A host that wants the provenance check
must install `gh`.

### The updater is part of the binary it updates

The old updater was a separate shell script. It was owned by root and the
agent never wrote to it. A compromised `gitops-agent` binary could not
change it. The next scheduled run still fetched the next release,
verified it, and installed it. A bad release could correct itself.

That is no longer true. The updater is now the subcommand
`gitops-agent update`, and it updates the binary that contains it.
Suppose a malicious release passes verification once, for example through
a compromised release workflow. Its `update` subcommand can then report
"already on the latest version" forever. The host stays on the bad
version, and the timer does nothing to stop it. A malicious release could
also skip verification in its own successor.

This is a real property that the project gave up. It is small next to a
risk this document already accepts. The service user is in the `docker`
group, which is root-equivalent. A compromised binary already has a more
direct way to take the host than to edit its own updater.

In exchange, the project dropped a second script, a shared shell library,
and a root-owned copy of each on the host. They were also attack surface,
and unlike the binary, nothing verified them after install. The project
chose one verified binary over a separate updater.

The root-owned part that remains: `/usr/local/bin/gitops-agent` is owned
by root with mode 0755. The agent user can run it but cannot change it through file permissions.
Through the docker socket it still can; see above.
The update unit runs as root and executes that file. When
`gitops-agent install` runs, it refuses to continue if that file is not
owned by root or is writable by group or others. This check runs at
install time only.

If this trade is not acceptable for your threat model, turn the timer off
and update by hand:

```bash
sudo systemctl disable --now gitops-agent-update.timer
```

Then install each release yourself, with the checks in
[Install](install.md#install-by-hand).

### The first install is trust on first use

`scripts/install.sh` is the first step, and nothing checks it before you
run it. You download it over HTTPS and you read it. Do not pipe it into a
shell. The URL points at `main`. To pin a version, replace `main` with a
release tag. After that, the script verifies the binary it installs, and the
binary verifies every later update.

The attestation check protects the binary, not the script. If you need to
avoid trust on first use, install by hand with the steps in
[Install](install.md#install-by-hand), and verify with
`gh attestation verify`.
