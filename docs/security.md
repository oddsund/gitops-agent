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

`gitops-agent update` (run daily by the optional self-update timer, and
by `scripts/install.sh` on first bootstrap) verifies a downloaded release
against the `.sha256` that the release publishes, before it installs the
binary. Thus nobody can use the update path to install a corrupted or
substituted binary without also controlling the release itself.

If `gh` is on the host, it also verifies the build attestation with
`gh attestation verify`. This is the stronger of the two checks, and it
runs by default. A host that does not want this check can opt out. See
[Install](install.md) for how.

### The updater trades away one property it used to have

Before the update logic moved into the binary, the updater
(`systemd/update.bash`) was a separate, root-owned file the agent never
wrote to. A compromised `gitops-agent` binary -- however that compromise
happened -- could not disarm its own updater: the next scheduled run
would still fetch, verify, and install whatever the *next* legitimate
release actually was, correcting course on its own.

That's no longer true. The updater is now a subcommand of the same
binary it updates (`gitops-agent update`, [internal/selfupdate](../internal/selfupdate)),
installed via the same `install(1)` step that installs everything else.
A malicious release that passed verification once -- a compromised
release workflow, a stolen signing identity, whatever the actual failure
was -- could ship a successor binary whose `update` subcommand always
reports "already on latest," permanently wedging that host on the bad
version with no self-correction.

This is a real property being given up, not a hypothetical one, and it's
worth stating plainly rather than dropping the subject. It's small next
to the risk already accepted elsewhere in this document: the service user
is in the `docker` group, which is root-equivalent by design, so a
compromised binary already had a much more direct route to full host
compromise than editing its own updater. Weighed against that, and against
the bash surface a separate updater required (a second script, a shared
library, a root-owned copy of each, the `GITHUB_RELEASE_LIB` indirection
between them -- all itself attack surface, and all unverified once
installed, unlike the binary itself), collapsing the updater into the
verified binary is the trade this project makes. If your threat model
weighs a self-healing updater over a smaller bash surface, run
`gitops-agent update` from your own process instead of the shipped timer,
against a binary you fetch and verify through a channel this repository's
own release doesn't control.
