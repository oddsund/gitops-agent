#!/usr/bin/env bash
# Tests for systemd/update.bash, the deprecated shim around
# `gitops-agent update`. Fake binaries only; no network, nothing outside a
# mktemp sandbox is touched. The shim runs the binary named by
# GITOPS_AGENT_BIN, so the test never needs to be root.
#
# The fakes emulate the one contract the shim depends on: a current binary
# lists "  update" in its -h output, an old one does not.
set -euo pipefail

[ "$(id -u)" -ne 0 ] || {
  echo "refusing to run as root"
  exit 1
}

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
shim="$here/../systemd/update.bash"

pass=0
failed=0
case_name=""
ok() { pass=$((pass + 1)); }
fail() {
  failed=$((failed + 1))
  echo "FAIL [$case_name]: $*" >&2
}

t="$(mktemp -d)"
trap 'rm -rf -- "$t"' EXIT
argv="$t/argv"

# One fake binary. FAKE_HELP_UPDATE=1 makes -h list the update subcommand
# like a current build. Any other invocation records its argv and
# GITOPS_AGENT_SERVICE.
cat >"$t/bin" <<'FAKE'
#!/usr/bin/env bash
if [ "${1:-}" = -h ]; then
  echo "usage: gitops-agent [-config path] [-version]" >&2
  if [ "${FAKE_HELP_UPDATE:-}" = 1 ]; then
    printf '%s\n' "" "subcommands:" "  install" "  update" >&2
  fi
  exit "${FAKE_HELP_EXIT:-0}"
fi
echo "$*" >"$FAKE_ARGV"
echo "$GITOPS_AGENT_SERVICE" >"$FAKE_ARGV.service"
FAKE
chmod 0755 "$t/bin"

# run_shim [VAR=value ...] -- ARGS...
run_shim() {
  local extra=()
  while [ "$#" -gt 0 ] && [ "$1" != -- ]; do
    extra+=("$1")
    shift
  done
  shift
  rm -f "$argv" "$argv.service"
  RC=0
  ERR="$(env FAKE_ARGV="$argv" ${extra[@]+"${extra[@]}"} bash "$shim" "$@" 2>&1 >/dev/null)" || RC=$?
}

assert_rc() {
  if [ "$RC" -eq "$1" ]; then ok; else fail "exit status $RC, want $1; stderr: $ERR"; fi
}
assert_err() {
  case "$ERR" in *"$1"*) ok ;; *) fail "stderr lacks '$1'; stderr: $ERR" ;; esac
}
assert_argv() {
  if [ "$(cat "$argv" 2>/dev/null)" = "$1" ]; then ok; else fail "argv = '$(cat "$argv" 2>/dev/null)', want '$1'"; fi
}

case_name=forwards
run_shim FAKE_HELP_UPDATE=1 GITOPS_AGENT_BIN="$t/bin" GITOPS_AGENT_SERVICE=svc --
assert_rc 0
assert_argv "update"
assert_err "deprecated"

case_name=args-forwarded
run_shim FAKE_HELP_UPDATE=1 GITOPS_AGENT_BIN="$t/bin" GITOPS_AGENT_SERVICE=svc -- --force
assert_rc 0
assert_argv "update --force"

case_name=old-binary
run_shim FAKE_HELP_UPDATE=0 GITOPS_AGENT_BIN="$t/bin" GITOPS_AGENT_SERVICE=svc --
assert_rc 1
assert_err "predates the 'update' subcommand"
if [ -e "$argv" ]; then fail "the old binary was executed with arguments"; else ok; fi

case_name=help-fails
run_shim FAKE_HELP_UPDATE=1 FAKE_HELP_EXIT=2 GITOPS_AGENT_BIN="$t/bin" --
assert_rc 1
assert_err "predates the 'update' subcommand"
if [ -e "$argv" ]; then fail "a binary whose -h fails was executed with arguments"; else ok; fi

case_name=missing-binary
run_shim GITOPS_AGENT_BIN="$t/nope" --
assert_rc 1
assert_err "not found or not executable"

case_name=relative-path
run_shim GITOPS_AGENT_BIN=./x --
assert_rc 1
assert_err "must be an absolute path"

case_name=env-passthrough
run_shim FAKE_HELP_UPDATE=1 GITOPS_AGENT_BIN="$t/bin" GITOPS_AGENT_SERVICE=my-svc GITHUB_RELEASE_LIB=/nonexistent --
assert_rc 0
if [ "$(cat "$argv.service")" = my-svc ]; then ok; else fail "service env not passed through"; fi

echo "passed: $pass, failed: $failed"
[ "$failed" -eq 0 ]
