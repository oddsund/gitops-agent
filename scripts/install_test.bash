#!/usr/bin/env bash
# Tests for scripts/install.sh with fake curl, sudo, gh, uname and id on
# PATH. No network, nothing outside a mktemp sandbox is touched.
#
# Limits: the fakes emulate only the behaviour install.sh depends on. This
# does NOT prove real sudo, real `install -o root`, GitHub or real
# `gh attestation verify` behaviour; the manual VM checklist in the PR
# covers those.
#
# install.sh hard-codes /usr/local/bin/gitops-agent. The test runs a copy
# with that one assignment rewritten into the sandbox, so a test bug can
# never write the real path.
set -euo pipefail

[ "$(uname -s)" = Linux ] || {
  echo "skipping: needs GNU coreutils on Linux"
  exit 0
}
[ "$(id -u)" -ne 0 ] || {
  echo "refusing to run as root"
  exit 1
}

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
real_script="$here/install.sh"
real_bash="$BASH"
real_install="$(command -v install)"

pass=0
failed=0
ok() { pass=$((pass + 1)); }
fail() {
  failed=$((failed + 1))
  echo "FAIL [$case_name]: $*" >&2
}

tools=(bash env sh sed awk head sha256sum install mktemp uname id mv rm wc cat dirname mkdir tr grep cp)
for c in "${tools[@]}"; do
  command -v "$c" >/dev/null 2>&1 || {
    echo "required tool missing: $c" >&2
    exit 1
  }
done

t=""
case_name=""
trap '[ -z "$t" ] || rm -rf -- "$t"' EXIT

# setup NAME: fresh sandbox with fixtures and fakes.
setup() {
  case_name="$1"
  [ -z "$t" ] || rm -rf -- "$t"
  t="$(mktemp -d)"
  mkdir -p "$t/fakebin" "$t/tools" "$t/root/usr/local/bin" "$t/fixtures" "$t/tmp"
  for c in "${tools[@]}"; do
    ln -s "$(command -v "$c")" "$t/tools/$c"
  done
  bindir="$t/root/usr/local/bin"
  bin="$bindir/gitops-agent"

  # install.sh under test, with BIN_PATH moved into the sandbox.
  sed "s#^BIN_PATH=/usr/local/bin/gitops-agent\$#BIN_PATH=$bin#" "$real_script" >"$t/install.sh"
  grep -q "^BIN_PATH=$bin\$" "$t/install.sh" || {
    echo "test setup: could not rewrite BIN_PATH" >&2
    exit 1
  }

  cat >"$t/fixtures/asset" <<'FAKE'
#!/usr/bin/env bash
case "${1:-}" in
-version) echo "${FAKE_INSTALLED_VERSION-}" ;;
install)
  shift
  printf '%s\n' "$@" >"$FAKE_HANDOFF"
  ;;
esac
FAKE
  chmod 0755 "$t/fixtures/asset"
  write_sha "$(sha256sum "$t/fixtures/asset" | awk '{ print $1 }')"

  cat >"$t/fakebin/uname" <<'FAKE'
#!/usr/bin/env bash
case "$1" in
-s) echo Linux ;;
-m) echo "${FAKE_ARCH:-aarch64}" ;;
esac
FAKE
  cat >"$t/fakebin/id" <<'FAKE'
#!/usr/bin/env bash
[ "$1" = -u ] && echo "${FAKE_UID:-1000}"
FAKE
  # Unprivileged install cannot honour "-o root -g root": drop the pair.
  cat >"$t/fakebin/install" <<FAKE
#!/usr/bin/env bash
args=()
while [ "\$#" -gt 0 ]; do
  if [ "\$1" = -o ] || [ "\$1" = -g ]; then shift 2; else args+=("\$1"); shift; fi
done
exec "$real_install" "\${args[@]}"
FAKE
  # Drops the "-o root -g root" pair, then runs the rest as is. With
  # FAKE_TAMPER=1 it also rewrites the install source first, standing in for
  # the invoking user swapping the verified file before the copy.
  cat >"$t/fakebin/sudo" <<'FAKE'
#!/usr/bin/env bash
echo "sudo $*" >>"$FAKE_T/sudo.log"
if [ "${FAKE_TAMPER:-}" = 1 ] && [ "${1:-}" = install ]; then
  echo evil >>"${@: -2:1}"
fi
args=()
while [ "$#" -gt 0 ]; do
  if [ "$1" = -o ] || [ "$1" = -g ]; then
    shift 2
  else
    args+=("$1")
    shift
  fi
done
exec "${args[@]}"
FAKE
  cat >"$t/fakebin/curl" <<'FAKE'
#!/usr/bin/env bash
echo "$*" >>"$FAKE_T/curl.log"
out=""
url=""
while [ "$#" -gt 0 ]; do
  if [ "$1" = --output ]; then
    out="$2"
    shift
  fi
  url="$1"
  shift
done
case "$url" in
*/releases/latest)
  printf '{\n  "tag_name": "%s"\n}\n' "${FAKE_TAG-v1.2.3}"
  ;;
