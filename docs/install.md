# Install

The supported way to install gitops-agent is `scripts/install.sh`. It
installs the release binary, then the binary installs its own config and
systemd units. Read the script before you run it.

## Before you start

- A Linux arm64 host with systemd.
- Docker with the compose plugin.
- A user that will run the agent. The user must be in the `docker` group.
  `gitops-agent install` does not add the user to the group.
- An SSH key for that user that can read your config repository. The
  default is `~/.ssh/id_ed25519`.
- The URL of that repository.
- Optional: the [GitHub CLI][gh] (`gh`), for the provenance check. See
  [What gets verified](#what-gets-verified).

## Install with the script

Download the script, read it, then run it. Do not pipe it into a shell.
The URL points at `main`. To pin a version, replace `main` with a release
tag.

```bash
curl --fail --show-error --location --proto '=https' --tlsv1.2 \
  --output install.sh \
  https://raw.githubusercontent.com/oddsund/gitops-agent/main/scripts/install.sh
less install.sh
bash install.sh --repo-url git@github.com:you/config.git --user pi
```

Run the script as your normal user, not with `sudo bash`. The script
downloads and verifies as you, so it can use your own `gh` login. It
calls `sudo` only for the install steps and for the final hand-off. If
you are already root, it does not need `sudo`.

If `gh` is installed and you run the script as root, for example with
`sudo bash install.sh`, the provenance check fails: root has no `gh`
login, and `sudo` strips your environment. The script then tells you what to do. Run it as your normal
user, so your own `gh` login is used. If you must run it as root, pass the
variables through `sudo`: `sudo GH_TOKEN=... bash install.sh ...` or, to
proceed on the checksum alone,
`sudo GITOPS_AGENT_SKIP_ATTESTATION=1 bash install.sh ...`.

The script does these steps, in order:

1. It finds the latest release tag.
2. It downloads `gitops-agent-linux-arm64` and its `.sha256` file, with a
   progress meter.
3. It checks the checksum of the download.
4. It stages the binary as root, next to its final path.
5. It hashes the staged copy again, as root. The hash must match the one
   it verified in step 3.
6. If `gh` is installed, it runs `gh attestation verify` on the staged
   file. This step runs as your user.
7. It moves the staged file to `/usr/local/bin/gitops-agent`.
8. It runs `gitops-agent install` with your arguments.

Nothing is installed before both checks pass. If a check is skipped, the
script logs that.

Also note:

- On a host that is not Linux arm64, the script stops with an error.
- If `/usr/local/bin/gitops-agent` already is the latest release, the
  script skips the download and goes straight to `gitops-agent install`.
  It logs `(the installed binary was not re-verified)`: the script trusts
  the binary on disk and checks nothing. You can run the script again at
  any time.
- The script passes every argument to `gitops-agent install` unchanged.
  `-h` prints the usage of the script and does nothing else.
- The script reads two environment variables. `GITOPS_AGENT_REPO` sets the
  release source. The default is `oddsund/gitops-agent`. For a fork, see
  [Limitations](#limitations). `GITOPS_AGENT_SKIP_ATTESTATION=1` skips the
  provenance check. The install path is fixed. You cannot change it.

> The script cannot verify itself. You trust the copy you downloaded, and
> you trust HTTPS to GitHub. That is why you read it first. The `.sha256`
> file comes from the same release as the binary, so it only guards
> against a corrupted download. It does not stop a compromised release.
> The build provenance attestation is the real check, and it needs `gh`.

### Install by hand

If you do not want to run the script, do its steps yourself. Every
release publishes the binary, a `.sha256` checksum file, and a
[build provenance attestation][attestations]. Prebuilt binaries are on the
[releases page][releases].

```bash
curl --fail --show-error --location -O https://github.com/oddsund/gitops-agent/releases/latest/download/gitops-agent-linux-arm64
curl --fail --show-error --location -O https://github.com/oddsund/gitops-agent/releases/latest/download/gitops-agent-linux-arm64.sha256
sha256sum -c gitops-agent-linux-arm64.sha256
gh attestation verify gitops-agent-linux-arm64 -R oddsund/gitops-agent
sudo install -o root -g root -m 0755 gitops-agent-linux-arm64 /usr/local/bin/gitops-agent
sudo gitops-agent install --repo-url git@github.com:you/config.git --user pi
```

The two checks prove different things. The checksum proves only that the
download is not corrupted. The attestation proves that a GitHub Actions
workflow in this repository built the file. It does not pin a particular
workflow file or tag: the check passes for any workflow run in that
repository. If you set `GITOPS_AGENT_REPO` to a fork, you trust the fork.

### From source

```bash
go install github.com/oddsund/gitops-agent/cmd/gitops-agent@latest
```

A source build reports `dev` as its version. `gitops-agent install` does
not record a version for it. `gitops-agent update` treats it as out of
date and replaces it with the latest release. `update` has a release asset
for linux/arm64 only (see [Limitations](#limitations)).

## What gets verified

`install.sh` and `gitops-agent update` run two checks, in this order. The
steps of `install.sh` around them are in [Install with the script](#install-with-the-script): it stages
the file as root and hashes it again before the provenance check, so the
file that `gh` checks is the file that gets installed.

- Checksum, always. The tool hashes the binary (SHA-256) and compares the
  hash with the `.sha256` file of the same release.
- Build provenance. The tool runs
  `gh attestation verify <file> -R oddsund/gitops-agent`. It does this only
  if `gh` is on the PATH of the host and `GITOPS_AGENT_SKIP_ATTESTATION` is
  not `1`.

> The attestation check is skipped when `gh` is not installed. Then the
> install, and every later update, rests on the checksum alone. The
> checksum comes from the same place as the binary: the same release.
> Whoever can replace the binary can replace the checksum. So without
> `gh`, the checksum guards against corruption, not against a compromised
> release. Each skipped check is logged: look for
> `skipping build provenance verification` in the output of `install.sh`
> and in `journalctl -u gitops-agent-update`.
>
> If `gh` is installed but cannot authenticate, the check fails and the
> install or update stops. Run `gh auth login`, or put a token in
> `/etc/gitops-agent/github-token` for the update timer. The file holds a
> credential and the agent does not check its mode, so make it readable by
> root only (`chmod 600`). Or set
> `GITOPS_AGENT_SKIP_ATTESTATION=1` and accept the checksum-only risk
> above.

## gitops-agent install

`install.sh` runs this command. You can also run it again by hand:

```
gitops-agent install --repo-url URL [--user NAME] [--ssh-key PATH] [--clone-path PATH]
```

| Flag | Meaning | Default |
|---|---|---|
| `--repo-url` | Git URL of your config repository. Required only when `/etc/gitops-agent/config.toml` does not exist yet. | none |
| `--user` | The user that `gitops-agent.service` runs as. Not `root`. | `$SUDO_USER` |
| `--ssh-key` | SSH key for git and for sops. | `~/.ssh/id_ed25519` of `--user` |
| `--clone-path` | Where the agent clones the repository. | `/opt/gitops-agent/repo` |

- The command needs root. It exits 1 on any error and 0 otherwise, `-h`
  included. It takes no positional arguments. It reads no environment
  variables, except `SUDO_USER` as the default for `--user`.
- Before it writes anything, it checks `/usr/local/bin/gitops-agent`. It
  refuses to continue if the file is not owned by root or if it is
  writable by group or others, because the update unit runs it as root. A
  missing binary only gives a warning. The check runs at install time
  only.
- It never downloads anything and never writes the binary. `install.sh`
  does that.
- It writes `/etc/gitops-agent/config.toml` from the example config only if
  the file does not exist (mode 0644). It never changes an existing file.
  Thus once a config exists, it ignores `--repo-url`, `--ssh-key` and
  `--clone-path`. It reads `ReadWritePaths=` for the unit back from the
  config on disk.
- It writes `/etc/gitops-agent/installed-version` only if the file does not
  exist, and only when the binary is a release build (`vX.Y.Z`). After
  that, `update` owns the file.
- It writes three units. See [The units](#the-units).
- It runs these systemd steps, in order: `daemon-reload`;
  `enable gitops-agent`; restart `gitops-agent` only if its unit file
  changed, systemd reports `NeedDaemonReload=yes` for it (an earlier run
  wrote the unit but failed before the restart), or it is not running;
  `enable --now gitops-agent-update.timer`;
  restart the timer only if the timer file changed. A second run with the
  same input changes nothing and restarts nothing.
- It validates values before it writes them, because they go into unit and
  config files. `--user` must be a plain user name. Paths and the URL must
  not contain control characters or line breaks. A bad value is an error,
  and nothing is written.
- It warns, and does not fail, if `/usr/local/bin/gitops-agent` is
  missing.
- It does not add the user to the `docker` group, create the SSH key, or
  install `gh`.

## gitops-agent update

```
gitops-agent update [--force] [--bin PATH] [--version-file PATH] [--service NAME]
```

| Flag | Environment variable | Default |
|---|---|---|
| `--force` | none | off |
| `--bin` | `GITOPS_AGENT_BIN` | `/usr/local/bin/gitops-agent` |
| `--version-file` | `GITOPS_AGENT_VERSION_FILE` | `/etc/gitops-agent/installed-version` |
| `--service` | `GITOPS_AGENT_SERVICE` | `gitops-agent` |

Three environment variables have no flag:

| Environment variable | Meaning | Default |
|---|---|---|
| `GITOPS_AGENT_REPO` | `owner/name` of the GitHub repository to take releases from | `oddsund/gitops-agent` |
| `GITOPS_AGENT_SKIP_ATTESTATION` | `1` skips the provenance check. Any other value does not. | unset |
| `GITHUB_TOKEN_FILE` | File that holds a GitHub token | `/etc/gitops-agent/github-token` |

The order is: flag, then environment variable, then default. An empty
environment variable counts as unset. `--bin` and `--version-file` must be
absolute paths.

`update` does these steps:

1. It asks the GitHub API for the latest release tag. The tag must look
   like `vX.Y.Z`.
2. It reads the version file. A missing file, or a value that is not a
   release version (such as `dev`), means "update".
3. It compares the two versions.
   - Same version: it logs `already on vX.Y.Z, nothing to do` and exits 0.
   - Installed version newer than the latest release: it logs a warning,
     does not downgrade, and exits 0.
   - Older: it continues.
   - `--force` continues in the first two cases.
4. It downloads the binary into the directory of `--bin` and checks the
   checksum.
5. It checks the build provenance. See
   [What gets verified](#what-gets-verified).
6. It renames the new file over the binary (atomic). Then it writes the
   version file: `vX.Y.Z` and a newline, mode 0644.
7. It runs `systemctl restart <service>`.

The failure rules:

- A failure before step 6 (download, checksum, provenance) leaves the old
  binary and the version file untouched. `update` removes the temporary
  file.
- If step 7 fails, the new binary and the new version file stay. The
  command exits 1 and logs the error. Run `systemctl restart gitops-agent`
  by hand. If you run `update` again, it says "already on" and does not
  retry the restart.
- If the version file cannot be written after the rename, `update` exits 1
  and does not restart the service. The new binary is on disk, but the old
  code keeps running. The next run repeats the update and restarts the
  service. If you cannot wait, run `systemctl restart gitops-agent` by
  hand.

Other facts:

- The command needs root. It exits 0 on success, on "already on", in the
  no-downgrade case, and for `-h`. It exits 1 on any error. It takes no
  positional arguments.
- The token in `GITHUB_TOKEN_FILE` is optional. `update` uses it for GitHub
  API rate limits and passes it to `gh` as `GH_TOKEN`. It never sends the
  token to the download hosts, never puts it on a command line, and never
  logs it. It does not enable private repositories (see
  [Limitations](#limitations)).
- Logs go to stderr with the prefix `gitops-agent update:`. Under the
  timer, read them with `journalctl -u gitops-agent-update`.
- You can run it by hand at any time: `sudo gitops-agent update`.

## The units

`gitops-agent install` writes three files to `/etc/systemd/system/`. They
have mode 0644. `install` rewrites each one atomically, and only when the
content changed.

- `gitops-agent.service` ([source](../systemd/gitops-agent.service)) is the
  agent. `install` renders it from an embedded template, with `User=` and
  `ReadWritePaths=` filled in. It runs as an unprivileged user that is a
  member of the `docker` group, because the agent talks to the docker
  socket. The unit gives the agent two directories: `StateDirectory=` for
  `/var/lib/gitops-agent` and `RuntimeDirectory=` for `/run/gitops-agent`.
  `ReadWritePaths=` covers the clone directory. Everything else that the
  unit can see is read-only. The unit file has a comment for each
  sandboxing directive and the reason for it. This sandboxing limits the
  damage from a bug in the agent. It does not defend against a hostile
  repository. See [Security model](security.md).
- `gitops-agent-update.service`
  ([source](../systemd/gitops-agent-update.service)) is a oneshot unit. It
  runs as root and executes `/usr/local/bin/gitops-agent update`. It waits
  for `network-online.target`. It has no `Environment=` line. To set the
  environment variables above, use a drop-in: run
  `systemctl edit gitops-agent-update.service`, then add `[Service]` and
  `Environment=GITOPS_AGENT_SKIP_ATTESTATION=1`.
- `gitops-agent-update.timer`
  ([source](../systemd/gitops-agent-update.timer)) sets
  `OnCalendar=daily`, `RandomizedDelaySec=30m` and `Persistent=true`. The
  random delay keeps many hosts from calling GitHub at the same moment. To
  check the timer, run `systemctl list-timers gitops-agent-update.timer`.

The units come from the binary, not from the config repository. If you
edit a unit by hand, the next `install` overwrites it. Use drop-ins for
local changes.

CAUTION: The update unit runs as root. It replaces a file in
`/usr/local/bin` and restarts a system service, and the unprivileged agent
user can do neither. To turn automatic updates off, run
`sudo systemctl disable --now gitops-agent-update.timer`. `install`
enables the timer again the next time it runs. See
[Security model](security.md) for the trade.

Both services expect the binary at `/usr/local/bin/gitops-agent` and the
config at `/etc/gitops-agent/config.toml`. These paths are fixed.

## Which version is installed

`gitops-agent -version` prints the release tag that the build put into the
binary. A plain `go build` reports `dev`. So does
`go install ...@latest`.

The version file `/etc/gitops-agent/installed-version` holds the tag that
`update` last installed. Other tools may read it. Its format is `vX.Y.Z`
and a newline.

## Limitations

- Releases are built for linux/arm64 only. `install.sh` stops with an error
  on any other platform. `gitops-agent update` looks for the arm64 asset on
  arm64 hosts and for an asset that is not published on amd64, so on amd64
  it fails with a download error. If you build from source on another
  platform, turn the timer off:
  `sudo systemctl disable --now gitops-agent-update.timer`. Publishing
  amd64 releases is tracked in the issue tracker.
- Private forks are not supported. `GITOPS_AGENT_REPO` can point at a
  public fork that publishes the same asset names and attestations. The
  GitHub token is for API rate limits only. The download is always
  anonymous, so a private repository fails with a 404.
- Only plain release tags are installed: `vX.Y.Z`. Pre-release tags such as
  `v1.2.3-rc1` are never installed. GitHub's "latest release" skips
  pre-releases and drafts, and the tag check rejects anything else.
- `update` never downgrades. If the installed version is newer than the
  latest release, it logs a warning and exits 0. `--force` overrides this.
- A failed restart after an update is not rolled back. See the failure
  rules under [gitops-agent update](#gitops-agent-update).
- The tools do not remove anything. `install` leaves old files in place
  (see the migration section below).

## Migrating a host provisioned the old way

Older installs used a shell script and a library, copied to the host by a
provisioning script:

- `/usr/local/sbin/gitops-agent-update`, a root-owned copy of the old
  `update.bash`
- `/usr/local/lib/gitops-agent/github-release.bash`, the library that
  script sourced
- a `gitops-agent-update.service` unit with
  `Environment=GITHUB_RELEASE_LIB=/usr/local/lib/gitops-agent/github-release.bash`
  and `ExecStart=/usr/local/sbin/gitops-agent-update`

### If you do nothing

Nothing breaks. The two copied files do not depend on this repository. The
daily timer keeps running the old script. It fetches the latest release,
verifies it, replaces the binary, and restarts the service. The new binary
already contains `gitops-agent update` and `gitops-agent install`, but the
old timer does not call them. Your host keeps updating the old way, and
keeps the old way's problems.

### How to migrate

Run the install script once, as in [Install with the script](#install-with-the-script). On a host that
already has a config, you do not need `--repo-url`:

```bash
bash install.sh --user pi
```

Replace `pi` with the user that runs the agent. This happens:

- Your `/etc/gitops-agent/config.toml` stays as it is.
- `gitops-agent.service`, `gitops-agent-update.service` and
  `gitops-agent-update.timer` are rewritten. The update unit now runs
  `/usr/local/bin/gitops-agent update` and has no `Environment=` line.
- The agent restarts only if its unit file changed, systemd reported
  `NeedDaemonReload=yes` for it, or it was not running. The timer restarts only if the timer file changed.
- `/etc/gitops-agent/installed-version` is not touched. Its format has not
  changed.
- If the binary is already the latest release, the script skips the
  download and does not re-verify the installed binary.
- If the old install left `/usr/local/bin/gitops-agent` owned by a user
  other than root, or writable by group or others, `gitops-agent install`
  refuses to continue. Fix the owner and mode, then run the script again.

Check the result:

```bash
systemctl cat gitops-agent-update.service
sudo systemctl start gitops-agent-update.service
journalctl -u gitops-agent-update -n 20
```

`ExecStart=` must be `/usr/local/bin/gitops-agent update`, and the log
should say `already on`.

If you set `GITOPS_AGENT_SKIP_ATTESTATION=1` or other variables in the old
unit, the rewrite removes them. Set them again with a drop-in. See
[The units](#the-units).

### What to remove

Neither the install script nor `gitops-agent install` deletes the old
files. After you check that the new unit works, remove them:

```bash
sudo rm /usr/local/sbin/gitops-agent-update
sudo rm /usr/local/lib/gitops-agent/github-release.bash
sudo rmdir /usr/local/lib/gitops-agent
```

`rmdir` only succeeds if the directory is empty. That is what you want.

### If the unit still points at the old path

If the old file was replaced by the current `systemd/update.bash` but the
unit was not rewritten, that file forwards to
`/usr/local/bin/gitops-agent update` and prints a deprecation notice. It
refuses to run a binary that is too old to have the `update` subcommand,
and tells you to run `install.sh`. `update.bash` is deprecated and will be
removed in a later release. Do not rely on it. Run `install.sh` instead.

### Provisioning scripts you maintain

If your own provisioning script installs these files or sources the
library, change it to call `scripts/install.sh` (or to install the binary
and run `gitops-agent install`). The library will be removed in a later
release.

[releases]: https://github.com/oddsund/gitops-agent/releases
[attestations]: https://docs.github.com/en/actions/security-guides/using-artifact-attestations-to-establish-provenance-for-builds
[gh]: https://cli.github.com/
