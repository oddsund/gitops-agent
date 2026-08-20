#!/usr/bin/env bash
# Shared helpers for downloading and checksum-verifying gitops-agent release
# assets from GitHub.
#
# By default these talk to the plain, unauthenticated
# releases/download/<tag>/<asset> URLs, which work for any public repo and
# need no credentials. If a GitHub token is passed in, they instead go
# through the GitHub API's asset-id endpoints, which also work against a
# private fork -- pass a token only if you're running this against one.
#
# Sourced, not executed: `set -euo pipefail` and friends are the caller's
# responsibility.

# github_release_asset_id REPO TAG TOKEN ASSET_NAME
# Prints the numeric asset id for ASSET_NAME in REPO's release TAG on
# stdout, or nothing if no such asset exists. Requires a token (see file
# header); only used on the authenticated path.
github_release_asset_id() {
  local repo="$1" tag="$2" token="$3" asset_name="$4"
  curl -fsSL --proto '=https' --tlsv1.2 \
    -H "Authorization: Bearer $token" -H "Accept: application/vnd.github+json" \
    "https://api.github.com/repos/${repo}/releases/tags/${tag}" \
    | jq -r --arg name "$asset_name" '.assets[] | select(.name == $name) | .id'
}

# github_release_download_asset REPO ASSET_ID TOKEN DEST
# Downloads asset ASSET_ID from REPO to DEST via the API's asset endpoint,
# which (unlike the plain releases/download URL) works for a private repo.
github_release_download_asset() {
  local repo="$1" asset_id="$2" token="$3" dest="$4"
  curl -f -S -L --proto '=https' --tlsv1.2 \
    -H "Authorization: Bearer $token" -H "Accept: application/octet-stream" \
    -o "$dest" \
    "https://api.github.com/repos/${repo}/releases/assets/${asset_id}"
}

# github_release_download_public URL DEST
# Downloads URL (a plain releases/download URL) to DEST, unauthenticated.
# Returns non-zero (and leaves no partial DEST) if the asset doesn't exist
# or the download otherwise fails. Not silent: -S surfaces curl's own error
# on failure, and dropping -s means a slow download still shows curl's
# progress meter on a terminal instead of going quiet for however long it
# takes.
github_release_download_public() {
  local url="$1" dest="$2"
  curl -f -S -L --proto '=https' --tlsv1.2 -o "$dest" "$url"
}

# github_release_fetch_verified REPO TAG TOKEN ASSET_NAME DEST
# Downloads ASSET_NAME from REPO's release TAG to DEST, then verifies it
# against the "${ASSET_NAME}.sha256" asset published alongside it. TOKEN
# may be empty, in which case both files come from the public
# releases/download URLs; otherwise the authenticated API asset-id path is
# used (see file header). Aborts loudly and removes DEST on any failure --
# a missing checksum asset, a download failure, or a mismatch -- so a
# caller can never end up installing an unverified or corrupt binary just
# because it didn't check the exit code.
github_release_fetch_verified() {
  local repo="$1" tag="$2" token="$3" asset_name="$4" dest="$5"

  local checksum_file
  checksum_file="$(mktemp)"

  if [ -n "$token" ]; then
    local asset_id checksum_id
    asset_id="$(github_release_asset_id "$repo" "$tag" "$token" "$asset_name")"
    if [ -z "$asset_id" ]; then
      echo "Error: release $tag has no $asset_name asset" >&2
      rm -f "$checksum_file"
      return 1
    fi

    checksum_id="$(github_release_asset_id "$repo" "$tag" "$token" "${asset_name}.sha256")"
    if [ -z "$checksum_id" ]; then
      echo "Error: release $tag has no ${asset_name}.sha256 asset -- refusing to install an unverified binary" >&2
      rm -f "$checksum_file"
      return 1
    fi

    echo "Downloading $asset_name from $repo ($tag) via the GitHub API..." >&2
    if ! github_release_download_asset "$repo" "$asset_id" "$token" "$dest"; then
      echo "Error: failed to download $asset_name from release $tag" >&2
      rm -f "$dest" "$checksum_file"
      return 1
    fi
    if ! github_release_download_asset "$repo" "$checksum_id" "$token" "$checksum_file"; then
      echo "Error: failed to download ${asset_name}.sha256 from release $tag" >&2
      rm -f "$dest" "$checksum_file"
      return 1
    fi
  else
    local url checksum_url
    url="https://github.com/${repo}/releases/download/${tag}/${asset_name}"
    checksum_url="https://github.com/${repo}/releases/download/${tag}/${asset_name}.sha256"

    echo "Downloading $asset_name from $url..." >&2
    if ! github_release_download_public "$url" "$dest"; then
      echo "Error: release $tag has no $asset_name asset" >&2
      rm -f "$dest" "$checksum_file"
      return 1
    fi
    if ! github_release_download_public "$checksum_url" "$checksum_file"; then
      echo "Error: release $tag has no ${asset_name}.sha256 asset -- refusing to install an unverified binary" >&2
      rm -f "$dest" "$checksum_file"
      return 1
    fi
  fi

  # The published file is sha256sum's own two-column "hash  filename"
  # format, but the filename column is whatever the release build produced
  # it as and won't match DEST's actual path, so pull out just the hash
  # instead of feeding the file straight to `sha256sum -c`.
  local expected actual
  expected="$(awk '{print $1}' "$checksum_file")"
  rm -f "$checksum_file"
  if [ -z "$expected" ]; then
    echo "Error: could not parse a checksum out of ${asset_name}.sha256" >&2
    rm -f "$dest"
    return 1
  fi

  actual="$(sha256sum "$dest" | awk '{print $1}')"
  if [ "$expected" != "$actual" ]; then
    echo "Error: checksum mismatch for $asset_name: expected $expected, got $actual -- leaving the currently installed binary untouched" >&2
    rm -f "$dest"
    return 1
  fi

  echo "checksum verified for $asset_name ($actual)" >&2
}
