#!/usr/bin/env bash

set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
smoke="$root/scripts/system-update-dsm-smoke.sh"
test_root="$(mktemp -d)"

cleanup_test_root() {
  if [[ -n "${cleanup_state_file:-}" ]]; then
    sudo -n rm -f "$cleanup_state_file" >/dev/null 2>&1 || true
  fi
  rm -rf "$test_root"
}

trap cleanup_test_root EXIT

fail() {
  printf 'FAIL: %s\n' "$1" >&2
  exit 1
}

[[ -x "$smoke" ]] || fail "smoke script is missing"

real_jq="$(command -v jq 2>/dev/null)" || fail "jq is required for smoke contract tests"
real_sudo="$(command -v sudo 2>/dev/null)" || fail "sudo is required for smoke contract tests"
fake_bin="$test_root/bin"
mkdir -p "$fake_bin"

cat >"$fake_bin/date" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
[[ "${1:-}" == +%s ]] || exit 96
cat "${FAKE_CLOCK:?}"
EOF

cat >"$fake_bin/jq" <<EOF
#!/usr/bin/env bash
printf 'jq\n' >>"\${FAKE_LOG:?}"
exec "$real_jq" "\$@"
EOF

cat >"$fake_bin/docker" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

printf 'docker:%s\n' "$*" >>"${FAKE_LOG:?}"
printf '%s\n' "$PWD" >"${FAKE_DOCKER_PWD:?}"
case "$*" in
  "compose ps -q postgres") printf 'postgres-container\n' ;;
  "compose ps -q updater") printf 'updater-container\n' ;;
  "inspect --format {{.Id}}|{{.Image}}|{{.State.StartedAt}} postgres-container")
    count_file="${FAKE_DOCKER_COUNT:?}-postgres"
    count=0
    [[ ! -f "$count_file" ]] || count="$(<"$count_file")"
    count=$((count + 1))
    printf '%s\n' "$count" >"$count_file"
    if [[ "${FAKE_DOCKER_DRIFT:-false}" == true && "$count" -gt 1 ]]; then
      printf 'postgres-container|sha256:postgres-new|2026-08-06T00:00:00Z\n'
    else
      printf 'postgres-container|sha256:postgres|2026-08-06T00:00:00Z\n'
    fi
    ;;
  "inspect --format {{.Id}}|{{.Image}}|{{.State.StartedAt}} updater-container")
    count_file="${FAKE_DOCKER_COUNT:?}-updater"
    count=0
    [[ ! -f "$count_file" ]] || count="$(<"$count_file")"
    count=$((count + 1))
    printf '%s\n' "$count" >"$count_file"
    if [[ "${FAKE_UPDATER_DRIFT:-false}" == true && "$count" -gt 1 ]]; then
      printf 'updater-container|sha256:updater-new|2026-08-06T00:00:00Z\n'
    else
      printf 'updater-container|sha256:updater|2026-08-06T00:00:00Z\n'
    fi
    ;;
  *) printf 'unexpected docker command\n' >&2; exit 91 ;;
esac
EOF

cat >"$fake_bin/curl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

output=
cookie_out=
url=
method=
max_time=
while [[ $# -gt 0 ]]; do
  case "$1" in
    -o) output="$2"; shift 2 ;;
    -c) cookie_out="$2"; shift 2 ;;
    --max-time) max_time="$2"; shift 2 ;;
    -H|-b) shift 2 ;;
    -X) method="$2"; shift 2 ;;
    --data-binary) shift 2 ;;
    --fail|--silent|--show-error) shift ;;
    http://*|https://*) url="$1"; shift ;;
    *) shift ;;
  esac
done