*.sha256)
  [ "${FAKE_NO_SHA:-}" != 1 ] || exit 22
  cp "$FAKE_T/fixtures/asset.sha256" "$out"
  ;;
*/gitops-agent-linux-arm64)
  [ "${FAKE_NO_ASSET:-}" != 1 ] || exit 22
  cp "$FAKE_T/fixtures/asset" "$out"
  ;;
*) exit 22 ;;
esac
FAKE
  chmod 0755 "$t/fakebin/"*
}

write_sha() {
  printf '%s  gitops-agent-linux-arm64\n' "$1" >"$t/fixtures/asset.sha256"
}

with_gh() {
  cat >"$t/fakebin/gh" <<'FAKE'
#!/usr/bin/env bash
echo "$*" >>"$FAKE_T/gh.log"
[ "${FAKE_GH:-}" != fail ]
FAKE
  chmod 0755 "$t/fakebin/gh"
}

# run_install [VAR=value ...] -- ARGS...
# Sets RC, OUT and ERR. The environment is cleared so nothing from the
# host leaks in, and PATH holds only the fakes and the symlinked tools.
run_install() {
  local extra=()
  while [ "$#" -gt 0 ] && [ "$1" != -- ]; do
    extra+=("$1")
    shift
  done
  shift
  rm -f "$t/out" "$t/err"
  RC=0
  env -i PATH="$t/fakebin:$t/tools" HOME="$t" TMPDIR="$t/tmp" \
    FAKE_T="$t" FAKE_HANDOFF="$t/handoff.args" \
    ${extra[@]+"${extra[@]}"} \
    "$real_bash" "$t/install.sh" "$@" >"$t/out" 2>"$t/err" || RC=$?
  OUT="$(cat "$t/out")"
  ERR="$(cat "$t/err")"
}

assert_rc() {
  if [ "$RC" -eq "$1" ]; then ok; else fail "exit status $RC, want $1; stderr: $ERR"; fi
}
assert_err() {
  case "$ERR" in *"$1"*) ok ;; *) fail "stderr lacks '$1'; stderr: $ERR" ;; esac
}
assert_out() {
  case "$OUT" in *"$1"*) ok ;; *) fail "stdout lacks '$1'" ;; esac
}
assert_bindir_empty() {
  if [ -z "$(ls -A "$bindir")" ]; then ok; else fail "bin dir not empty: $(ls -A "$bindir")"; fi
}
assert_no_workdir() {
  if compgen -G "$t/tmp/gitops-agent-install.*" >/dev/null; then fail "workdir left behind"; else ok; fi
}
assert_absent() {
  if [ -e "$1" ]; then fail "$1 exists"; else ok; fi
}
assert_no_download() {
  if grep -qs '/releases/download/' "$t/curl.log"; then fail "a download happened"; else ok; fi
}

# 1. Happy path with gh.
setup happy
with_gh
run_install -- --repo-url "git@x:a b.git" --user pi
assert_rc 0
assert_err "Checksum verified"
assert_err "Build provenance verified"
if cmp -s "$t/fixtures/asset" "$bin"; then ok; else fail "installed binary differs from asset"; fi
if [ "$(stat -c %a "$bin")" = 755 ]; then ok; else fail "mode is $(stat -c %a "$bin"), want 755"; fi
if [ "$(cat "$t/handoff.args")" = "$(printf '%s\n' --repo-url "git@x:a b.git" --user pi)" ]; then ok; else fail "handoff args: $(cat "$t/handoff.args")"; fi
assert_no_workdir
if compgen -G "$bindir/*.new" >/dev/null; then fail "staged .new file left"; else ok; fi
if [ -s "$t/sudo.log" ]; then ok; else fail "sudo was never called"; fi
downloads=0
while IFS= read -r line; do
  case "$line" in "-q "*) ok ;; *) fail "curl -q is not the first argument: $line" ;; esac
  case "$line" in */releases/latest*) continue ;; esac
  downloads=$((downloads + 1))
  for f in "--proto =https" "--proto-redir =https" "--tlsv1.2" "--fail" "--show-error" "--location"; do
    case "$line" in *"$f"*) ok ;; *) fail "curl call lacks '$f': $line" ;; esac
  done
  if printf '%s\n' "${line#-q }" | grep -Eq ' (-s|-q|--silent|--progress-bar)( |$)'; then fail "silent curl for a download: $line"; else ok; fi
done <"$t/curl.log"
if [ "$downloads" -eq 2 ]; then ok; else fail "expected 2 download calls, got $downloads"; fi
if [ "$(cat "$t/gh.log")" = "attestation verify $bin.new -R oddsund/gitops-agent" ]; then ok; else fail "gh argv: $(cat "$t/gh.log")"; fi
if grep -q '^sudo gh ' "$t/sudo.log"; then fail "gh ran through sudo"; else ok; fi

# 2. Checksum mismatch.
setup checksum-mismatch
with_gh
write_sha "$(printf '0%.0s' {1..64})"
run_install -- --repo-url u
assert_rc 1
assert_err "checksum mismatch"
assert_bindir_empty
assert_absent "$t/handoff.args"
assert_no_workdir

