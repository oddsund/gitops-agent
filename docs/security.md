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
`gh attestation verify` checks that a GitHub Actions workflow in this
repository built the binary. It does not pin a particular workflow file or
tag. This is still the stronger check. It runs by default. A host
that does not want it can opt out; see [Install](install.md).

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
