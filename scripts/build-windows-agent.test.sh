#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
build_script="$repo_root/scripts/build-windows-agent.sh"
asset_generator="$repo_root/scripts/generate-windows-installer-assets.go"
go_bin="${GO_BIN:-go}"
if [[ "$go_bin" == */* ]]; then
  [[ -x "$go_bin" ]] || {
    printf 'FAIL: GO_BIN is not executable: %s\n' "$go_bin" >&2
    exit 1
  }
else
  go_bin="$(command -v "$go_bin" 2>/dev/null)" || {
    printf 'FAIL: Go compiler is unavailable; set GO_BIN\n' >&2
    exit 1
  }
fi
test_root="$(mktemp -d)"
trap 'rm -rf "$test_root"' EXIT

pass_count=0

fail() {
  printf 'FAIL: %s\n' "$1" >&2
  exit 1
}

assert_failure_contains() {
  local expected="$1"
  shift
  local output
  local status

  set +e
  output="$("$@" 2>&1)"
  status=$?
  set -e

  if [[ $status -eq 0 ]]; then
    fail "command unexpectedly succeeded: $*"
  fi
  if [[ "$output" != *"$expected"* ]]; then
    fail "expected error containing '$expected', got: $output"
  fi
  pass_count=$((pass_count + 1))
}

assert_file() {
  local path="$1"
  [[ -f "$path" ]] || fail "expected file: $path"
}

valid_commit="0123456789ab"
valid_built_at="2026-07-27T08:09:10Z"
valid_key="$(printf 'test-public-key' | base64 -w0)"

assert_failure_contains "VERSION is required" env -u ACE_UPDATE_PUBLIC_KEY "$build_script"
invalid_versions=(
  v0.2.0
  01.2.3
  1.02.3
  1.2.03
  1.2.3-01
  1.2.3-alpha.01
  1.2.3-
  1.2.3-alpha..1
  1.2.3+
)
for invalid_version in "${invalid_versions[@]}"; do
  assert_failure_contains "VERSION must be a semantic version" env GO_BIN="$go_bin" ACE_UPDATE_PUBLIC_KEY="$valid_key" "$build_script" --agent-only "$invalid_version" "$valid_commit" "$valid_built_at" "$test_root/version"
done
assert_failure_contains "COMMIT must be a hexadecimal revision" env ACE_UPDATE_PUBLIC_KEY="$valid_key" "$build_script" 0.2.0 not-a-commit "$valid_built_at" "$test_root/commit"
assert_failure_contains "BUILT_AT must be a UTC RFC3339 timestamp" env ACE_UPDATE_PUBLIC_KEY="$valid_key" "$build_script" 0.2.0 "$valid_commit" 2026-07-27T08:09:10+08:00 "$test_root/time"
assert_failure_contains "BUILT_AT must be a valid UTC RFC3339 timestamp" env ACE_UPDATE_PUBLIC_KEY="$valid_key" "$build_script" --agent-only 0.2.0 "$valid_commit" 2026-99-27T08:09:10Z "$test_root/time-calendar"
assert_failure_contains "OUT_DIR must be an absolute path" env ACE_UPDATE_PUBLIC_KEY="$valid_key" "$build_script" 0.2.0 "$valid_commit" "$valid_built_at" relative-output
touch "$test_root/output-file"
assert_failure_contains "OUT_DIR is not a directory" env ACE_UPDATE_PUBLIC_KEY="$valid_key" "$build_script" --agent-only 0.2.0 "$valid_commit" "$valid_built_at" "$test_root/output-file"
assert_failure_contains "ACE_UPDATE_PUBLIC_KEY is required" env -u ACE_UPDATE_PUBLIC_KEY "$build_script" --agent-only 0.2.0 "$valid_commit" "$valid_built_at" "$test_root/key"
assert_failure_contains "ACE_UPDATE_PUBLIC_KEY must be valid base64" env ACE_UPDATE_PUBLIC_KEY='not base64!' "$build_script" --agent-only 0.2.0 "$valid_commit" "$valid_built_at" "$test_root/key-format"
assert_failure_contains "ACE_AGENT_SOURCE must not start with '-'" env GO_BIN="$go_bin" ACE_UPDATE_PUBLIC_KEY="$valid_key" ACE_AGENT_SOURCE=-agent-source "$build_script" --agent-only 0.2.0 "$valid_commit" "$valid_built_at" "$test_root/source-option"
assert_failure_contains "Agent source directory does not exist" env ACE_UPDATE_PUBLIC_KEY="$valid_key" ACE_AGENT_SOURCE="$test_root/missing-source" "$build_script" --agent-only 0.2.0 "$valid_commit" "$valid_built_at" "$test_root/source"
assert_failure_contains "ISCC is required in full-package mode" env -u ISCC ACE_UPDATE_PUBLIC_KEY="$valid_key" "$build_script" 0.2.0 "$valid_commit" "$valid_built_at" "$test_root/iscc-required"
assert_failure_contains "ISCC is not executable or unavailable" env ISCC="$test_root/missing-iscc" ACE_UPDATE_PUBLIC_KEY="$valid_key" "$build_script" 0.2.0 "$valid_commit" "$valid_built_at" "$test_root/iscc-missing"

fake_iscc="$test_root/fake-iscc"
printf '%s\n' \
  '#!/usr/bin/env bash' \
  'set -euo pipefail' \
  'app_version=' \
  'output_dir=' \
  'source_exe=' \
  'for argument in "$@"; do' \
  '  case "$argument" in' \
  '    /DAppVersion=*) app_version="${argument#/DAppVersion=}" ;;' \
  '    /DOutputDir=*) output_dir="${argument#/DOutputDir=}" ;;' \
  '    /DSourceExe=*) source_exe="${argument#/DSourceExe=}" ;;' \
  '  esac' \
  'done' \
  '[[ -n "$app_version" && -n "$output_dir" && -s "$source_exe" ]] || exit 31' \
  'if [[ "${FAKE_ISCC_MODE:-none}" == output ]]; then' \
  '  printf "fresh installer\n" >"$output_dir/AceAgentSetup-windows-amd64-V${app_version}.exe"' \
  'fi' >"$fake_iscc"
chmod +x "$fake_iscc"

stale_output="$test_root/stale full package"
mkdir -p "$stale_output"
stale_installer="$stale_output/AceAgentSetup-windows-amd64-V0.2.0.exe"
printf 'stale installer\n' >"$stale_installer"
assert_failure_contains "Inno Setup did not produce a fresh installer" env GO_BIN="$go_bin" ISCC="$fake_iscc" ACE_UPDATE_PUBLIC_KEY="$valid_key" "$build_script" 0.2.0 "$valid_commit" "$valid_built_at" "$stale_output"
[[ ! -e "$stale_installer" ]] || fail "stale installer was retained after a no-output ISCC run"

full_version="1.2.3-rc.1+build.01"
full_output="$test_root/fresh full package"
if ! full_build_output="$(env GO_BIN="$go_bin" ISCC="$fake_iscc" FAKE_ISCC_MODE=output ACE_UPDATE_PUBLIC_KEY="$valid_key" "$build_script" "$full_version" "$valid_commit" "$valid_built_at" "$full_output" 2>&1)"; then
  fail "fake full-package build failed: $full_build_output"
fi
fresh_installer="$full_output/AceAgentSetup-windows-amd64-V${full_version}.exe"
assert_file "$fresh_installer"
[[ "$(<"$fresh_installer")" == "fresh installer" ]] || fail "full-package build did not retain the fresh fake ISCC artifact"
[[ "$full_build_output" == *"$fresh_installer"*"SHA-256"* ]] || fail "full-package build output does not report the installer SHA-256"
pass_count=$((pass_count + 1))

ln -s "$repo_root/agent/cmd/ace-agent" "$test_root/Agent Source"
agent_output="$test_root/agent output with spaces"
if ! build_output="$(
  cd "$test_root"
  env GO_BIN="$go_bin" ACE_AGENT_SOURCE="Agent Source" ACE_UPDATE_PUBLIC_KEY="$valid_key" "$build_script" --agent-only 0.2.0 "$valid_commit" "$valid_built_at" "$agent_output" 2>&1
)"; then
  fail "agent-only build failed: $build_output"
fi
assert_file "$agent_output/AceAgent.exe"
file_output="$(file "$agent_output/AceAgent.exe")"
[[ "$file_output" == *"PE32+ executable (GUI) x86-64"* ]] || fail "unexpected Agent format: $file_output"
objdump_output="$(objdump -p "$agent_output/AceAgent.exe")"
[[ "$objdump_output" == *"Subsystem"*"Windows GUI"* ]] || fail "Agent is not a Windows GUI subsystem executable"
[[ "$build_output" == *"AceAgent.exe"*"SHA-256"* ]] || fail "build output does not report the Agent SHA-256"
[[ "$build_output" != *"$valid_key"* ]] || fail "build output exposed ACE_UPDATE_PUBLIC_KEY"
pass_count=$((pass_count + 1))

assets_one="$test_root/assets-one"
assets_two="$test_root/assets-two"
"$go_bin" run "$asset_generator" "$assets_one"
"$go_bin" run "$asset_generator" "$assets_two"
for asset in ace-agent.ico wizard-small.bmp wizard-large.bmp; do
  assert_file "$assets_one/$asset"
  cmp "$assets_one/$asset" "$assets_two/$asset" >/dev/null || fail "$asset is not deterministic"
done

ico_header="$(od -An -tx1 -N6 "$assets_one/ace-agent.ico" | tr -d ' \n')"
[[ "$ico_header" == "000001000500" ]] || fail "unexpected ICO header: $ico_header"
small_header="$(od -An -tx1 -N2 "$assets_one/wizard-small.bmp" | tr -d ' \n')"
large_header="$(od -An -tx1 -N2 "$assets_one/wizard-large.bmp" | tr -d ' \n')"
[[ "$small_header" == "424d" && "$large_header" == "424d" ]] || fail "wizard assets are not BMP files"

read_bmp_dimension() {
  local path="$1"
  od -An -tu4 -j18 -N8 "$path" | awk '{print $1 "x" $2}'
}

[[ "$(read_bmp_dimension "$assets_one/wizard-small.bmp")" == "55x58" ]] || fail "unexpected wizard-small.bmp dimensions"
[[ "$(read_bmp_dimension "$assets_one/wizard-large.bmp")" == "164x314" ]] || fail "unexpected wizard-large.bmp dimensions"
pass_count=$((pass_count + 1))

printf 'PASS: %d Windows Agent build contract checks\n' "$pass_count"
