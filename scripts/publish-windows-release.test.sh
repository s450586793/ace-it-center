#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
publisher="$repo_root/scripts/publish-windows-release.sh"
test_root="$(mktemp -d)"
trap 'rm -rf "$test_root"' EXIT
chmod 0755 "$test_root"

pass_count=0

fail() {
  printf 'FAIL: %s\n' "$1" >&2
  exit 1
}

assert_file() {
  local path="$1"
  [[ -f "$path" ]] || fail "expected file: $path"
}

assert_link_target() {
  local path="$1"
  local expected="$2"
  [[ -L "$path" ]] || fail "expected symbolic link: $path"
  [[ "$(readlink "$path")" == "$expected" ]] || fail "unexpected link target for $path: $(readlink "$path")"
}

assert_no_temporary_entries() {
  local stable_root="$1"
  local leftovers
  leftovers="$(find "$stable_root" -name '.publish-*' -o -name '.next-*' 2>/dev/null)"
  [[ -z "$leftovers" ]] || fail "temporary publication entries remained: $leftovers"
}

write_manifest() {
  local path="$1"
  local version="$2"
  local artifact="$3"
  local artifact_name
  local artifact_size
  local artifact_hash

  artifact_name="$(basename "$artifact")"
  artifact_size="$(wc -c <"$artifact" | tr -d ' ')"
  artifact_hash="$(sha256sum "$artifact" | awk '{print $1}')"
  jq -n \
    --arg version "$version" \
    --arg published_at "2026-07-27T00:00:00Z" \
    --arg minimum_os "10.0.17763" \
    --arg url "/downloads/windows/stable/$artifact_name" \
    --arg sha256 "$artifact_hash" \
    --arg signature "test-signature" \
    --argjson size "$artifact_size" \
    '{schema: 1, channel: "stable", version: $version, published_at: $published_at, minimum_os: $minimum_os, url: $url, size: $size, sha256: $sha256, signature: $signature}' >"$path"
}

make_release_input() {
  local directory="$1"
  local version="$2"
  local artifact="$directory/AceAgentSetup-windows-amd64-V${version}.exe"

  mkdir -p "$directory"
  printf 'installer bytes for %s\n' "$version" >"$artifact"
  write_manifest "$directory/latest.json" "$version" "$artifact"
}

run_publish() {
  local release_root="$1"
  local input_root="$2"
  local version="$3"
  env \
    PATH="$fake_bin:$PATH" \
    ACE_RELEASE_BIN="$fake_bin/ace-release" \
    "$publisher" \
    "$release_root" \
    "$input_root/AceAgentSetup-windows-amd64-V${version}.exe" \
    "$input_root/latest.json" \
    "$public_key"
}

run_publish_with_invalid_signature() {
  ACE_RELEASE_VERIFY_MODE=fail run_publish "$@"
}

run_publish_with_sync_failure() {
  FAKE_SYNC_FAIL=true run_publish "$@"
}

run_publish_with_commit_sync_failure() {
  rm -f "$sync_count_file"
  FAKE_SYNC_FAIL_AT=5 FAKE_SYNC_COUNT_FILE="$sync_count_file" run_publish "$@"
}

run_publish_with_rollback_ln_failure() {
  rm -f "$sync_count_file" "$ln_count_file"
  FAKE_SYNC_FAIL_AT=5 \
  FAKE_SYNC_COUNT_FILE="$sync_count_file" \
  FAKE_LN_FAIL_AT=5 \
  FAKE_LN_COUNT_FILE="$ln_count_file" \
  run_publish "$@"
}

run_publish_with_rollback_mv_failure() {
  rm -f "$sync_count_file" "$mv_count_file"
  FAKE_SYNC_FAIL_AT=5 \
  FAKE_SYNC_COUNT_FILE="$sync_count_file" \
  FAKE_MV_FAIL_AT=5 \
  FAKE_MV_COUNT_FILE="$mv_count_file" \
  run_publish "$@"
}

