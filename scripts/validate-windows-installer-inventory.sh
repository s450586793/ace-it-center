#!/usr/bin/env bash

set -euo pipefail

inventory="${1:-}"
[[ -n "$inventory" && -f "$inventory" && ! -L "$inventory" ]] || {
  printf 'error: installer inventory file is required\n' >&2
  exit 1
}
grep -Eiq '(^|[\\/])AceAgent\.exe([^[:alnum:]_.-]|$)' "$inventory" || {
  printf 'error: installer inventory does not contain AceAgent.exe\n' >&2
  exit 1
}
grep -Eiq '(^|[\\/])AceAgentUpdater\.exe([^[:alnum:]_.-]|$)' "$inventory" || {
  printf 'error: installer inventory does not contain AceAgentUpdater.exe\n' >&2
  exit 1
}
if grep -Eiq 'WinDivert|\.sys([^[:alnum:]_]|$)' "$inventory"; then
  printf 'error: installer inventory contains a forbidden driver\n' >&2
  exit 1
fi
