#!/usr/bin/env bash

set -euo pipefail

readonly max_polls=300
readonly max_duration_seconds=600
readonly cleanup_pending_instruction='cleanup_pending: follow deploy/README.md using private updater-state/update-state.json; verify exact task original IDs/aliases have no container references, then delete them without force.'

fail() {
  printf 'error: system update smoke check failed\n' >&2
  exit 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail
}

require_value() {
  [[ -n "${!1:-}" ]] || fail
}

snapshot_service() {
  local service="$1"
  local container_id

  container_id="$(docker compose ps -q "$service" 2>/dev/null)" || return 1
  [[ -n "$container_id" ]] || return 1
  docker inspect --format '{{.Id}}|{{.Image}}|{{.State.StartedAt}}' "$container_id" 2>/dev/null
}

request() {
  local method="$1"
  local path="$2"
  local remaining
  local request_timeout
  shift 2

  remaining="$(remaining_seconds)" || return 1
  request_timeout="$remaining"
  (( request_timeout > 5 )) && request_timeout=5
  : >"$response_file"
  curl --fail --silent --show-error --max-time "$request_timeout" \
    -X "$method" \
    -o "$response_file" \
    "$@" \
    "${base_url}${path}" \
    2>/dev/null
}

remaining_seconds() {
  local now

  now="$(date +%s 2>/dev/null)" || return 1
  [[ "$now" =~ ^[0-9]+$ ]] || return 1
  (( now < deadline )) || return 1
  printf '%s\n' "$((deadline - now))"
}

sleep_for_poll() {
  local remaining
  local interval=2

  remaining="$(remaining_seconds)" || return 1
  (( remaining < interval )) && interval="$remaining"
  (( interval > 0 )) || return 1
  sleep "$interval"
}

backup_for_task_exists() {
  local task_id="$1"
  local candidate_path
  local candidate

  [[ "$task_id" =~ ^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$ ]] || return 1
  for candidate_path in "$backup_dir"/upgrade-????????T??????Z-"$task_id".dump; do
    [[ -f "$candidate_path" ]] || continue
    candidate="${candidate_path##*/}"
    [[ "$candidate" =~ ^upgrade-[0-9]{8}T[0-9]{6}Z-"$task_id"\.dump$ ]] || continue
    [[ -n "${backup_baseline[$candidate]:-}" ]] && continue
    [[ -s "$candidate_path" ]] && return 0
  done
  return 1
}

[[ "${ACE_CONFIRM_SYSTEM_UPDATE:-}" == yes ]] || fail
require_value ACE_BASE_URL
require_value ACE_OWNER_USERNAME
require_value ACE_OWNER_PASSWORD
require_value ACE_EXPECTED_TARGET

[[ "$ACE_BASE_URL" =~ ^https?://[^[:space:]/]+(:[0-9]+)?$ ]] || fail
[[ "$ACE_EXPECTED_TARGET" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([-.][0-9A-Za-z.-]+)?$ ]] || fail

project_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)" || fail
cd "$project_root" || fail

require_command curl
require_command docker
require_command jq
require_command date
require_command sleep

base_url="${ACE_BASE_URL%/}"
backup_dir="${ACE_BACKUP_DIR:-${ACE_DATA_DIR:-.}/backups}"
[[ -d "$backup_dir" ]] || fail

started_at="$(date +%s 2>/dev/null)" || fail
[[ "$started_at" =~ ^[0-9]+$ ]] || fail
deadline=$((started_at + max_duration_seconds))

tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT
chmod 0700 "$tmp_dir"
cookie_jar="$(mktemp "$tmp_dir/cookie.XXXXXX")"
response_file="$(mktemp "$tmp_dir/response.XXXXXX")"
request_file="$(mktemp "$tmp_dir/request.XXXXXX")"
chmod 0600 "$cookie_jar" "$response_file" "$request_file"

declare -A backup_baseline=()
for backup_path in "$backup_dir"/upgrade-*.dump; do
  [[ -f "$backup_path" ]] || continue
  backup_baseline["${backup_path##*/}"]=1
done

postgres_before="$(snapshot_service postgres)" || fail
updater_before="$(snapshot_service updater)" || fail

ACE_SMOKE_USERNAME="$ACE_OWNER_USERNAME" ACE_SMOKE_PASSWORD="$ACE_OWNER_PASSWORD" \
  jq -n '$ENV | {username: .ACE_SMOKE_USERNAME, password: .ACE_SMOKE_PASSWORD}' >"$request_file" 2>/dev/null || fail
request POST /api/v1/auth/login \
  -c "$cookie_jar" \
  -H 'Content-Type: application/json' \
  --data-binary "@$request_file" || fail
[[ -s "$cookie_jar" ]] || fail

request POST /api/v1/system/update/check -b "$cookie_jar" || fail
jq -e --arg target "$ACE_EXPECTED_TARGET" '
  .latest != null and
  .latest.backend == $target and
  .latest.web == $target and
  .update_available == true
' "$response_file" >/dev/null 2>/dev/null || fail

jq -n --arg target_version "$ACE_EXPECTED_TARGET" '{target_version: $target_version}' >"$request_file" 2>/dev/null || fail
request POST /api/v1/system/update \
  -b "$cookie_jar" \
  -H 'Content-Type: application/json' \
  --data-binary "@$request_file" || fail
task_id="$(jq -er '.id | select(type == "string" and test("^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$"))' "$response_file" 2>/dev/null)" || fail

for ((poll = 1; poll <= max_polls; poll++)); do
  if ! request GET /api/v1/system/update -b "$cookie_jar"; then
    sleep_for_poll || break
    continue
  fi

  stage="$(jq -er '.task.stage // empty' "$response_file" 2>/dev/null)" || fail
  case "$stage" in
    failed|manual_intervention)
      fail
      ;;
    succeeded)
      jq -e --arg target "$ACE_EXPECTED_TARGET" '
        .current.backend == $target and
        .current.web == $target and
        .task.to.backend == $target and
        .task.to.web == $target
      ' "$response_file" >/dev/null 2>/dev/null || fail
      cleanup="$(jq -er '.task.cleanup // empty' "$response_file" 2>/dev/null)" || fail
      request GET /api/v1/health -b "$cookie_jar" || fail
      jq -e '.status == "ok"' "$response_file" >/dev/null 2>/dev/null || fail
      [[ "$(snapshot_service postgres)" == "$postgres_before" ]] || fail
      [[ "$(snapshot_service updater)" == "$updater_before" ]] || fail
      backup_for_task_exists "$task_id" || fail
      case "$cleanup" in
        complete)
          printf 'system update smoke check passed\n'
          exit 0
          ;;
        pending)
          printf '%s\n' "$cleanup_pending_instruction"
          exit 0
          ;;
        *)
          fail
          ;;
      esac
      ;;
  esac
  sleep_for_poll || break
done

fail