assert_publish_failure_contains() {
  local expected="$1"
  shift
  local output
  local status

  set +e
  output="$("$@" 2>&1)"
  status=$?
  set -e
  [[ $status -ne 0 ]] || fail "publication unexpectedly succeeded"
  [[ "$output" == *"$expected"* ]] || fail "expected error containing '$expected', got: $output"
}

fake_bin="$test_root/bin"
mkdir -p "$fake_bin"

cat >"$fake_bin/ace-release" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
[[ "${1:-}" == verify ]] || exit 21
shift
public_key=
artifact=
manifest=
while [[ $# -gt 0 ]]; do
  case "$1" in
    -public) public_key="$2"; shift 2 ;;
    -artifact) artifact="$2"; shift 2 ;;
    -manifest) manifest="$2"; shift 2 ;;
    *) exit 22 ;;
  esac
done
[[ -s "$public_key" && -s "$artifact" && -s "$manifest" ]] || exit 23
[[ "${ACE_RELEASE_VERIFY_MODE:-pass}" == pass ]] || {
  printf 'ace-release: manifest signature verification failed\n' >&2
  exit 1
}
[[ "$(jq -r '.signature' "$manifest")" == test-signature ]] || exit 24
[[ "$(jq -r '.size' "$manifest")" == "$(wc -c <"$artifact" | tr -d ' ')" ]] || exit 25
[[ "$(jq -r '.sha256' "$manifest")" == "$(sha256sum "$artifact" | awk '{print $1}')" ]] || exit 26
printf 'verified\n'
EOF
chmod +x "$fake_bin/ace-release"

cat >"$fake_bin/sync" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
if [[ "${FAKE_SYNC_FAIL:-false}" == true ]]; then
  printf 'injected sync failure\n' >&2
  exit 27
fi
if [[ -n "${FAKE_SYNC_FAIL_AT:-}" ]]; then
  count=0
  [[ ! -f "${FAKE_SYNC_COUNT_FILE:?}" ]] || count="$(<"$FAKE_SYNC_COUNT_FILE")"
  count=$((count + 1))
  printf '%s\n' "$count" >"$FAKE_SYNC_COUNT_FILE"
  if [[ "$count" == "$FAKE_SYNC_FAIL_AT" ]]; then
    printf 'injected sync failure at call %s\n' "$count" >&2
    exit 28
  fi
fi
exec /usr/bin/sync "$@"
EOF
chmod +x "$fake_bin/sync"
sync_count_file="$test_root/sync-count"

cat >"$fake_bin/ln" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
if [[ -n "${FAKE_LN_FAIL_AT:-}" ]]; then
  count=0
  [[ ! -f "${FAKE_LN_COUNT_FILE:?}" ]] || count="$(<"$FAKE_LN_COUNT_FILE")"
  count=$((count + 1))
  printf '%s\n' "$count" >"$FAKE_LN_COUNT_FILE"
  if [[ "$count" == "$FAKE_LN_FAIL_AT" ]]; then
    printf 'injected rollback ln failure at call %s\n' "$count" >&2
    exit 29
  fi
fi
exec /usr/bin/ln "$@"
EOF
chmod +x "$fake_bin/ln"
ln_count_file="$test_root/ln-count"

cat >"$fake_bin/mv" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
if [[ -n "${FAKE_MV_FAIL_AT:-}" ]]; then
  count=0
  [[ ! -f "${FAKE_MV_COUNT_FILE:?}" ]] || count="$(<"$FAKE_MV_COUNT_FILE")"
  count=$((count + 1))
  printf '%s\n' "$count" >"$FAKE_MV_COUNT_FILE"
  if [[ "$count" == "$FAKE_MV_FAIL_AT" ]]; then
    printf 'injected rollback mv failure at call %s\n' "$count" >&2
    exit 30
  fi