[[ -n "$output" && -n "$url" ]] || exit 92
[[ "$max_time" =~ ^[1-5]$ ]] || exit 97
printf 'curl-max:%s\n' "$max_time" >>"${FAKE_LOG:?}"
case "$method:$url" in
  POST:*/api/v1/auth/login)
    printf 'login\n' >>"${FAKE_LOG:?}"
    [[ -n "$cookie_out" ]] || exit 93
    printf 'session-cookie=%s\n' "${FAKE_COOKIE_SECRET:?}" >"$cookie_out"
    stat -c '%a' "$cookie_out" >"${FAKE_COOKIE_MODE:?}"
    printf '%s\n' "$cookie_out" >"${FAKE_COOKIE_PATH:?}"
    printf '{"ok":true,"raw":"login-raw-marker"}\n' >"$output"
    ;;
  POST:*/api/v1/system/update/check)
    printf 'check\n' >>"${FAKE_LOG:?}"
    if [[ "${FAKE_TARGET_MODE:-match}" == mismatch ]]; then
      printf '{"latest":{"backend":"v9.9.9","web":"v9.9.9"},"update_available":true,"raw":"check-raw-marker"}\n' >"$output"
    else
      printf '{"latest":{"backend":"%s","web":"%s"},"update_available":true,"raw":"check-raw-marker"}\n' "${FAKE_TARGET:?}" "${FAKE_TARGET:?}" >"$output"
    fi
    ;;
  POST:*/api/v1/system/update)
    printf 'start\n' >>"${FAKE_LOG:?}"
    printf '{"id":"%s","raw":"start-raw-marker"}\n' "${FAKE_TASK_ID:?}" >"$output"
    if [[ "${FAKE_SKIP_BACKUP:-false}" != true ]]; then
      printf 'custom-format-backup\n' >"${FAKE_BACKUP_DIR:?}/upgrade-20260806T000000Z-${FAKE_TASK_ID:?}.dump"
    fi
    if [[ -n "${FAKE_UNRELATED_BACKUP:-}" ]]; then
      printf 'custom-format-backup\n' >"${FAKE_BACKUP_DIR:?}/upgrade-20260806T000000Z-123e4567-e89b-12d3-a456-426614174001.dump"
    fi
    [[ -z "${FAKE_START_CLOCK:-}" ]] || printf '%s\n' "$FAKE_START_CLOCK" >"${FAKE_CLOCK:?}"
    ;;
  GET:*/api/v1/health)
    printf 'health\n' >>"${FAKE_LOG:?}"
    printf '{"status":"ok","raw":"health-raw-marker"}\n' >"$output"
    ;;
  GET:*/api/v1/system/update)
    count=0
    [[ ! -f "${FAKE_POLL_COUNT:?}" ]] || count="$(<"$FAKE_POLL_COUNT")"
    count=$((count + 1))
    printf '%s\n' "$count" >"$FAKE_POLL_COUNT"
    printf 'poll\n' >>"${FAKE_LOG:?}"
    case "${FAKE_POLL_MODE:-success}" in
      checking)
        printf '{"task":{"stage":"checking","cleanup":"not_run"},"raw":"status-raw-marker"}\n' >"$output"
        ;;
      manual)
        printf '{"task":{"stage":"manual_intervention","cleanup":"not_run"},"raw":"status-raw-marker"}\n' >"$output"
        ;;
      failed)
        printf '{"task":{"stage":"failed","cleanup":"not_run"},"raw":"status-raw-marker"}\n' >"$output"
        ;;
      success)
        if [[ "$count" -eq 1 ]]; then
          printf '{"task":{"stage":"checking","cleanup":"not_run"},"raw":"status-raw-marker"}\n' >"$output"
        else
          printf '{"current":{"backend":"%s","web":"%s"},"task":{"stage":"succeeded","cleanup":"%s","to":{"backend":"%s","web":"%s"}},"raw":"status-raw-marker"}\n' "${FAKE_TARGET:?}" "${FAKE_TARGET:?}" "${FAKE_CLEANUP:-complete}" "${FAKE_TARGET:?}" "${FAKE_TARGET:?}" >"$output"
        fi
        ;;
    esac
    if [[ -n "${FAKE_STATUS_ADVANCE:-}" ]]; then
      clock="$(<"${FAKE_CLOCK:?}")"
      printf '%s\n' "$((clock + FAKE_STATUS_ADVANCE))" >"$FAKE_CLOCK"
    fi
    ;;
  *) exit 94 ;;
