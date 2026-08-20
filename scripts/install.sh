#!/usr/bin/env bash
# Bootstraps a host: downloads and verifies the latest gitops-agent
# release binary, installs it to /usr/local/bin, then hands off to
# `gitops-agent install` for everything else (config.toml, systemd
# units). This is the one step that can't live in the binary -- it can't
# install itself before it exists. Everything downstream of this script
# is Go: see internal/installer and internal/selfupdate.
#
# Every argument given to this script passes straight through to
# `gitops-agent install` (-repo-url, -user, ...); run
# `gitops-agent install -h` for the full list, or pass -h here.
#
# This script verifies the binary it downloads, but nothing verifies
# this script itself -- download it and read it before running it,
# rather than piping curl straight into a shell. See docs/install.md.
set -euo pipefail
IFS=$'\n\t'

REPO="${GITOPS_AGENT_REPO:-oddsund/gitops-agent}"
BIN_PATH="${GITOPS_AGENT_BIN:-/usr/local/bin/gitops-agent}"

log() { echo "$*" >&2; }

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

log "Resolving the latest gitops-agent release..."
latest_tag="$(curl -fsSL --proto '=https' --tlsv1.2 \
  -H "Accept: application/vnd.github+json" \
  "https://api.github.com/repos/${REPO}/releases/latest" \
  | jq -r '.tag_name // empty')"
if [ -z "$latest_tag" ]; then
  echo "Error: could not determine the latest release tag from the GitHub API." >&2
  exit 1
fi
log "Latest release: $latest_tag"

workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT
dest="$workdir/$asset_name"
checksum_file="$workdir/${asset_name}.sha256"

# -S -L, no -s: a slow download over a home connection shows curl's own
# progress meter instead of going quiet, per this repo's shell script
# rules.
log "Downloading $asset_name from $latest_tag..."
curl -f -S -L --proto '=https' --tlsv1.2 -o "$dest" \
  "https://github.com/${REPO}/releases/download/${latest_tag}/${asset_name}"
curl -f -S -L --proto '=https' --tlsv1.2 -o "$checksum_file" \
  "https://github.com/${REPO}/releases/download/${latest_tag}/${asset_name}.sha256"

# The published file names the asset as the build produced it, which
# never matches $dest's actual path, so pull out just the hash column
# instead of feeding the file to `sha256sum -c`.
expected="$(awk '{print $1}' "$checksum_file")"
if [ -z "$expected" ]; then
  echo "Error: could not parse a checksum out of ${asset_name}.sha256" >&2
  exit 1
fi
actual="$(sha256sum "$dest" | awk '{print $1}')"
if [ "$expected" != "$actual" ]; then
  echo "Error: checksum mismatch for $asset_name: expected $expected, got $actual" >&2
  exit 1
fi
log "checksum verified for $asset_name ($actual)"

# gh is optional: this repo's whole pitch is a single static binary with
# few dependencies, so a missing gh only skips this check (logged) rather
# than failing the bootstrap -- the checksum check already ran either
# way. Set GITOPS_AGENT_SKIP_ATTESTATION=1 to opt out even when gh is
# present.
if ! command -v gh >/dev/null 2>&1; then
  log "gh not found -- skipping build provenance verification (checksum only)"
elif [ "${GITOPS_AGENT_SKIP_ATTESTATION:-}" = "1" ]; then
  log "Skipping build provenance verification (GITOPS_AGENT_SKIP_ATTESTATION=1)"
else
  log "Verifying build provenance for $dest..."
  if ! gh attestation verify "$dest" -R "$REPO"; then
    echo "Error: build provenance verification failed for $dest" >&2
    exit 1
  fi
  log "build provenance verified for $dest"
fi

chmod +x "$dest"
log "Installing to $BIN_PATH"
$SUDO install -o root -g root -m 0755 "$dest" "$BIN_PATH"

log "Handing off to '$BIN_PATH install'"
exec $SUDO "$BIN_PATH" install "$@"