fi
exec /usr/bin/mv "$@"
EOF
chmod +x "$fake_bin/mv"
mv_count_file="$test_root/mv-count"

public_key="$test_root/update-signing.key.pub"
printf 'test-public-key\n' >"$public_key"

release_root="$test_root/releases"
first_input="$test_root/input-1.2.3"
make_release_input "$first_input" "1.2.3"
if ! first_output="$(run_publish "$release_root" "$first_input" "1.2.3" 2>&1)"; then
  fail "initial publication failed: $first_output"
fi

stable_root="$release_root/windows/stable"
version="1.2.3"
artifact_name="AceAgentSetup-windows-amd64-V${version}.exe"
version_root="$stable_root/releases/$version"
assert_file "$version_root/$artifact_name"
assert_file "$version_root/latest.json"
[[ "$(stat -c '%a' "$version_root")" == 755 ]] || fail "release directory mode is $(stat -c '%a' "$version_root"), want 755"
[[ "$(stat -c '%a' "$version_root/$artifact_name")" == 644 ]] || fail "versioned artifact mode is not 644"
[[ "$(stat -c '%a' "$version_root/latest.json")" == 644 ]] || fail "published manifest mode is not 644"
if sudo -n true >/dev/null 2>&1; then
  sudo -n -u nobody test -r "$version_root/$artifact_name" || fail "non-owner cannot read the versioned artifact"
  sudo -n -u nobody test -r "$version_root/latest.json" || fail "non-owner cannot read the published manifest"
fi
cmp "$first_input/$artifact_name" "$version_root/$artifact_name" >/dev/null || fail "published versioned artifact changed bytes"
cmp "$first_input/latest.json" "$version_root/latest.json" >/dev/null || fail "published manifest changed bytes"
assert_link_target "$stable_root/$artifact_name" "releases/$version/$artifact_name"
assert_link_target "$stable_root/AceAgentSetup-windows-amd64.exe" "releases/$version/$artifact_name"
assert_link_target "$stable_root/latest.json" "releases/$version/latest.json"
assert_no_temporary_entries "$stable_root"
pass_count=$((pass_count + 1))

old_manifest_hash="$(sha256sum "$stable_root/latest.json" | awk '{print $1}')"
old_alias_target="$(readlink "$stable_root/AceAgentSetup-windows-amd64.exe")"

older_input="$test_root/input-1.2.2"
make_release_input "$older_input" "1.2.2"
assert_publish_failure_contains "older than current stable release" run_publish "$release_root" "$older_input" "1.2.2"
[[ "$(sha256sum "$stable_root/latest.json" | awk '{print $1}')" == "$old_manifest_hash" ]] || fail "downgrade changed stable manifest"
[[ "$(readlink "$stable_root/AceAgentSetup-windows-amd64.exe")" == "$old_alias_target" ]] || fail "downgrade changed stable alias"
[[ ! -e "$stable_root/releases/1.2.2" ]] || fail "downgrade retained a release directory"
assert_no_temporary_entries "$stable_root"
pass_count=$((pass_count + 1))

assert_publish_failure_contains "already current" run_publish "$release_root" "$first_input" "1.2.3"
[[ "$(sha256sum "$stable_root/latest.json" | awk '{print $1}')" == "$old_manifest_hash" ]] || fail "equal version changed stable manifest"
assert_no_temporary_entries "$stable_root"
pass_count=$((pass_count + 1))

invalid_input="$test_root/input-1.2.4-invalid"
make_release_input "$invalid_input" "1.2.4"
assert_publish_failure_contains "manifest signature verification failed" run_publish_with_invalid_signature "$release_root" "$invalid_input" "1.2.4"
[[ "$(sha256sum "$stable_root/latest.json" | awk '{print $1}')" == "$old_manifest_hash" ]] || fail "failed verification changed stable manifest"
[[ ! -e "$stable_root/releases/1.2.4" ]] || fail "failed verification retained a release directory"
assert_no_temporary_entries "$stable_root"
pass_count=$((pass_count + 1))