esac
EOF

cat >"$fake_bin/sleep" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
[[ "$#" == 1 && "$1" =~ ^[12]$ ]] || exit 95
printf 'sleep:%s\n' "$1" >>"${FAKE_LOG:?}"
clock="$(<"${FAKE_CLOCK:?}")"
printf '%s\n' "$((clock + $1))" >"$FAKE_CLOCK"
EOF
chmod +x "$fake_bin/date" "$fake_bin/jq" "$fake_bin/docker" "$fake_bin/curl" "$fake_bin/sleep"

assert_secret_free() {
  local output="$1"
  for secret in super-password session-cookie updater-token-secret login-raw-marker check-raw-marker start-raw-marker status-raw-marker health-raw-marker; do
    [[ "$output" != *"$secret"* ]] || fail "output leaked secret or raw response"
  done
}

assert_no_calls() {
  local log="$1"
  [[ ! -s "$log" ]] || fail "confirmation or input failure called an external dependency"
}

run_smoke() {
  local case_root="$1"
  shift
  mkdir -p "$case_root/backups"
  mkdir -p "$case_root/non-project-cwd"
  : >"$case_root/log"
  printf '0\n' >"$case_root/clock"
  (
    cd "$case_root/non-project-cwd"
    env \
    PATH="$fake_bin:$PATH" \
    FAKE_LOG="$case_root/log" \
    FAKE_CLOCK="$case_root/clock" \
    FAKE_COOKIE_MODE="$case_root/cookie-mode" \
    FAKE_COOKIE_PATH="$case_root/cookie-path" \
    FAKE_COOKIE_SECRET=session-cookie \
    FAKE_BACKUP_DIR="$case_root/backups" \
    FAKE_POLL_COUNT="$case_root/polls" \
    FAKE_DOCKER_COUNT="$case_root/docker-count" \
    FAKE_DOCKER_PWD="$case_root/docker-pwd" \
    FAKE_TARGET=v1.2.3 \
    FAKE_TASK_ID=123e4567-e89b-12d3-a456-426614174000 \
    ACE_BASE_URL=http://dsm.example.test:9060 \
    ACE_OWNER_USERNAME=owner \
    ACE_OWNER_PASSWORD=super-password \
    ACE_UPDATER_TOKEN=updater-token-secret \
    ACE_EXPECTED_TARGET=v1.2.3 \
    ACE_BACKUP_DIR="$case_root/backups" \
    "$@" \
    bash "$smoke"
  )
}

run_failure() {
  local output
  set +e
  output="$(run_smoke "$@" 2>&1)"
  local status=$?
  set -e
  [[ $status -ne 0 ]] || fail "smoke command unexpectedly succeeded"
  assert_secret_free "$output"
}

no_confirm="$test_root/no-confirm"
mkdir -p "$no_confirm"
run_failure "$no_confirm" env -u ACE_CONFIRM_SYSTEM_UPDATE
assert_no_calls "$no_confirm/log"

for missing in ACE_BASE_URL ACE_OWNER_USERNAME ACE_OWNER_PASSWORD ACE_EXPECTED_TARGET; do
  case_root="$test_root/missing-$missing"
  mkdir -p "$case_root"
  run_failure "$case_root" env -u "$missing" ACE_CONFIRM_SYSTEM_UPDATE=yes
  assert_no_calls "$case_root/log"
done

