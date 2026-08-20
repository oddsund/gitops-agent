#!/usr/bin/env bash
# Checks GitHub for a newer gitops-agent release than what's installed, and
# if there is one, downloads and installs it and restarts the service.
#
# This is the source copy, tracked in this repo. It is not necessarily what
# runs on a host: gitops-agent-update.service is set up to execute an
# installed copy of this script (see that unit's own comments for why), so
# editing this file has no effect on a host until that installed copy is
# refreshed.
set -euo pipefail
IFS=$'\n\t'

REPO="${GITOPS_AGENT_REPO:-oddsund/gitops-agent}"
GITOPS_AGENT_BIN="${GITOPS_AGENT_BIN:-/usr/local/bin/gitops-agent}"
GITOPS_AGENT_VERSION_FILE="${GITOPS_AGENT_VERSION_FILE:-/etc/gitops-agent/installed-version}"
GITOPS_AGENT_SERVICE="${GITOPS_AGENT_SERVICE:-gitops-agent}"
# Only needed if $REPO is a private fork; the public repo works fine
# without one. See scripts/lib/github-release.bash.
GITHUB_TOKEN_FILE="${GITHUB_TOKEN_FILE:-/etc/gitops-agent/github-token}"
# Build provenance is verified with `gh attestation verify` when gh is
# on PATH -- set GITOPS_AGENT_SKIP_ATTESTATION=1 to skip it and rely on
# the checksum alone. See scripts/lib/github-release.bash.

# Default to this repo's own copy of the release helper, resolved relative
# to this script -- that way a plain checkout works with no setup. Override
# with GITHUB_RELEASE_LIB if your install process places the helper
# somewhere else (for example, alongside a root-owned copy of this script).
SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" >/dev/null 2>&1 && pwd -P)"
GITHUB_RELEASE_LIB="${GITHUB_RELEASE_LIB:-$SCRIPT_DIR/../scripts/lib/github-release.bash}"

log() { echo "$*" >&2; }

if [ ! -f "$GITHUB_RELEASE_LIB" ]; then
  echo "Error: $GITHUB_RELEASE_LIB not found. Set GITHUB_RELEASE_LIB to point at github-release.bash." >&2
  exit 1
fi
# shellcheck source=scripts/lib/github-release.bash
source "$GITHUB_RELEASE_LIB"

# Map the host architecture to the release asset naming convention
# (gitops-agent-linux-<arch>). Add more cases here as more architectures
# get published releases.
case "$(uname -m)" in
  aarch64|arm64) asset_arch="arm64" ;;
  x86_64|amd64) asset_arch="amd64" ;;
  *) echo "Error: no release asset for this architecture ($(uname -m))" >&2; exit 1 ;;
esac
asset_name="gitops-agent-linux-${asset_arch}"

if [ "$(id -u)" -eq 0 ]; then
  SUDO=""
else
  command -v sudo >/dev/null 2>&1 || { echo "Error: not running as root and 'sudo' isn't installed" >&2; exit 1; }
  SUDO="sudo"
fi

github_token=""
if [ -f "$GITHUB_TOKEN_FILE" ]; then
  github_token="$($SUDO cat "$GITHUB_TOKEN_FILE")"
fi

log "Checking for a newer gitops-agent release..."
# Ask the GitHub API for the latest release tag rather than relying on a
# git clone being present: this script may run as root with no clone of
# its own, and the API is what we're already talking to below to resolve
# release assets.
auth_header=()
if [ -n "$github_token" ]; then
  auth_header=(-H "Authorization: Bearer $github_token")
fi
latest_tag="$(curl -fsSL --proto '=https' --tlsv1.2 \
  "${auth_header[@]}" -H "Accept: application/vnd.github+json" \
  "https://api.github.com/repos/${REPO}/releases/latest" \
  | jq -r '.tag_name // empty')"
if [ -z "$latest_tag" ]; then
  echo "Error: could not determine the latest release tag from the GitHub API." >&2
  exit 1
fi
case "$latest_tag" in
  v*) : ;;
  *) echo "Error: latest release tag '$latest_tag' doesn't look like a version tag (expected v*)" >&2; exit 1 ;;
esac

installed_tag=""
if [ -f "$GITOPS_AGENT_VERSION_FILE" ]; then
  installed_tag="$($SUDO cat "$GITOPS_AGENT_VERSION_FILE")"
fi

if [ "$latest_tag" = "$installed_tag" ]; then
  log "Already on $latest_tag, nothing to do."
  exit 0
fi

log "Installed: ${installed_tag:-<none>}. Latest: $latest_tag. Updating."

workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT

log "Downloading and verifying $asset_name from $latest_tag (checksum, plus build provenance if gh is installed)"
github_release_fetch_verified "$REPO" "$latest_tag" "$github_token" \
  "$asset_name" "$workdir/gitops-agent"
chmod +x "$workdir/gitops-agent"

# install(1) writes to a temp file in the destination directory and renames
# it into place, so this is safe even while the old binary is running --
# gitops-agent won't see the new file until it's restarted below.
$SUDO install -o root -g root -m 0755 "$workdir/gitops-agent" "$GITOPS_AGENT_BIN"
echo "$latest_tag" | $SUDO tee "$GITOPS_AGENT_VERSION_FILE" >/dev/null

log "Restarting $GITOPS_AGENT_SERVICE."
$SUDO systemctl restart "$GITOPS_AGENT_SERVICE"
log "Updated to $latest_tag."
