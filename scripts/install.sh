#!/usr/bin/env bash
# Downloads and verifies the latest gitops-agent release binary, installs it
# to /usr/local/bin/gitops-agent, then execs `gitops-agent install "$@"`.
# This is the one step that cannot live in the binary.
#
# Usage (as your normal user, NOT under sudo):
#   bash install.sh --repo-url git@github.com:you/config.git \
#     [--user NAME] [--ssh-key PATH] [--clone-path PATH]
# Every argument is passed through unchanged to `gitops-agent install`.
# The script calls sudo itself, and only for the steps that need root, so
# the download and `gh attestation verify` run with your own gh login. If
# you do run it as root (sudo bash install.sh), root has no gh login and
# sudo strips your environment: pass GH_TOKEN or
# GITOPS_AGENT_SKIP_ATTESTATION through it, as in
# `sudo GH_TOKEN=... bash install.sh ...`.
#
# TRUST: this script cannot verify itself. Download it, read it, then run
# it; do not pipe it into a shell. The .sha256 file comes from the same
# origin as the binary, so it only guards against corruption, not against a
# compromised release. The build provenance attestation
# (`gh attestation verify`) is the real provenance check; it pins
# .github/workflows/release.yml in the configured repo, the release tag and
# GitHub-hosted runners. Without `gh` this script falls back to the checksum
# alone and says so.
#
# Environment:
#   GITOPS_AGENT_REPO              release source, default oddsund/gitops-agent
#   GITOPS_AGENT_SKIP_ATTESTATION  set to 1 to skip the provenance check
# There is deliberately no override for the install path: `gitops-agent
# install` and the shipped units hard-code /usr/local/bin/gitops-agent.
set -euo pipefail
# TRAP: IFS has no space, so unquoted expansions and `read` do not split on
# spaces. Every expansion is quoted; parse with awk, never read or word
# splitting.
IFS=$'\n\t'

REPO="${GITOPS_AGENT_REPO:-oddsund/gitops-agent}"
BIN_PATH=/usr/local/bin/gitops-agent
# Same cap as internal/selfupdate. curl enforces it only when the server
# sends a length, so the size is checked again after the download.
MAX_ASSET_BYTES=209715200 # 200 MiB

log() { echo "$*" >&2; }
die() {
  echo "Error: $*" >&2
  exit 1
}

usage() {
  cat <<'USAGE'
Usage: install.sh --repo-url URL [--user NAME] [--ssh-key PATH] [--clone-path PATH]

Downloads the latest gitops-agent release, verifies it, installs it to
/usr/local/bin/gitops-agent and runs 'gitops-agent install' with the
arguments given here. Run it as your normal user, not with sudo: it calls
sudo itself for the steps that need root. Read this script before you run it.
USAGE
}

need() {
  local c
  for c in "$@"; do
    command -v "$c" >/dev/null 2>&1 || die "required command not found: $c"
  done
}

workdir=""
cleanup() {
  # The explicit return matters: a false test as the last command of an EXIT
  # trap would otherwise change the exit status under set -e.
  [ -z "$workdir" ] || rm -rf -- "$workdir"
  return 0
}

# fetch URL DEST [EXTRA_CURL_ARGS...]
# -q must stay the first argument: it stops curl from reading ~/.curlrc
# (root's, when run under sudo), which could set "insecure" or a proxy and
# defeat the TLS pinning below.
# No --silent and no --progress-bar: the default meter is what shows
# progress over a slow SSH session, and it degrades to plain lines when
# stderr is not a tty. --proto-redir is required because --proto alone does
# not constrain the redirect to objects.githubusercontent.com.
fetch() {
  local url="$1" dest="$2"
  shift 2
  curl -q --fail --show-error --location \
    --proto '=https' --proto-redir '=https' --tlsv1.2 \
    --connect-timeout 15 --retry 3 \
    "$@" \
    --output "$dest" "$url"
}

fetch_asset() {
  fetch "$1" "$2" --max-filesize "$MAX_ASSET_BYTES"
}