success="$test_root/success"
success_output="$(run_smoke "$success" env ACE_CONFIRM_SYSTEM_UPDATE=yes 2>&1)" || fail "successful smoke command failed: $success_output"
assert_secret_free "$success_output"
[[ "$(<"$success/cookie-mode")" == 600 ]] || fail "cookie jar mode is not 0600"
cookie_path="$(<"$success/cookie-path")"
[[ ! -e "$cookie_path" ]] || fail "cookie jar was not removed"
[[ "$(grep -cx start "$success/log")" == 1 ]] || fail "start was not called exactly once"
[[ "$(grep -cx poll "$success/log")" == 2 ]] || fail "success polling did not wait for completion"
[[ -f "$success/backups/upgrade-20260806T000000Z-123e4567-e89b-12d3-a456-426614174000.dump" ]] || fail "new task backup was not verified"
[[ "$(<"$success/docker-pwd")" == "$root" ]] || fail "smoke did not pin Docker Compose to the project root"
grep -Fqx 'docker:compose ps -q postgres' "$success/log" || fail "postgres snapshot was not collected"
grep -Fqx 'docker:compose ps -q updater' "$success/log" || fail "updater snapshot was not collected"

mismatch="$test_root/mismatch"
run_failure "$mismatch" env ACE_CONFIRM_SYSTEM_UPDATE=yes FAKE_TARGET_MODE=mismatch
[[ ! -s "$mismatch/log" || "$(grep -cx start "$mismatch/log" 2>/dev/null || true)" == 0 ]] || fail "mismatched target started an update"

manual="$test_root/manual"
run_failure "$manual" env ACE_CONFIRM_SYSTEM_UPDATE=yes FAKE_POLL_MODE=manual
[[ "$(grep -cx start "$manual/log")" == 1 ]] || fail "manual state did not start exactly once"
[[ "$(<"$manual/polls")" == 1 ]] || fail "manual state did not stop after the first poll"
[[ "$(grep -cx 'sleep:2' "$manual/log" 2>/dev/null || true)" == 0 ]] || fail "manual state slept after terminal poll"

failed="$test_root/failed"
run_failure "$failed" env ACE_CONFIRM_SYSTEM_UPDATE=yes FAKE_POLL_MODE=failed
[[ "$(grep -cx start "$failed/log")" == 1 ]] || fail "failed state did not start exactly once"
[[ "$(<"$failed/polls")" == 1 ]] || fail "failed state did not stop after the first poll"
[[ "$(grep -cx 'sleep:2' "$failed/log" 2>/dev/null || true)" == 0 ]] || fail "failed state slept after terminal poll"

drift="$test_root/drift"
run_failure "$drift" env ACE_CONFIRM_SYSTEM_UPDATE=yes FAKE_DOCKER_DRIFT=true

updater_drift="$test_root/updater-drift"
run_failure "$updater_drift" env ACE_CONFIRM_SYSTEM_UPDATE=yes FAKE_UPDATER_DRIFT=true

no_backup="$test_root/no-backup"
run_failure "$no_backup" env ACE_CONFIRM_SYSTEM_UPDATE=yes FAKE_SKIP_BACKUP=true

unrelated_backup="$test_root/unrelated-backup"
run_failure "$unrelated_backup" env ACE_CONFIRM_SYSTEM_UPDATE=yes FAKE_SKIP_BACKUP=true FAKE_UNRELATED_BACKUP=true

pending="$test_root/pending"
pending_output="$(run_smoke "$pending" env ACE_CONFIRM_SYSTEM_UPDATE=yes FAKE_CLEANUP=pending 2>&1)" || fail "cleanup-pending smoke command failed"
assert_secret_free "$pending_output"
cleanup_instruction='cleanup_pending: follow deploy/README.md using private updater-state/update-state.json; verify exact task original IDs/aliases have no container references, then delete them without force.'
[[ "$pending_output" == *"$cleanup_instruction"* ]] || fail "cleanup-pending instruction changed"
grep -Fqx "$cleanup_instruction" "$root/deploy/README.md" || fail "cleanup-pending documentation and smoke instruction differ"
for private_value in sha256:postgres sha256:updater ace-it-center-rollback-backend ace-it-center-rollback-web; do
  [[ "$pending_output" != *"$private_value"* ]] || fail "cleanup-pending output leaked private image identity"