# 3. Garbage sha file.
setup garbage-sha
echo "not-a-digest" >"$t/fixtures/asset.sha256"
run_install -- --repo-url u
assert_rc 1
assert_err "could not parse a SHA-256 digest"
assert_bindir_empty

# 4. Sha asset missing.
setup sha-404
run_install FAKE_NO_SHA=1 -- --repo-url u
assert_rc 1
assert_err "refusing to install an unverified binary"
assert_bindir_empty
assert_no_workdir

# 5. Binary asset missing.
setup asset-404
run_install FAKE_NO_ASSET=1 -- --repo-url u
assert_rc 1
assert_err "failed to download gitops-agent-linux-arm64"
assert_bindir_empty

# 6. Bad tags never reach a URL.
setup empty-tag
run_install FAKE_TAG= -- --repo-url u
assert_rc 1
assert_err "no release found"
assert_bindir_empty
for bad in latest v1.2 v1.2.3-rc1 'v1.2.3/../x'; do
  setup "bad-tag $bad"
  run_install "FAKE_TAG=$bad" -- --repo-url u
  assert_rc 1
  assert_err "not a version tag"
  assert_no_download
  assert_bindir_empty
done

# 7. Unsupported architecture fails before any network access.
setup arch
run_install FAKE_ARCH=x86_64 -- --repo-url u
assert_rc 1
assert_err "linux/arm64 only"
if [ ! -s "$t/curl.log" ]; then ok; else fail "network used before the arch check"; fi

# 8. gh present but failing.
setup gh-fails
with_gh
run_install FAKE_GH=fail -- --repo-url u
assert_rc 1
assert_err "build provenance verification failed"
assert_bindir_empty
assert_no_workdir
setup gh-fails-skipped
with_gh
run_install FAKE_GH=fail GITOPS_AGENT_SKIP_ATTESTATION=1 -- --repo-url u
assert_rc 0
assert_err "WARNING: skipping build provenance verification (GITOPS_AGENT_SKIP_ATTESTATION=1)"
if [ ! -s "$t/gh.log" ]; then ok; else fail "gh ran despite the skip variable"; fi

# 9. gh absent.
setup no-gh
run_install -- --repo-url u
assert_rc 0
assert_err "WARNING: gh not found"

# 10. Help.
for h in -h --help; do
  setup "help $h"
  run_install -- "$h"
  assert_rc 0
  assert_out "Usage: install.sh"
  if [ ! -s "$t/curl.log" ]; then ok; else fail "network used for $h"; fi
  assert_bindir_empty
done

# 11. Already installed at the latest tag skips the download.
setup already-installed
cp "$t/fixtures/asset" "$bin"
echo "# marker" >>"$bin"
cp "$bin" "$t/preplaced"
run_install FAKE_INSTALLED_VERSION=v1.2.3 -- --repo-url u
assert_rc 0
assert_err "skipping download"
if [ "$(wc -l <"$t/curl.log")" -eq 1 ]; then ok; else fail "expected only the release lookup: $(cat "$t/curl.log")"; fi
if cmp -s "$t/preplaced" "$bin"; then ok; else fail "installed binary was touched"; fi
if [ -s "$t/handoff.args" ]; then ok; else fail "install was not run"; fi
rm -f "$t/handoff.args" "$t/curl.log"
run_install FAKE_INSTALLED_VERSION=v1.0.0 -- --repo-url u
assert_rc 0
if cmp -s "$t/fixtures/asset" "$bin"; then ok; else fail "older binary was not replaced"; fi

# 12. Bad GITOPS_AGENT_REPO.
for r in a/b/c "evil repo"; do
  setup "bad-repo $r"
  run_install "GITOPS_AGENT_REPO=$r" -- --repo-url u
  assert_rc 1
  assert_err "must look like owner/name"
  if [ ! -s "$t/curl.log" ]; then ok; else fail "network used with a bad repo"; fi
done

# 13. The file is swapped after verification: the staged copy is re-hashed.
setup tamper
run_install FAKE_TAMPER=1 -- --repo-url u
assert_rc 1
assert_err "changed after verification"
assert_bindir_empty
assert_absent "$t/handoff.args"
assert_no_workdir

# 14. Running as root: no sudo anywhere, empty sudo_cmd array.
setup root
rm -f "$t/fakebin/sudo"
run_install FAKE_UID=0 -- --repo-url u --user pi
assert_rc 0
if cmp -s "$t/fixtures/asset" "$bin"; then ok; else fail "root: installed binary differs"; fi
if [ "$(cat "$t/handoff.args")" = "$(printf '%s\n' --repo-url u --user pi)" ]; then ok; else fail "root: handoff args"; fi
assert_absent "$t/sudo.log"

# 15. Not root and no sudo.
setup no-sudo
rm -f "$t/fakebin/sudo"
run_install -- --repo-url u
assert_rc 1
assert_err "'sudo' is not installed"
assert_bindir_empty

echo "passed: $pass, failed: $failed"
[ "$failed" -eq 0 ]