# The only silent curl: a sub-second JSON lookup captured into a variable.
# The progress meter would pollute the capture's stderr; --show-error still
# reports failures. The sed parse is sloppy on purpose (no jq on a small
# box) and safe only because the caller validates the result against
# vX.Y.Z before it reaches a URL or argv. pipefail makes a curl failure
# fail this function.
latest_tag() {
  curl -q --fail --silent --show-error --location \
    --proto '=https' --proto-redir '=https' --tlsv1.2 \
    --connect-timeout 15 --retry 3 \
    --header 'Accept: application/vnd.github+json' \
    "https://api.github.com/repos/${REPO}/releases/latest" |
    sed -n 's/^[[:space:]]*"tag_name":[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1
}

# Help comes before any work: otherwise -h would download and install a
# binary just to print help.
for a in "$@"; do
  case "$a" in
  -h | -help | --help)
    usage
    exit 0
    ;;
  esac
done

[ "$(uname -s)" = Linux ] || die "gitops-agent releases are published for Linux only; this host is $(uname -s)"
# An unmapped architecture must not guess an asset name that is not
# published. Keep this list in step with the release workflow matrix and
# assetNameFor in internal/selfupdate/config.go.
case "$(uname -m)" in
aarch64 | arm64) asset_name=gitops-agent-linux-arm64 ;;
x86_64 | amd64) asset_name=gitops-agent-linux-amd64 ;;
*) die "gitops-agent releases are published for linux/arm64 and linux/amd64 only; this host is $(uname -m)" ;;
esac

# An array, not a string, because of the IFS trap. Download and verification
# run as the invoking user (least privilege, and the user's own gh login can
# be used); only the install steps and the final exec use sudo.
sudo_cmd=()
if [ "$(id -u)" -ne 0 ]; then
  command -v sudo >/dev/null 2>&1 || die "not running as root and 'sudo' is not installed; install sudo or run as root"
  sudo_cmd=(sudo)
fi

need curl sed awk head wc sha256sum install mktemp uname id mv rm

[[ "$REPO" =~ ^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$ ]] || die "GITOPS_AGENT_REPO must look like owner/name, got: $REPO"

