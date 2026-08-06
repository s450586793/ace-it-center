#!/usr/bin/env bash

set -euo pipefail
shopt -s extglob
export LC_ALL=C

fail() {
  printf 'error: %s\n' "$1" >&2
  exit 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "$1 is required"
}

compare_numeric_identifier() {
  local left="${1##+(0)}"
  local right="${2##+(0)}"

  [[ -n "$left" ]] || left=0
  [[ -n "$right" ]] || right=0
  if (( ${#left} < ${#right} )); then
    printf '%s\n' -1
  elif (( ${#left} > ${#right} )); then
    printf '%s\n' 1
  elif [[ "$left" < "$right" ]]; then
    printf '%s\n' -1
  elif [[ "$left" > "$right" ]]; then
    printf '%s\n' 1
  else
    printf '%s\n' 0
  fi
}

compare_semver() {
  local left_without_build="${1%%+*}"
  local right_without_build="${2%%+*}"
  local left_core="${left_without_build%%-*}"
  local right_core="${right_without_build%%-*}"
  local left_prerelease=
  local right_prerelease=
  local comparison
  local index
  local left_identifier
  local right_identifier
  local -a left_core_parts=()
  local -a right_core_parts=()
  local -a left_prerelease_parts=()
  local -a right_prerelease_parts=()

  [[ "$left_without_build" != "$left_core" ]] && left_prerelease="${left_without_build#*-}"
  [[ "$right_without_build" != "$right_core" ]] && right_prerelease="${right_without_build#*-}"
  IFS='.' read -r -a left_core_parts <<<"$left_core"
  IFS='.' read -r -a right_core_parts <<<"$right_core"
  for index in 0 1 2; do
    comparison="$(compare_numeric_identifier "${left_core_parts[$index]}" "${right_core_parts[$index]}")"
    if [[ "$comparison" != 0 ]]; then
      printf '%s\n' "$comparison"
      return
    fi
  done

  if [[ -z "$left_prerelease" && -z "$right_prerelease" ]]; then
    printf '%s\n' 0
    return
  fi
  if [[ -z "$left_prerelease" ]]; then
    printf '%s\n' 1
    return
  fi
  if [[ -z "$right_prerelease" ]]; then
    printf '%s\n' -1
    return
  fi

  IFS='.' read -r -a left_prerelease_parts <<<"$left_prerelease"
  IFS='.' read -r -a right_prerelease_parts <<<"$right_prerelease"
  for ((index = 0; index < ${#left_prerelease_parts[@]} && index < ${#right_prerelease_parts[@]}; index++)); do
    left_identifier="${left_prerelease_parts[$index]}"
    right_identifier="${right_prerelease_parts[$index]}"
    if [[ "$left_identifier" =~ ^[0-9]+$ && "$right_identifier" =~ ^[0-9]+$ ]]; then
      comparison="$(compare_numeric_identifier "$left_identifier" "$right_identifier")"
      if [[ "$comparison" != 0 ]]; then
        printf '%s\n' "$comparison"
        return
      fi
    elif [[ "$left_identifier" =~ ^[0-9]+$ ]]; then
      printf '%s\n' -1
      return
    elif [[ "$right_identifier" =~ ^[0-9]+$ ]]; then
      printf '%s\n' 1
      return
    elif [[ "$left_identifier" < "$right_identifier" ]]; then
      printf '%s\n' -1
      return
    elif [[ "$left_identifier" > "$right_identifier" ]]; then
      printf '%s\n' 1
      return
    fi
  done

  if (( ${#left_prerelease_parts[@]} < ${#right_prerelease_parts[@]} )); then
    printf '%s\n' -1
  elif (( ${#left_prerelease_parts[@]} > ${#right_prerelease_parts[@]} )); then
    printf '%s\n' 1
  else
    printf '%s\n' 0
  fi
}

restore_link() {
  local destination="$1"
  local previous_target="$2"
  local temporary="$3"

  if [[ -n "$previous_target" ]]; then
    rm -f -- "$temporary" || return 1
    ln -s -- "$previous_target" "$temporary" || return 1
    mv -Tf -- "$temporary" "$destination" || return 1
  else
    rm -f -- "$destination" || return 1
  fi
}

release_root="${1:-}"
artifact="${2:-}"
manifest="${3:-}"
public_key="${4:-}"
[[ $# -eq 4 ]] || fail "expected RELEASE_ROOT ARTIFACT MANIFEST PUBLIC_KEY"
[[ "$release_root" == /* && "$release_root" != / ]] || fail "RELEASE_ROOT must be an absolute path other than /"
[[ -f "$artifact" && ! -L "$artifact" ]] || fail "ARTIFACT must be a regular file"
[[ -s "$artifact" ]] || fail "ARTIFACT must not be empty"
[[ -f "$manifest" && ! -L "$manifest" ]] || fail "MANIFEST must be a regular file"
[[ -f "$public_key" && ! -L "$public_key" ]] || fail "PUBLIC_KEY must be a regular file"

require_command flock
require_command jq
require_command sync

ace_release_bin="${ACE_RELEASE_BIN:-ace-release}"
if [[ "$ace_release_bin" == */* ]]; then
  [[ -x "$ace_release_bin" ]] || fail "ACE_RELEASE_BIN is not executable"
else
  ace_release_bin="$(command -v "$ace_release_bin" 2>/dev/null)" || fail "ace-release is required"
fi

"$ace_release_bin" verify -public "$public_key" -artifact "$artifact" -manifest "$manifest" >/dev/null

version="$(jq -er '.version | select(type == "string")' "$manifest")" || fail "manifest version is unavailable"
artifact_name="AceAgentSetup-windows-amd64-V${version}.exe"
[[ "$(basename "$artifact")" == "$artifact_name" ]] || fail "artifact filename does not match manifest version"
expected_url="/downloads/windows/stable/$artifact_name"
[[ "$(jq -er '.url | select(type == "string")' "$manifest")" == "$expected_url" ]] || fail "manifest URL must be $expected_url"

windows_root="$release_root/windows"
stable_root="$windows_root/stable"
versions_root="$stable_root/releases"
mkdir -p -- "$versions_root"
for directory in "$release_root" "$windows_root" "$stable_root" "$versions_root"; do
  [[ -d "$directory" && ! -L "$directory" ]] || fail "release path must be a real directory"
done

exec 9>"$release_root/.windows-release.lock"
flock -x 9

current_version=
if [[ -e "$stable_root/latest.json" || -L "$stable_root/latest.json" ]]; then
  [[ -f "$stable_root/latest.json" ]] || fail "current stable manifest is unavailable"
  current_version="$(jq -er '.version | select(type == "string")' "$stable_root/latest.json")" || fail "current stable version is unavailable"
  version_order="$(compare_semver "$version" "$current_version")"
  if [[ "$version_order" == -1 ]]; then
    fail "release $version is older than current stable release $current_version"
  fi
  if [[ "$version_order" == 0 ]]; then
    fail "release $version is already current"
  fi
fi

version_root="$versions_root/$version"
versioned_link="$stable_root/$artifact_name"
stable_alias="$stable_root/AceAgentSetup-windows-amd64.exe"
stable_manifest="$stable_root/latest.json"
[[ ! -e "$version_root" && ! -L "$version_root" ]] || fail "release version is immutable and already exists"
[[ ! -e "$versioned_link" && ! -L "$versioned_link" ]] || fail "versioned artifact is immutable and already exists"
[[ ! -e "$stable_alias" || -L "$stable_alias" ]] || fail "stable alias must be a symbolic link"
[[ ! -e "$stable_manifest" || -L "$stable_manifest" ]] || fail "stable manifest must be a symbolic link"

stage_root="$(mktemp -d "$stable_root/.publish-${version}.XXXXXX")"
next_versioned="$stable_root/.next-versioned-$$"
next_alias="$stable_root/.next-alias-$$"
next_manifest="$stable_root/.next-manifest-$$"
rollback_alias="$stable_root/.next-rollback-alias-$$"
rollback_manifest="$stable_root/.next-rollback-manifest-$$"
previous_alias_target=
previous_manifest_target=
[[ -L "$stable_alias" ]] && previous_alias_target="$(readlink "$stable_alias")"
[[ -L "$stable_manifest" ]] && previous_manifest_target="$(readlink "$stable_manifest")"
published_release=false
published_versioned=false
published_alias=false
published_manifest=false
committed=false

cleanup() {
  local status=$?
  local rollback_incomplete=false
  local pointer_restore_incomplete=false
  local release_content_retained=false
  trap - EXIT
  rm -f -- "$next_versioned" "$next_alias" "$next_manifest" "$rollback_alias" "$rollback_manifest" || rollback_incomplete=true
  if [[ "$committed" != true ]]; then
    if [[ "$published_manifest" == true ]]; then
      if ! restore_link "$stable_manifest" "$previous_manifest_target" "$rollback_manifest"; then
        pointer_restore_incomplete=true
        rollback_incomplete=true
      fi
    fi
    if [[ "$published_alias" == true ]]; then
      if ! restore_link "$stable_alias" "$previous_alias_target" "$rollback_alias"; then
        pointer_restore_incomplete=true
        rollback_incomplete=true
      fi
    fi
    rm -f -- "$rollback_alias" "$rollback_manifest" || rollback_incomplete=true
    if [[ "$pointer_restore_incomplete" == true ]]; then
      release_content_retained=true
    else
      if [[ "$published_versioned" == true ]] && ! rm -f -- "$versioned_link"; then
        rollback_incomplete=true
        release_content_retained=true
      fi
      if [[ "$published_release" == true && "$release_content_retained" != true ]] && ! rm -rf -- "$version_root"; then
        rollback_incomplete=true
        release_content_retained=true
      fi
    fi
    if [[ "$published_manifest" == true || "$published_alias" == true || "$published_versioned" == true ]]; then
      sync -f "$stable_root" || rollback_incomplete=true
    fi
    if [[ "$published_release" == true ]]; then
      sync -f "$versions_root" || rollback_incomplete=true
    fi
  fi
  if [[ -n "${stage_root:-}" && -d "$stage_root" ]]; then
    rm -rf -- "$stage_root" || rollback_incomplete=true
  fi
  if [[ "$rollback_incomplete" == true ]]; then
    if [[ "$release_content_retained" == true ]]; then
      printf 'error: publication rollback was incomplete; release content retained for manual intervention\n' >&2
    else
      printf 'error: publication rollback was incomplete\n' >&2
    fi
    status=1
  fi
  exit "$status"
}
trap cleanup EXIT

install -m 0644 -- "$artifact" "$stage_root/$artifact_name"
install -m 0644 -- "$manifest" "$stage_root/latest.json"
ln -- "$stage_root/$artifact_name" "$stage_root/AceAgentSetup-windows-amd64.exe"
chmod 0755 "$stage_root"
"$ace_release_bin" verify \
  -public "$public_key" \
  -artifact "$stage_root/$artifact_name" \
  -manifest "$stage_root/latest.json" >/dev/null
sync -f "$stage_root/$artifact_name"
sync -f "$stage_root/latest.json"
sync -f "$stage_root"

ln -s -- "releases/$version/$artifact_name" "$next_versioned"
ln -s -- "releases/$version/$artifact_name" "$next_alias"
ln -s -- "releases/$version/latest.json" "$next_manifest"

mv -- "$stage_root" "$version_root"
stage_root=
published_release=true
sync -f "$versions_root"

mv -T -- "$next_versioned" "$versioned_link"
published_versioned=true
mv -Tf -- "$next_alias" "$stable_alias"
published_alias=true
mv -Tf -- "$next_manifest" "$stable_manifest"
published_manifest=true
sync -f "$stable_root"
committed=true

printf 'Published Windows Agent %s to %s\n' "$version" "$stable_root"
