#!/usr/bin/env bash

set -euo pipefail

fail() {
  printf 'error: %s\n' "$1" >&2
  exit 1
}

is_semantic_version() {
  local candidate="$1"
  local without_build
  local prerelease
  local identifier
  local -a identifiers=()

  [[ "$candidate" =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-([0-9A-Za-z-]+)(\.[0-9A-Za-z-]+)*)?(\+([0-9A-Za-z-]+)(\.[0-9A-Za-z-]+)*)?$ ]] || return 1
  without_build="${candidate%%+*}"
  if [[ "$without_build" != *-* ]]; then
    return 0
  fi
  prerelease="${without_build#*-}"
  IFS='.' read -r -a identifiers <<<"$prerelease"
  for identifier in "${identifiers[@]}"; do
    if [[ "$identifier" =~ ^[0-9]+$ && "$identifier" != "0" && "$identifier" == 0* ]]; then
      return 1
    fi
  done
  return 0
}

agent_only=false
if [[ "${1:-}" == "--agent-only" ]]; then
  agent_only=true
  shift
fi

version="${1:-}"
commit="${2:-}"
built_at="${3:-}"
out_dir="${4:-}"

[[ -n "$version" ]] || fail "VERSION is required"
is_semantic_version "$version" || fail "VERSION must be a semantic version such as 0.2.0"
[[ -n "$commit" ]] || fail "COMMIT is required"
[[ "$commit" =~ ^[0-9a-fA-F]{7,64}$ ]] || fail "COMMIT must be a hexadecimal revision between 7 and 64 characters"
[[ -n "$built_at" ]] || fail "BUILT_AT is required"
[[ "$built_at" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$ ]] || fail "BUILT_AT must be a UTC RFC3339 timestamp such as 2026-07-27T08:09:10Z"
normalized_built_at="$(date -u -d "$built_at" '+%Y-%m-%dT%H:%M:%SZ' 2>/dev/null)" || fail "BUILT_AT must be a valid UTC RFC3339 timestamp"
[[ "$normalized_built_at" == "$built_at" ]] || fail "BUILT_AT must be a valid UTC RFC3339 timestamp"
[[ -n "$out_dir" ]] || fail "OUT_DIR is required"
[[ "$out_dir" == /* && "$out_dir" != "/" ]] || fail "OUT_DIR must be an absolute path other than /"
[[ ! -e "$out_dir" || -d "$out_dir" ]] || fail "OUT_DIR is not a directory: $out_dir"
[[ $# -eq 4 ]] || fail "expected VERSION COMMIT BUILT_AT OUT_DIR"

update_public_key="${ACE_UPDATE_PUBLIC_KEY:-}"
[[ -n "$update_public_key" ]] || fail "ACE_UPDATE_PUBLIC_KEY is required"
[[ "$update_public_key" =~ ^[A-Za-z0-9+/]+={0,2}$ ]] || fail "ACE_UPDATE_PUBLIC_KEY must be valid base64"
decoded_key_size="$(printf '%s' "$update_public_key" | base64 --decode 2>/dev/null | wc -c)" || fail "ACE_UPDATE_PUBLIC_KEY must be valid base64"
[[ "$decoded_key_size" -gt 0 ]] || fail "ACE_UPDATE_PUBLIC_KEY must decode to a non-empty value"

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
agent_source="${ACE_AGENT_SOURCE:-$repo_root/agent/cmd/ace-agent}"
[[ "$agent_source" != -* ]] || fail "ACE_AGENT_SOURCE must not start with '-'"
[[ -d "$agent_source" ]] || fail "Agent source directory does not exist: $agent_source"
agent_source="$(cd -- "$agent_source" && pwd -P)" || fail "cannot resolve Agent source directory: $agent_source"

iscc_command=""
if [[ "$agent_only" == false ]]; then
  [[ -n "${ISCC:-}" ]] || fail "ISCC is required in full-package mode"
  if [[ "$ISCC" == */* ]]; then
    [[ -x "$ISCC" ]] || fail "ISCC is not executable or unavailable: $ISCC"
    iscc_command="$ISCC"
  else
    iscc_command="$(command -v "$ISCC" 2>/dev/null)" || fail "ISCC is not executable or unavailable: $ISCC"
  fi
fi

go_command="${GO_BIN:-go}"
if [[ "$go_command" == */* ]]; then
  [[ -x "$go_command" ]] || fail "GO_BIN is not executable: $go_command"
else
  go_command="$(command -v "$go_command" 2>/dev/null)" || fail "Go compiler is unavailable; set GO_BIN"
fi

mkdir -p "$out_dir" || fail "cannot create OUT_DIR: $out_dir"
[[ -d "$out_dir" ]] || fail "OUT_DIR is not a directory: $out_dir"

agent_path="$out_dir/AceAgent.exe"
buildinfo_package="aceitcenter.local/platform/agent/internal/buildinfo"
ldflags="-s -w -H windowsgui"
ldflags+=" -X ${buildinfo_package}.Version=${version}"
ldflags+=" -X ${buildinfo_package}.Commit=${commit}"
ldflags+=" -X ${buildinfo_package}.BuiltAt=${built_at}"
ldflags+=" -X ${buildinfo_package}.UpdatePublicKey=${update_public_key}"

(
  cd "$repo_root"
  CGO_ENABLED=0 GOOS=windows GOARCH=amd64 "$go_command" build \
    -trimpath \
    -ldflags "$ldflags" \
    -o "$agent_path" \
    "$agent_source"
)

[[ -s "$agent_path" ]] || fail "Agent build did not produce $agent_path"
printf 'Built %s\n' "$agent_path"
printf 'SHA-256 %s  %s\n' "$(sha256sum "$agent_path" | awk '{print $1}')" "$agent_path"

if [[ "$agent_only" == true ]]; then
  exit 0
fi

installer_script="$repo_root/installer/windows/AceAgent.iss"
[[ -f "$installer_script" ]] || fail "Inno Setup source does not exist: $installer_script"
installer_path="$out_dir/AceAgentSetup-windows-amd64-V${version}.exe"
if [[ -e "$installer_path" || -L "$installer_path" ]]; then
  rm -f -- "$installer_path" || fail "cannot remove previous installer artifact: $installer_path"
fi

"$iscc_command" \
  "/DAppVersion=$version" \
  "/DSourceExe=$agent_path" \
  "/DOutputDir=$out_dir" \
  "$installer_script"

[[ -s "$installer_path" ]] || fail "Inno Setup did not produce a fresh installer: $installer_path"
printf 'Built %s\n' "$installer_path"
printf 'SHA-256 %s  %s\n' "$(sha256sum "$installer_path" | awk '{print $1}')" "$installer_path"
