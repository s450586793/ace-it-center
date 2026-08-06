#!/usr/bin/env bash

set -euo pipefail

fail() {
  printf 'error: %s\n' "$1" >&2
  exit 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "$1 is required"
}

require_command curl
require_command docker
require_command sudo

read_env_value() {
  local key="$1"
  sed -n "s/^${key}=//p" .env | tail -n 1 | tr -d '\r'
}

project_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$project_root"

[[ -f .env ]] || fail ".env is required"

sudo docker compose config --quiet
sudo docker compose pull backend web
sudo docker compose up -d --no-build --remove-orphans

http_port="$(sed -n 's/^ACE_HTTP_PORT=//p' .env | tail -n 1)"
http_port="${http_port:-9060}"
[[ "$http_port" =~ ^[0-9]+$ ]] || fail "ACE_HTTP_PORT must be numeric"

healthy=false
for _ in $(seq 1 60); do
  if curl --fail --silent --show-error --max-time 5 "http://127.0.0.1:${http_port}/api/v1/health" >/dev/null; then
    healthy=true
    break
  fi
  sleep 2
done
[[ "$healthy" == true ]] || fail "deployment did not become healthy"

sudo docker compose ps

if ! bash scripts/cleanup-dsm-images.sh; then
  printf 'warning: deployment is healthy, but unused project image cleanup was incomplete\n' >&2
fi

image_tag="${ACE_IMAGE_TAG:-$(read_env_value ACE_IMAGE_TAG)}"
printf 'deployed image tag %s\n' "${image_tag:-latest}"