log "Resolving the latest release of ${REPO}..."
tag="$(latest_tag)" || die "could not query https://api.github.com/repos/${REPO}/releases/latest"
[ -n "$tag" ] || die "no release found for ${REPO} (the GitHub API returned no tag_name)"
[[ "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "latest release tag '${tag}' is not a version tag (expected vX.Y.Z)"
log "Latest release: ${tag}"

# Re-running this script is the documented migration and re-provision path.
# Skipping the download when the installed binary already is the latest tag
# avoids replacing a good binary (gitops-agent install refreshes
# installed-version either way). It only executes the already-installed,
# root-owned binary.
skip_download=0
if [ -x "$BIN_PATH" ]; then
  installed="$("$BIN_PATH" -version 2>/dev/null)" || installed=""
  if [ "$installed" = "$tag" ]; then
    log "${BIN_PATH} is already ${tag}, skipping download (the installed binary was not re-verified)"
    skip_download=1
  fi
fi

if [ "$skip_download" -eq 0 ]; then
  workdir="$(mktemp -d "${TMPDIR:-/tmp}/gitops-agent-install.XXXXXX")"
  trap cleanup EXIT
  # Without these the EXIT trap would not run on Ctrl-C or kill.
  trap 'exit 130' INT
  trap 'exit 143' TERM

  base="https://github.com/${REPO}/releases/download/${tag}"
  log "Downloading ${asset_name} ${tag}..."
  fetch_asset "${base}/${asset_name}" "${workdir}/${asset_name}" || die "failed to download ${asset_name} from release ${tag}"
  fetch "${base}/${asset_name}.sha256" "${workdir}/${asset_name}.sha256" || die "failed to download ${asset_name}.sha256 from release ${tag}; refusing to install an unverified binary"

  size="$(wc -c <"${workdir}/${asset_name}" | awk '{ print $1 }')"
  [ "$size" -le "$MAX_ASSET_BYTES" ] || die "${asset_name} is ${size} bytes, over the ${MAX_ASSET_BYTES} byte limit; nothing was installed"

  # Only the hash column is used; the filename column is whatever the
  # release build wrote.
  expected="$(awk 'NR == 1 && NF == 2 { print $1 }' "${workdir}/${asset_name}.sha256")"
  [[ "$expected" =~ ^[0-9a-fA-F]{64}$ ]] || die "could not parse a SHA-256 digest out of ${asset_name}.sha256"
  expected="${expected,,}"
  actual="$(sha256sum "${workdir}/${asset_name}" | awk '{ print $1 }')"
  [ "$actual" = "$expected" ] || die "checksum mismatch for ${asset_name}: expected ${expected}, got ${actual}; nothing was installed"
  log "Checksum verified: ${actual}"

  # First mutation of the host, and not yet live: the binary only becomes
  # gitops-agent after the checks below pass. GNU install alone unlinks then writes, so
  # stage and rename to replace the binary atomically. A crash in between
  # leaves a stray gitops-agent.new, which is harmless; /usr/local/bin is
  # root-owned, so the fixed staging name is not a symlink-race vector.
  staged="${BIN_PATH}.new"
  log "Installing ${BIN_PATH}..."
  ${sudo_cmd[@]+"${sudo_cmd[@]}"} install -o root -g root -m 0755 "${workdir}/${asset_name}" "$staged"
  # TRAP: the workdir belongs to the invoking user, who could swap the file
  # between the checks above and the copy. Hash the root-owned staged copy
  # and compare with the digest that was verified before the rename.
  staged_hash="$(${sudo_cmd[@]+"${sudo_cmd[@]}"} sha256sum "$staged" | awk '{ print $1 }')"
  if [ "$staged_hash" != "$actual" ]; then
    ${sudo_cmd[@]+"${sudo_cmd[@]}"} rm -f -- "$staged"
    die "${staged} changed after verification (got ${staged_hash}, verified ${actual}); nothing was installed"
  fi
  # Attestation runs on the root-owned staged copy, not on the user-writable
  # download: the user can read it but not modify it, so the bytes verified
  # here are the bytes that get renamed into place. gh still runs as the
  # invoking user so their own gh login is used.
  # The skip variable is checked before gh so the log names the reason that
  # actually applies.
  if [ "${GITOPS_AGENT_SKIP_ATTESTATION:-}" = 1 ]; then
    log "WARNING: skipping build provenance verification (GITOPS_AGENT_SKIP_ATTESTATION=1); the checksum alone does not prove who built this binary"
  elif ! command -v gh >/dev/null 2>&1; then
    log "WARNING: gh not found, skipping build provenance verification; the checksum alone does not prove who built this binary. Install the GitHub CLI and re-run to verify provenance."
  else
    log "Verifying build provenance with gh..."
    # $REPO and $tag were regex-validated above, so they are safe in argv.
    # release.yml runs only on tag push, so the attested ref is the tag.
    # Keep the workflow path in step with internal/selfupdate/attestation.go.
    if ! gh attestation verify "$staged" \
      -R "$REPO" \
      --signer-workflow "${REPO}/.github/workflows/release.yml" \
      --source-ref "refs/tags/${tag}" \
      --deny-self-hosted-runners; then
      ${sudo_cmd[@]+"${sudo_cmd[@]}"} rm -f -- "$staged"
      die "build provenance verification failed for ${asset_name}; nothing was installed. If gh reports an unknown flag, upgrade gh to >= 2.68.0. If gh is not logged in, run 'gh auth login'. Run this script as your normal user, not with sudo, so your own gh login is used; if you must run it as root, pass the variables through sudo: 'sudo GH_TOKEN=... bash install.sh ...' or, to proceed on the checksum alone, 'sudo GITOPS_AGENT_SKIP_ATTESTATION=1 bash install.sh ...'"
    fi
    log "Build provenance verified"
  fi
  ${sudo_cmd[@]+"${sudo_cmd[@]}"} mv -f -- "$staged" "$BIN_PATH"
fi

# TRAP: exec does not run EXIT traps, so clean up first.
cleanup
trap - EXIT
# Argument values stay out of the log on purpose.
log "Running: ${BIN_PATH} install (with the arguments you passed)"
exec ${sudo_cmd[@]+"${sudo_cmd[@]}"} "$BIN_PATH" install "$@"