rollback_input="$test_root/input-1.2.4-rollback"
make_release_input "$rollback_input" "1.2.4"
assert_publish_failure_contains "sync failure at call 5" run_publish_with_commit_sync_failure "$release_root" "$rollback_input" "1.2.4"
[[ "$(sha256sum "$stable_root/latest.json" | awk '{print $1}')" == "$old_manifest_hash" ]] || fail "commit sync failure did not restore stable manifest"
[[ "$(readlink "$stable_root/AceAgentSetup-windows-amd64.exe")" == "$old_alias_target" ]] || fail "commit sync failure did not restore stable alias"
[[ ! -e "$stable_root/releases/1.2.4" ]] || fail "commit sync failure retained a release directory"
[[ ! -e "$stable_root/AceAgentSetup-windows-amd64-V1.2.4.exe" && ! -L "$stable_root/AceAgentSetup-windows-amd64-V1.2.4.exe" ]] || fail "commit sync failure retained a versioned link"
assert_no_temporary_entries "$stable_root"
pass_count=$((pass_count + 1))

ln_rollback_root="$test_root/ln-rollback-releases"
run_publish "$ln_rollback_root" "$first_input" "1.2.3" >/dev/null
ln_rollback_input="$test_root/input-1.2.4-ln-rollback"
make_release_input "$ln_rollback_input" "1.2.4"
assert_publish_failure_contains "publication rollback was incomplete; release content retained for manual intervention" run_publish_with_rollback_ln_failure "$ln_rollback_root" "$ln_rollback_input" "1.2.4"
ln_stable_root="$ln_rollback_root/windows/stable"
assert_file "$ln_stable_root/releases/1.2.4/AceAgentSetup-windows-amd64-V1.2.4.exe"
assert_file "$ln_stable_root/releases/1.2.4/latest.json"
assert_link_target "$ln_stable_root/AceAgentSetup-windows-amd64-V1.2.4.exe" "releases/1.2.4/AceAgentSetup-windows-amd64-V1.2.4.exe"
[[ "$(jq -r '.version' "$ln_stable_root/latest.json")" == "1.2.4" ]] || fail "ln rollback failure did not retain the new stable manifest target"
assert_no_temporary_entries "$ln_stable_root"
pass_count=$((pass_count + 1))

mv_rollback_root="$test_root/mv-rollback-releases"
run_publish "$mv_rollback_root" "$first_input" "1.2.3" >/dev/null
mv_rollback_input="$test_root/input-1.2.4-mv-rollback"
make_release_input "$mv_rollback_input" "1.2.4"
assert_publish_failure_contains "publication rollback was incomplete; release content retained for manual intervention" run_publish_with_rollback_mv_failure "$mv_rollback_root" "$mv_rollback_input" "1.2.4"
mv_stable_root="$mv_rollback_root/windows/stable"
assert_file "$mv_stable_root/releases/1.2.4/AceAgentSetup-windows-amd64-V1.2.4.exe"
assert_file "$mv_stable_root/releases/1.2.4/latest.json"
assert_link_target "$mv_stable_root/AceAgentSetup-windows-amd64-V1.2.4.exe" "releases/1.2.4/AceAgentSetup-windows-amd64-V1.2.4.exe"
[[ "$(jq -r '.version' "$mv_stable_root/latest.json")" == "1.2.4" ]] || fail "mv rollback failure did not retain the new stable manifest target"
assert_no_temporary_entries "$mv_stable_root"
pass_count=$((pass_count + 1))

