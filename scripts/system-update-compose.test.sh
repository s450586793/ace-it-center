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

require_command docker
require_command jq

env_file="$tmp/compose.env"
cat >"$env_file" <<EOF
ACE_HTTP_PORT=19060
ACE_DATA_DIR=$tmp/data
ACE_RELEASES_DIR=$tmp/releases
ACE_BACKEND_IMAGE=ghcr.io/s450586793/ace-it-center-backend
ACE_WEB_IMAGE=ghcr.io/s450586793/ace-it-center-web
ACE_UPDATER_IMAGE=ghcr.io/s450586793/ace-it-center-updater
ACE_IMAGE_TAG=stable
ACE_UPDATER_IMAGE_TAG=v0.4.1
ACE_UPDATER_TOKEN=0123456789abcdefghijklmnopqrstuvwxyzABCD
POSTGRES_DB=ace_it_center
POSTGRES_USER=ace
POSTGRES_PASSWORD=compose-contract-password
ACE_SECURE_COOKIES=false
TZ=Asia/Shanghai
EOF

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
assert_jq '
  ([.services.backend, .services.web, .services.postgres] | all((.volumes // []) | all(.target != "/var/run/docker.sock"))) and
  ([.services.updater.volumes[] | select(.source == "/var/run/docker.sock" and .target == "/var/run/docker.sock")] | length == 1)
' 'only updater may mount the Docker socket'
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