done

cleanup_runbook="$test_root/cleanup-runbook.sh"
awk '
  $0 == "```bash" { in_block = 1; block = ""; next }
  in_block && $0 == "```" {
    if (block ~ /updater-state\/update-state\.json/ && block ~ /docker image rm/) {
      printf "%s", block
      found = 1
    }
    in_block = 0
    next
  }
  in_block { block = block $0 ORS }
  END { if (!found) exit 1 }
' "$root/deploy/README.md" >"$cleanup_runbook" || fail "cleanup runbook is missing"

[[ "$(id -u)" != 0 ]] || fail "root-owned state contract must run as a non-root operator"
sudo -n true >/dev/null 2>&1 || fail "passwordless sudo is required for root-owned state contract"

cleanup_case="$test_root/cleanup-permissions"
cleanup_state_dir="$cleanup_case/updater-state"
cleanup_state_file="$cleanup_state_dir/update-state.json"
cleanup_source="$cleanup_case/update-state.source.json"
cleanup_log="$cleanup_case/sudo.log"
cleanup_bin="$cleanup_case/bin"
mkdir -p "$cleanup_state_dir" "$cleanup_bin"
cat >"$cleanup_source" <<'EOF'
{
  "task": {
    "id": "123e4567-e89b-12d3-a456-426614174000",
    "stage": "succeeded",
    "cleanup": "pending",
    "original": {
      "backend": {
        "id": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
        "digest": "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
        "version": "v1.2.2",
        "rollback_alias": "ace-it-center-rollback-backend:123e4567-e89b-12d3-a456-426614174000",
        "repository": "ghcr.io/s450586793/ace-it-center-backend"
      },
      "web": {
        "id": "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
        "digest": "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
        "version": "v1.2.2",
        "rollback_alias": "ace-it-center-rollback-web:123e4567-e89b-12d3-a456-426614174000",
        "repository": "ghcr.io/s450586793/ace-it-center-web"
      }
    }
  }
}
EOF
sudo -n install -o root -g root -m 0600 "$cleanup_source" "$cleanup_state_file"
[[ "$(stat -c '%U:%G:%a' "$cleanup_state_file")" == root:root:600 ]] || fail "state fixture is not root-owned mode 0600"
if "$real_jq" -e '.task.stage == "succeeded"' "$cleanup_state_file" >/dev/null 2>&1; then
  fail "non-root operator unexpectedly read root-owned mode-0600 state"
fi

cat >"$cleanup_bin/sudo" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

printf '%s\n' "$1" >>"${CLEANUP_SUDO_LOG:?}"
case "$1" in
  test|jq)
    exec "${REAL_SUDO:?}" -n "$@"
    ;;
  docker)
    shift
    case "$*" in
      "image inspect sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
        printf '[{"Id":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","RepoTags":["ace-it-center-rollback-backend:123e4567-e89b-12d3-a456-426614174000"],"RepoDigests":["ghcr.io/s450586793/ace-it-center-backend@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"],"Config":{"Labels":{"org.opencontainers.image.version":"v1.2.2"}}}]\n'
        ;;
      "image inspect sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc")
        printf '[{"Id":"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","RepoTags":["ace-it-center-rollback-web:123e4567-e89b-12d3-a456-426614174000"],"RepoDigests":["ghcr.io/s450586793/ace-it-center-web@sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"],"Config":{"Labels":{"org.opencontainers.image.version":"v1.2.2"}}}]\n'
        ;;
      "image inspect --format {{.Id}} ace-it-center-rollback-backend:123e4567-e89b-12d3-a456-426614174000")
        printf 'sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n'
        ;;
      "image inspect --format {{.Id}} ace-it-center-rollback-web:123e4567-e89b-12d3-a456-426614174000")
        printf 'sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc\n'
        ;;
      "ps -aq --filter ancestor=sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"|\
      "ps -aq --filter ancestor=sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"|\
      "image rm ace-it-center-rollback-backend:123e4567-e89b-12d3-a456-426614174000"|\
      "image rm sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"|\
      "image rm ace-it-center-rollback-web:123e4567-e89b-12d3-a456-426614174000"|\
      "image rm sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc")
        ;;
      *) exit 98 ;;
    esac
    ;;
  *) exit 99 ;;
