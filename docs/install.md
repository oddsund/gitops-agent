# Install

## From source

```
go install github.com/oddsund/gitops-agent/cmd/gitops-agent@latest
```

This gets you the binary only. Run `gitops-agent install` yourself
afterwards (see below) to write `config.toml` and the systemd units.

## Bootstrap script

[`scripts/install.sh`](../scripts/install.sh) is the fastest way to get a
fresh host running end to end. It downloads the latest release binary,
verifies its checksum and (if `gh` is installed) its build provenance
attestation, installs it to `/usr/local/bin/gitops-agent`, then hands off
to `gitops-agent install` to write `config.toml` and the systemd units.

The script verifies the binary it downloads, but nothing verifies the
script itself. Download it and read it before you run it, rather than
piping curl straight into a shell:

```bash
curl -LO https://raw.githubusercontent.com/oddsund/gitops-agent/main/scripts/install.sh
less install.sh
chmod +x install.sh
sudo ./install.sh -repo-url git@github.com:yourname/your-gitops-config.git
```

Every argument after the script name passes straight through to
`gitops-agent install` -- see [below](#gitops-agent-install) for the full
flag list. `-repo-url` is the only one you're likely to need.

## From a release, by hand

Prebuilt binaries are on the [releases page][releases]. Releases are built
for `linux/arm64` only. If you need another platform, open an issue.

Every release publishes three things: the binary, a `.sha256` checksum
file, and a [build provenance attestation][attestations]. The attestation
records which workflow run and which commit produced the binary. This is
what `scripts/install.sh` automates; do it by hand if you'd rather inspect
each step yourself, or you're re-installing just the binary on a host
that's already configured:

```bash
curl -LO https://github.com/oddsund/gitops-agent/releases/latest/download/gitops-agent-linux-arm64
curl -LO https://github.com/oddsund/gitops-agent/releases/latest/download/gitops-agent-linux-arm64.sha256

sha256sum -c gitops-agent-linux-arm64.sha256
gh attestation verify gitops-agent-linux-arm64 -R oddsund/gitops-agent

chmod +x gitops-agent-linux-arm64
sudo install -o root -g root -m 0755 gitops-agent-linux-arm64 /usr/local/bin/gitops-agent
```

The two commands prove different things. The checksum proves only that
the download is not corrupted and was not replaced in transit. The
attestation proves that the binary came from the release workflow of this
repository. `gh attestation verify` needs the [GitHub CLI][gh].

## Which version is installed

`gitops-agent -version` prints the release tag that the build put into the
binary. A plain `go build` reports `dev`. So does
`go install ...@latest` without a tagged module version.

## `gitops-agent install`

Writes the host-local config and the systemd units, and enables the
services. `scripts/install.sh` calls this automatically; run it yourself
if you already have the binary in place, or to pick up a config repo
change like a moved `clone_path` (re-running is safe -- see below).

```
sudo gitops-agent install -repo-url git@github.com:yourname/your-gitops-config.git
```

| Flag | Default | |
|---|---|---|
| `-repo-url` | *(required)* | The config repo's git URL |
| `-user` | `$SUDO_USER`, else the current user | Who `gitops-agent.service` runs as |
| `-ssh-key-path` | `<user>`'s home + `/.ssh/id_ed25519` | Used for both git auth and sops decryption -- see [Secrets](secrets.md) |
| `-clone-path` | `/opt/gitops-agent/repo` | Where the config repo is cloned to on this host |
| `-config` | `/etc/gitops-agent/config.toml` | Where to write it, if it doesn't already exist |

`config.toml` is written once, from [`config.example.toml`](../config.example.toml),
and never overwritten on a later run -- if you hand-edited it, that edit
sticks. `gitops-agent.service`'s `User=` and `ReadWritePaths=` are
re-rendered on every run, `ReadWritePaths=` taken from `clone_path` in the
config *on disk*, not from `-clone-path`, so a hand-edited `clone_path`
stays authoritative too. The service is only restarted if the rendered
unit actually changed, or it wasn't running -- a repeat run with the same
inputs is a no-op.

See [internal/installer](../internal/installer) for the implementation.

## `gitops-agent update`

Checks GitHub for a release newer than the one recorded in
`/etc/gitops-agent/installed-version`, and if there is one, downloads,
verifies (checksum, then attestation -- same two checks as the manual
steps above), installs it atomically, and restarts `gitops-agent.service`.
This is what the [self-update timer](#the-self-update-timer) runs daily;
run it by hand to update immediately.

Configuration is environment variables, not flags:

| Variable | Default | |
|---|---|---|
| `GITOPS_AGENT_REPO` | `oddsund/gitops-agent` | Where to look for releases |
| `GITOPS_AGENT_BIN` | `/usr/local/bin/gitops-agent` | What to replace |
| `GITOPS_AGENT_VERSION_FILE` | `/etc/gitops-agent/installed-version` | What's currently installed |
| `GITOPS_AGENT_SERVICE` | `gitops-agent` | What to restart after installing |
| `GITHUB_TOKEN_FILE` | `/etc/gitops-agent/github-token` | Only needed against a private fork |
| `GITOPS_AGENT_SKIP_ATTESTATION` | unset | Set to `1` to skip the attestation check and rely on the checksum alone |

A checksum mismatch or a failed attestation aborts the update and leaves
the currently installed binary untouched. A missing `gh` only skips the
attestation check (logged), it doesn't fail the update -- the checksum
check already ran either way.

See [internal/selfupdate](../internal/selfupdate) for the implementation.

## systemd

`gitops-agent install` writes two units.

### The agent

[`systemd/gitops-agent.service`](../systemd/gitops-agent.service) is the
main unit. It runs as an unprivileged user that is a member of the
`docker` group, because the agent talks to the docker socket.

The unit gives the agent two directories: `StateDirectory=` for
`/var/lib/gitops-agent` and `RuntimeDirectory=` for `/run/gitops-agent`.
`ReadWritePaths=` covers the clone directory. Everything else that the
unit can see is read-only.

The unit file has a comment for each sandboxing directive and the reason
for it. This sandboxing limits the damage from a bug in the agent. It does
not defend against a hostile repository. See
[Security model](security.md).

### The self-update timer

[`systemd/gitops-agent-update.service`](../systemd/gitops-agent-update.service)
and [`.timer`](../systemd/gitops-agent-update.timer) run
`gitops-agent update` once a day. The timer adds a random delay, so that
many hosts do not call GitHub at the same moment. The service runs as
root (no `User=`): installing into `/usr/local/bin` and restarting
`gitops-agent.service` both need privileges the unprivileged agent user
doesn't have. That's safe despite `ExecStart=` pointing at the very
binary the unprivileged unit above also runs -- see
[Security model](security.md) for why, and for the tradeoff this timer
takes on.

CAUTION: This unit runs as root. Decide whether you want that before you
install the timer, or replace it with your own update process (a plain
cron job calling `gitops-agent update` works too).

### Paths

Both units expect the binary at `/usr/local/bin/gitops-agent` and the
configuration at `/etc/gitops-agent/config.toml`. If yours are elsewhere,
re-run `gitops-agent install` with `-config`, or edit `ExecStart=`
directly.

## Migrating an existing host

A host provisioned before `gitops-agent install`/`update` existed has
`gitops-agent-update.service`'s `ExecStart=` pointing at a root-owned copy
of the old, full `update.bash` under `/usr/local/sbin`, with a
`GITHUB_RELEASE_LIB` environment line pointing at a second root-owned copy
of `scripts/lib/github-release.bash`. Nothing forces you to migrate: that
old copy keeps checking GitHub and updating the *binary* correctly on its
own, indefinitely -- it just doesn't know about the `install`/`update`
subcommands the binary it's fetching now has.

To move a host onto the new, binary-driven units, re-run the same command
you originally provisioned with:

```bash
sudo ./install.sh -repo-url git@github.com:yourname/your-gitops-config.git
```

(or `sudo gitops-agent install ...` directly, if the binary's already
current). This re-renders `gitops-agent.service` and replaces
`gitops-agent-update.service`/`.timer` with versions whose `ExecStart=`
points straight at `gitops-agent update` -- no script, no environment
indirection. Once that's done, `/usr/local/sbin/gitops-agent-update` and
`/usr/local/lib/gitops-agent/github-release.bash` are no longer referenced
by anything and are safe to delete.

If a host's timer somehow still points at the old path but has picked up
a newer `systemd/update.bash` some other way (for example: it was
re-provisioned with tooling that hasn't been updated to point `ExecStart=`
at the binary yet, but did fetch a current release of this repo), that
script is now a short shim that execs `gitops-agent update` itself, with a
deprecation notice on stderr -- it keeps working either way.

[releases]: https://github.com/oddsund/gitops-agent/releases
[attestations]: https://docs.github.com/en/actions/security-guides/using-artifact-attestations-to-establish-provenance-for-builds
[gh]: https://cli.github.com/