sync_failure_root="$test_root/sync-failure-releases"
sync_failure_input="$test_root/input-3.0.0"
make_release_input "$sync_failure_input" "3.0.0"
assert_publish_failure_contains "sync" run_publish_with_sync_failure "$sync_failure_root" "$sync_failure_input" "3.0.0"
[[ ! -e "$sync_failure_root/windows/stable/releases/3.0.0" ]] || fail "sync failure retained a release directory"
assert_no_temporary_entries "$sync_failure_root/windows/stable"
pass_count=$((pass_count + 1))

semver_root="$test_root/semver-releases"
prerelease_input="$test_root/input-1.0.0-rc.1"
stable_input="$test_root/input-1.0.0"
make_release_input "$prerelease_input" "1.0.0-rc.1"
make_release_input "$stable_input" "1.0.0"
run_publish "$semver_root" "$prerelease_input" "1.0.0-rc.1" >/dev/null
run_publish "$semver_root" "$stable_input" "1.0.0" >/dev/null
[[ "$(jq -r '.version' "$semver_root/windows/stable/latest.json")" == "1.0.0" ]] || fail "stable SemVer did not supersede prerelease"
assert_no_temporary_entries "$semver_root/windows/stable"
pass_count=$((pass_count + 1))

grep -Fxq 'deploy/secrets/' "$repo_root/.dockerignore" || fail "Docker build context does not exclude deploy/secrets/"
pass_count=$((pass_count + 1))

go_module_copy_line="$(grep -nFx 'COPY go.mod go.sum ./' "$repo_root/deploy/windows-builder.Dockerfile" | cut -d: -f1 || true)"
goproxy_arg_line="$(grep -nFx 'ARG GOPROXY=https://goproxy.cn,direct' "$repo_root/deploy/windows-builder.Dockerfile" | cut -d: -f1 || true)"
goproxy_run_line="$(grep -nFx 'RUN GOPROXY="${GOPROXY}" go mod download' "$repo_root/deploy/windows-builder.Dockerfile" | cut -d: -f1 || true)"
[[ -n "$goproxy_arg_line" && "$goproxy_arg_line" -gt "$go_module_copy_line" ]] || fail "Windows builder GOPROXY invalidates layers before Go module download"
[[ -n "$goproxy_run_line" && "$goproxy_run_line" -gt "$goproxy_arg_line" ]] || fail "Windows builder does not apply GOPROXY to Go module download"
grep -Fxq '        GOPROXY: ${GOPROXY:-https://goproxy.cn,direct}' "$repo_root/deploy/windows-builder.compose.yaml" || fail "Windows builder Compose does not provide the DSM GOPROXY default"
pass_count=$((pass_count + 1))

inventory_validator="$repo_root/scripts/validate-windows-installer-inventory.sh"
valid_inventory="$test_root/valid-installer-inventory.txt"
printf '%s\n' 'app/AceAgent.exe' 'app/AceAgentUpdater.exe' >"$valid_inventory"
"$inventory_validator" "$valid_inventory" || fail "valid dual-executable installer inventory was rejected"

agent_only_inventory="$test_root/agent-only-inventory.txt"
printf '%s\n' 'app/AceAgent.exe' >"$agent_only_inventory"
assert_publish_failure_contains "does not contain AceAgentUpdater.exe" "$inventory_validator" "$agent_only_inventory"

decoy_inventory="$test_root/decoy-installer-inventory.txt"
printf '%s\n' 'app/NotAceAgent.exe' 'app/NotAceAgentUpdater.exe' >"$decoy_inventory"
assert_publish_failure_contains "does not contain AceAgent.exe" "$inventory_validator" "$decoy_inventory"

driver_inventory="$test_root/driver-inventory.txt"
printf '%s\n' 'app/AceAgent.exe' 'app/AceAgentUpdater.exe' 'app/WinDivert64.sys' >"$driver_inventory"
assert_publish_failure_contains "contains a forbidden driver" "$inventory_validator" "$driver_inventory"
pass_count=$((pass_count + 1))

printf 'PASS: %d Windows release publication contract checks\n' "$pass_count"