esac
EOF
chmod +x "$cleanup_bin/sudo"
: >"$cleanup_log"
cleanup_output="$({
  cd "$root"
  env PATH="$cleanup_bin:$PATH" REAL_SUDO="$real_sudo" CLEANUP_SUDO_LOG="$cleanup_log" ACE_DATA_DIR="$cleanup_case" \
    bash "$cleanup_runbook"
} 2>&1)" || fail "documented cleanup runbook failed against root-owned state"
[[ -z "$cleanup_output" ]] || fail "cleanup runbook exposed private state or Docker output"
[[ "$(grep -cx test "$cleanup_log")" == 1 ]] || fail "cleanup runbook did not root-scope its state existence check"
[[ "$(grep -cx jq "$cleanup_log")" == 12 ]] || fail "cleanup runbook did not root-scope all state jq reads"
[[ "$(grep -cx docker "$cleanup_log")" == 10 ]] || fail "cleanup runbook changed its exact non-force Docker checks or deletes"

state_access_count=0
while IFS= read -r line; do
  [[ "$line" == *'"$state_file"'* ]] || continue
  [[ "$line" != state_file=* ]] || continue
  case "$line" in
    *'sudo test '*|*'sudo jq '*) state_access_count=$((state_access_count + 1)) ;;
    *) fail "cleanup runbook contains a non-root state access" ;;
  esac
done <"$cleanup_runbook"
[[ "$state_access_count" == 8 ]] || fail "cleanup runbook does not root-scope every expected state access"

sudo -n rm -f "$cleanup_state_file"

bounded="$test_root/bounded"
run_failure "$bounded" env ACE_CONFIRM_SYSTEM_UPDATE=yes FAKE_POLL_MODE=checking FAKE_STATUS_ADVANCE=300
[[ "$(<"$bounded/polls")" == 2 ]] || fail "polling did not stop at the wall-clock deadline"
[[ "$(grep -cx start "$bounded/log")" == 1 ]] || fail "bounded polling retried start"
[[ "$(grep -cx 'sleep:2' "$bounded/log")" == 1 ]] || fail "bounded polling slept past the wall-clock deadline"

deadline="$test_root/deadline"
run_failure "$deadline" env ACE_CONFIRM_SYSTEM_UPDATE=yes FAKE_POLL_MODE=checking FAKE_START_CLOCK=595 FAKE_STATUS_ADVANCE=5
[[ "$(<"$deadline/polls")" == 1 ]] || fail "deadline allowed a second poll"
[[ "$(grep -cx 'sleep:2' "$deadline/log" 2>/dev/null || true)" == 0 ]] || fail "deadline overslept the wall-clock bound"

grep -Fq -- '--fail --silent --show-error --max-time' "$smoke" || fail "curl safety flags are missing"
grep -Fq 'jq -e' "$smoke" || fail "jq -e validation is missing"
grep -Fq 'readonly max_polls=300' "$smoke" || fail "poll count ceiling is missing"
if rg -n 'docker (image prune|container rm|rm -f)|--force' "$smoke" >/dev/null; then
  fail "smoke script contains a forbidden Docker cleanup command"
fi

printf 'PASS: DSM system-update smoke contract\n'
