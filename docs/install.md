# Install

## From source

```
go install github.com/oddsund/gitops-agent/cmd/gitops-agent@latest
```

## From a release

Prebuilt binaries are on the [releases page][releases]. Releases are built
for `linux/arm64` only. If you need another platform, open an issue.

Every release publishes three things: the binary, a `.sha256` checksum
file, and a [build provenance attestation][attestations]. The attestation
records which workflow run and which commit produced the binary.

Verify both before you run a downloaded binary as root:

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

## systemd

This repository ships two units.

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

### The optional self-update timer

[`systemd/gitops-agent-update.service`](../systemd/gitops-agent-update.service)
and [`.timer`](../systemd/gitops-agent-update.timer) run
[`systemd/update.bash`](../systemd/update.bash) once a day. The timer adds
a random delay, so that many hosts do not call GitHub at the same moment.

`update.bash` asks the GitHub API for the newest release. If that release
is newer than the installed one, the script downloads it, verifies it with
[`scripts/lib/github-release.bash`](../scripts/lib/github-release.bash),
installs the binary, and restarts `gitops-agent.service`.

Verification here follows the same two checks as the manual steps above:
checksum, then attestation. The checksum check always runs. If `gh` is on
the host, the attestation check runs too. If `gh` is missing, the script
logs a line that says it skipped the attestation check. It then
continues with the checksum check alone. A missing optional tool does
not fail the update.

To skip the attestation check even when `gh` is present, set
`GITOPS_AGENT_SKIP_ATTESTATION=1` in the environment of the unit. In
both cases -- a checksum mismatch or a failed attestation -- the update
aborts and the currently installed binary stays in place.

CAUTION: This unit runs as root. It installs a binary and restarts a
system service, and the unprivileged agent user cannot do either. Decide
whether you want that before you install the timer, or replace it with
your own update process.

### Paths

Both units expect the binary at `/usr/local/bin/gitops-agent` and the
configuration at `/etc/gitops-agent/config.toml`. If yours are elsewhere,
change `ExecStart=`.

[releases]: https://github.com/oddsund/gitops-agent/releases
[attestations]: https://docs.github.com/en/actions/security-guides/using-artifact-attestations-to-establish-provenance-for-builds
[gh]: https://cli.github.com/
