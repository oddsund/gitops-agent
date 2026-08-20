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

`systemd/update.bash` verifies a downloaded release against the `.sha256`
that the release publishes, before it installs the binary. Thus nobody can
use the update path to install a corrupted or substituted binary without
also controlling the release itself.

The build attestation is a stronger check, and today it is a manual step.
See [Install](install.md). To do it in the updater as well is an open
issue.
