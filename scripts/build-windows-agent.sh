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
updater_source="${ACE_UPDATER_SOURCE:-$repo_root/agent/cmd/ace-agent-updater}"
[[ "$updater_source" != -* ]] || fail "ACE_UPDATER_SOURCE must not start with '-'"
[[ -d "$updater_source" ]] || fail "Updater source directory does not exist: $updater_source"
updater_source="$(cd -- "$updater_source" && pwd -P)" || fail "cannot resolve Updater source directory: $updater_source"

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
windres_command="${WINDRES_BIN:-x86_64-w64-mingw32-windres}"
if [[ "$windres_command" == */* ]]; then
  [[ -x "$windres_command" ]] || fail "WINDRES_BIN is not executable: $windres_command"
else
  windres_command="$(command -v "$windres_command" 2>/dev/null)" || fail "windres is unavailable; set WINDRES_BIN"
fi
jq_command="$(command -v jq 2>/dev/null)" || fail "jq is required to create the Agent resource overlay"
version_core="${version%%[-+]*}"
IFS='.' read -r version_major version_minor version_patch <<<"$version_core"
for component in "$version_major" "$version_minor" "$version_patch"; do
  (( component <= 65535 )) || fail "VERSION numeric components must not exceed 65535 for Windows VERSIONINFO"
done
windows_version="${version_major}.${version_minor}.${version_patch}.0"
windows_version_commas="${version_major},${version_minor},${version_patch},0"

mkdir -p "$out_dir" || fail "cannot create OUT_DIR: $out_dir"
[[ -d "$out_dir" ]] || fail "OUT_DIR is not a directory: $out_dir"

agent_path="$out_dir/AceAgent.exe"
updater_path="$out_dir/AceAgentUpdater.exe"
buildinfo_package="aceitcenter.local/platform/agent/internal/buildinfo"
ldflags="-s -w -H windowsgui"
ldflags+=" -X ${buildinfo_package}.Version=${version}"
ldflags+=" -X ${buildinfo_package}.Commit=${commit}"
ldflags+=" -X ${buildinfo_package}.BuiltAt=${built_at}"
ldflags+=" -X ${buildinfo_package}.UpdatePublicKey=${update_public_key}"

resource_root="$(mktemp -d)" || fail "cannot create temporary resource directory"
agent_resource_target=""
updater_resource_target=""
agent_resource_owned=false
updater_resource_owned=false
cleanup() {
  if [[ "$agent_resource_owned" == true ]]; then
    rm -f -- "$agent_resource_target"
  fi
  if [[ "$updater_resource_owned" == true ]]; then
    rm -f -- "$updater_resource_target"
  fi
  rm -rf -- "$resource_root"
}
trap cleanup EXIT

render_resource() {
  local template="$1"
  local destination="$2"
  [[ -f "$template" ]] || fail "VERSIONINFO template does not exist: $template"
  sed \
    -e "s/@FILE_VERSION_COMMAS@/$windows_version_commas/g" \
    -e "s/@FILE_VERSION_DOTS@/$windows_version/g" \
    -e "s/@PRODUCT_VERSION@/$version/g" \
    -e "s|@AGENT_MANIFEST@|$agent_manifest|g" \
    "$template" >"$destination"
}

agent_rc="$resource_root/versioninfo-agent.rc"
updater_rc="$resource_root/versioninfo-updater.rc"
agent_syso="$resource_root/versioninfo-agent.syso"
updater_syso="$resource_root/versioninfo-updater.syso"
agent_overlay="$resource_root/agent-overlay.json"
agent_manifest="$repo_root/agent/internal/tray/assets_windows.manifest"
existing_agent_resource="$repo_root/agent/internal/tray/assets_windows.syso"
agent_resource_target="$agent_source/zz_versioninfo_windows.syso"
updater_resource_target="$updater_source/zz_versioninfo_windows.syso"
[[ -f "$agent_manifest" && -f "$existing_agent_resource" ]] || fail "existing Agent Windows manifest resource is unavailable"
[[ ! -e "$agent_resource_target" && ! -L "$agent_resource_target" ]] || fail "Agent VERSIONINFO target already exists: $agent_resource_target"
[[ ! -e "$updater_resource_target" && ! -L "$updater_resource_target" ]] || fail "Updater VERSIONINFO target already exists: $updater_resource_target"
render_resource "$repo_root/installer/windows/versioninfo-agent.rc.in" "$agent_rc"
render_resource "$repo_root/installer/windows/versioninfo-updater.rc.in" "$updater_rc"
"$windres_command" -i "$agent_rc" -o "$agent_syso" -O coff || fail "windres failed for Agent VERSIONINFO"
"$windres_command" -i "$updater_rc" -o "$updater_syso" -O coff || fail "windres failed for Updater VERSIONINFO"
[[ -s "$agent_syso" && -s "$updater_syso" ]] || fail "windres did not produce both VERSIONINFO resources"
ln -- "$agent_syso" "$agent_resource_target" || fail "cannot inject Agent VERSIONINFO resource"
agent_resource_owned=true
ln -- "$updater_syso" "$updater_resource_target" || fail "cannot inject Updater VERSIONINFO resource"
updater_resource_owned=true
"$jq_command" -n --arg existing "$existing_agent_resource" '{Replace: {($existing): ""}}' >"$agent_overlay"

(
  cd "$repo_root"
  CGO_ENABLED=0 GOOS=windows GOARCH=amd64 "$go_command" build \
    -trimpath \
    -overlay "$agent_overlay" \
    -ldflags "$ldflags" \
    -o "$agent_path" \
    "$agent_source"
)

[[ -s "$agent_path" ]] || fail "Agent build did not produce $agent_path"
printf 'Built %s\n' "$agent_path"
printf 'SHA-256 %s  %s\n' "$(sha256sum "$agent_path" | awk '{print $1}')" "$agent_path"

(
  cd "$repo_root"
  CGO_ENABLED=0 GOOS=windows GOARCH=amd64 "$go_command" build \
    -trimpath \
    -ldflags "$ldflags" \
    -o "$updater_path" \
    "$updater_source"
)

rm -f -- "$agent_resource_target" "$updater_resource_target" || fail "cannot remove temporary VERSIONINFO resources"
agent_resource_owned=false
updater_resource_owned=false

[[ -s "$updater_path" ]] || fail "Updater build did not produce $updater_path"
printf 'Built %s\n' "$updater_path"
printf 'SHA-256 %s  %s\n' "$(sha256sum "$updater_path" | awk '{print $1}')" "$updater_path"

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
  "/DWindowsVersion=$windows_version" \
  "/DSourceExe=$agent_path" \
  "/DSourceUpdater=$updater_path" \
  "/DOutputDir=$out_dir" \
  "$installer_script"

[[ -s "$installer_path" ]] || fail "Inno Setup did not produce a fresh installer: $installer_path"
printf 'Built %s\n' "$installer_path"
printf 'SHA-256 %s  %s\n' "$(sha256sum "$installer_path" | awk '{print $1}')" "$installer_path"
