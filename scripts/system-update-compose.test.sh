#!/usr/bin/env bash

set -euo pipefail

fail() {
  printf 'FAIL: %s\n' "$1" >&2
  exit 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "$1 is required"
}

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

workflow="$repo_root/.github/workflows/ci-images.yml"
workflow_contains() {
  grep -Fqx -- "$1" "$workflow" || fail "$2"
}

workflow_contains '            push_on_main: true' 'backend/web main publication policy is missing'
workflow_contains '            push_on_main: false' 'updater main no-push policy is missing'
workflow_contains "          push: \${{ needs.release-tag.outputs.is_release_tag == 'true' || (github.ref == 'refs/heads/main' && matrix.push_on_main) }}" 'publish push expression does not distinguish updater on main'
workflow_contains "            type=raw,value=latest,enable=\${{ github.ref == 'refs/heads/main' && matrix.push_on_main }}" 'latest tag expression may include updater'
workflow_contains "            type=sha,prefix=sha-,enable=\${{ github.ref == 'refs/heads/main' && matrix.push_on_main }}" 'sha tag expression may include updater'
workflow_contains "            type=raw,value=\${{ github.ref_name }},enable=\${{ needs.release-tag.outputs.is_release_tag == 'true' }}" 'release version tag expression is missing'

matrix_policy="$(awk '
  /^          - target:/ { target=$3 }
  /^            push_on_main:/ { print target "=" $2 }
' "$workflow")"
[[ "$matrix_policy" == $'backend=true\nweb=true\nupdater=false' ]] || fail 'publish matrix main policy must keep updater build-only'

promotion="$(sed -n '/^  promote-stable:/,$p' "$workflow")"
[[ "$promotion" == *'      - publish'* ]] || fail 'stable promotion must wait for the full publish matrix'
[[ "$promotion" == *'ace-it-center-backend:stable'* && "$promotion" == *'ace-it-center-web:stable'* ]] || fail 'stable promotion must include backend and web'
[[ "$promotion" != *'ace-it-center-updater:stable'* ]] || fail 'updater must never be promoted to stable'

env_file="$tmp/compose.env"
cat >"$env_file" <<EOF
ACE_HTTP_PORT=19060
ACE_DATA_DIR=$tmp/data
ACE_RELEASES_DIR=$tmp/releases
ACE_BACKEND_IMAGE=ghcr.io/s450586793/ace-it-center-backend
ACE_WEB_IMAGE=ghcr.io/s450586793/ace-it-center-web
ACE_UPDATER_IMAGE=ghcr.io/s450586793/ace-it-center-updater
ACE_UPDATER_IMAGE_TAG=v0.4.1
ACE_UPDATER_TOKEN=0123456789abcdefghijklmnopqrstuvwxyzABCD
POSTGRES_DB=ace_it_center
POSTGRES_USER=ace
POSTGRES_PASSWORD=compose-contract-password
ACE_SECURE_COOKIES=false
TZ=Asia/Shanghai
EOF

if grep -q '^ACE_IMAGE_TAG=' "$env_file"; then
  fail 'compose contract must exercise the default backend/web stable tag'
fi

require_command docker
require_command jq

config="$tmp/compose.json"
(
  cd "$repo_root"
  docker compose --env-file "$env_file" config --format json
) >"$config"

assert_jq() {
  local assertion="$1"
  local message="$2"

  jq -e "$assertion" "$config" >/dev/null || fail "$message"
}

uses_only_internal_network='def uses_only_internal_network:
  if (.networks | type) == "array" then
    .networks == ["internal"]
  else
    (.networks | keys | sort) == ["internal"]
  end;'

assert_jq '
  (.services | keys | sort) == ["backend", "postgres", "updater", "web"]
' 'compose services must be postgres, backend, web, and updater'
assert_jq '
  .services.backend.image == "ghcr.io/s450586793/ace-it-center-backend:stable" and
  .services.web.image == "ghcr.io/s450586793/ace-it-center-web:stable" and
  .services.updater.image == "ghcr.io/s450586793/ace-it-center-updater:v0.4.1"
' 'application and updater image tags are incorrect'
assert_jq "
  $uses_only_internal_network
  [.services.backend, .services.web, .services.updater]
  | all(uses_only_internal_network)
" 'application and updater services must use only the internal network'
socket_boundary='
  ([.services.backend, .services.web, .services.postgres] | all((.volumes // []) | all(.source != "/var/run/docker.sock"))) and
  ([.services.updater.volumes[] | select(.source == "/var/run/docker.sock" and .target == "/var/run/docker.sock")] | length == 1)
'
assert_jq "$socket_boundary" 'only updater may mount the Docker socket'

mutated_config="$tmp/compose-socket-source-regression.json"
jq '.services.backend.volumes = ((.services.backend.volumes // []) + [{"type":"bind","source":"/var/run/docker.sock","target":"/tmp/not-the-docker-socket"}])' "$config" >"$mutated_config"
if jq -e "$socket_boundary" "$mutated_config" >/dev/null; then
  fail 'socket boundary accepted the Docker socket source on backend under another target'
fi
assert_jq '
  (.services.updater.ports // [] | length) == 0
' 'updater must not publish host ports'
assert_jq '
  [.services.updater.volumes[] | select(.target == "/config/compose.yaml" or .target == "/config/.env")]
  | length == 2 and all(.read_only == true)
' 'updater compose and environment mounts must be read-only'
assert_jq '
  [.services.updater.volumes[] | select(.target == "/state" or .target == "/backups")]
  | length == 2 and all(.read_only != true)
' 'updater state and backup mounts must be writable'
assert_jq '
  .services.backend.environment.ACE_UPDATER_URL == "http://updater:8090" and
  .services.backend.environment.ACE_UPDATER_TOKEN == .services.updater.environment.ACE_UPDATER_TOKEN and
  .services.updater.environment.ACE_COMPOSE_PROJECT == "ace-it-center" and
  .services.updater.environment.ACE_BACKEND_IMAGE == "ghcr.io/s450586793/ace-it-center-backend" and
  .services.updater.environment.ACE_WEB_IMAGE == "ghcr.io/s450586793/ace-it-center-web"
' 'backend and updater configuration is not fixed or token-shared'
assert_jq '
  .services.updater.cap_drop == ["ALL"] and
  .services.updater.security_opt == ["no-new-privileges:true"] and
  .services.updater.healthcheck.test == ["CMD", "wget", "-q", "-O", "-", "http://127.0.0.1:8090/health"] and
  .services.updater.depends_on.postgres.condition == "service_healthy"
' 'updater security, healthcheck, or PostgreSQL dependency is incorrect'
assert_jq '
  (.services.postgres.depends_on // {} | has("updater") | not) and
  (.services.postgres.volumes | length) == 1 and
  .services.postgres.volumes[0].target == "/var/lib/postgresql/data"
' 'postgres must not depend on updater or have its storage changed'

printf 'PASS: system update compose contract\n'
