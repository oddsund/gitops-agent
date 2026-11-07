#!/usr/bin/env bash
# Deprecated. The update logic lives in the binary now: `gitops-agent
# update`. This shim only forwards to it.
#
# It exists for hosts whose gitops-agent-update.service still runs
# /usr/local/sbin/gitops-agent-update, a root-owned copy of this file that
# the old provisioning flow installs. Running `scripts/install.sh` (or
# `gitops-agent install`) on such a host rewrites the unit to call the
# binary directly; after that nothing runs this file.
#
# Not forwarded on purpose: GITHUB_RELEASE_LIB (nothing to source any
# more). GITOPS_AGENT_BIN, GITOPS_AGENT_VERSION_FILE, GITOPS_AGENT_SERVICE,
# GITOPS_AGENT_REPO, GITOPS_AGENT_SKIP_ATTESTATION and GITHUB_TOKEN_FILE
# need no forwarding: `gitops-agent update` reads them from the
# environment itself.
set -euo pipefail
IFS=$'\n\t'

bin="${GITOPS_AGENT_BIN:-/usr/local/bin/gitops-agent}"

[[ "$bin" == /* ]] || { echo "Error: GITOPS_AGENT_BIN must be an absolute path, got: $bin" >&2; exit 1; }
[ -x "$bin" ] || { echo "Error: $bin not found or not executable; run scripts/install.sh to install gitops-agent" >&2; exit 1; }

# TRAP: a binary from before the update subcommand existed does not reject
# "update": its flag parsing stops at the first non-flag argument and it
# starts the reconcile loop, which would hang this oneshot unit as root.
# Ask for -h instead: every build parses flags before it starts anything, so
# an old binary prints its usage and exits 0, and a current one lists its
# subcommands there (pinned by TestDaemonHelpListsUpdateForShim). The output
# goes through a variable because grep -q on a pipe can SIGPIPE the binary
# and trip pipefail.
help="$("$bin" -h 2>&1)" || help=""
case $'\n'"$help"$'\n' in
*$'\n  update\n'*) ;;
*)
  echo "Error: $bin predates the 'update' subcommand; run scripts/install.sh to install a current release" >&2
  exit 1
  ;;
esac

echo "update.bash is deprecated; running '$bin update'. Run scripts/install.sh to switch this host's systemd units to call the binary directly and stop seeing this notice." >&2

exec "$bin" update "$@"
