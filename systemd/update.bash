#!/usr/bin/env bash
# Deprecated. Update.bash used to do the whole update itself --
# checking GitHub, downloading, verifying (via
# scripts/lib/github-release.bash), installing, restarting. All of that
# now lives in the gitops-agent binary itself, as the "update" subcommand
# (see internal/selfupdate); this script just execs it.
#
# It exists only for the migration window: a host provisioned before
# this change has gitops-agent-update.service's ExecStart= pointing at a
# root-owned copy of this script (see that unit's own history), and this
# keeps such a host updating with no action needed on its end. Re-run
# scripts/install.sh to move ExecStart= to `gitops-agent update` directly
# and stop seeing the notice below.
set -euo pipefail
IFS=$'\n\t'

BIN_PATH="${GITOPS_AGENT_BIN:-/usr/local/bin/gitops-agent}"

echo "update.bash is deprecated -- delegating to '$BIN_PATH update'. Re-run scripts/install.sh to update this host's systemd units and stop seeing this message." >&2

exec "$BIN_PATH" update
