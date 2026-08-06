#!/usr/bin/env bash

set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
smoke="$root/scripts/system-update-dsm-smoke.sh"
test_root="$(mktemp -d)"
trap 'rm -rf "$test_root"' EXIT

fail() {
  printf 'FAIL: %s\n' "$1" >&2
  exit 1
}

[[ -x "$smoke" ]] || fail "smoke script is missing"

real_jq="$(command -v jq 2>/dev/null)" || fail "jq is required for smoke contract tests"
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
[[ "$pending_output" == *"cleanup_pending: inspect references and remove only the displayed Ace IT Center old image after confirmation."* ]] || fail "cleanup-pending instruction changed"

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
