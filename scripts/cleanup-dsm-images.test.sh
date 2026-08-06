#!/usr/bin/env bash

set -euo pipefail

fail() {
  printf 'FAIL: %s\n' "$1" >&2
  exit 1
}

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

current="sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
old="sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
builder="sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"

cat >"$tmp/sudo" <<'EOF'
#!/bin/sh
exec "$@"
EOF

cat >"$tmp/docker" <<EOF
#!/bin/sh
case "\$*" in
  "image ls --no-trunc --quiet --filter label=com.docker.compose.project=ace-it-center")
    printf '%s\\n%s\\n' '$current' '$old'
    ;;
  "image ls --no-trunc --quiet --filter label=com.docker.compose.project=ace-it-center-windows-builder")
    printf '%s\\n' '$builder'
    ;;
  "image ls --no-trunc --quiet "*)
    ;;
  "ps -aq --filter ancestor=$current")
    printf '%s\\n' container-1
    ;;
  "ps -aq --filter ancestor="*)
    ;;
  "image rm "*)
    printf '%s\\n' "\${3}" >>'$tmp/removed'
    ;;
  *)
    printf 'unexpected docker command: %s\\n' "\$*" >&2
    exit 1
    ;;
esac
EOF

chmod +x "$tmp/sudo" "$tmp/docker"
PATH="$tmp:$PATH" bash "$root/scripts/cleanup-dsm-images.sh" >"$tmp/output"

grep -Fxq "$old" "$tmp/removed" || fail "old application image was not removed"
grep -Fxq "$builder" "$tmp/removed" || fail "unused builder image was not removed"
if grep -Fxq "$current" "$tmp/removed"; then
  fail "image referenced by a container was removed"
fi
grep -Fq 'removed=2 retained=1' "$tmp/output" || fail "cleanup summary is incorrect"

printf 'PASS: project image cleanup checks\n'
