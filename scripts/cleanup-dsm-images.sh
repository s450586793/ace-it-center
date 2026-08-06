#!/usr/bin/env bash

set -euo pipefail

fail() {
  printf 'error: %s\n' "$1" >&2
  exit 1
}

command -v sudo >/dev/null 2>&1 || fail "sudo is required"

read_env_value() {
  local key="$1"
  [[ -f .env ]] || return 0
  sed -n "s/^${key}=//p" .env | tail -n 1 | tr -d '\r'
}

backend_image="${ACE_BACKEND_IMAGE:-$(read_env_value ACE_BACKEND_IMAGE)}"
backend_image="${backend_image:-ghcr.io/s450586793/ace-it-center-backend}"
web_image="${ACE_WEB_IMAGE:-$(read_env_value ACE_WEB_IMAGE)}"
web_image="${web_image:-ghcr.io/s450586793/ace-it-center-web}"

collect_candidates() {
  sudo docker image ls --no-trunc --quiet --filter label=com.docker.compose.project=ace-it-center
  sudo docker image ls --no-trunc --quiet --filter label=com.docker.compose.project=ace-it-center-windows-builder
  sudo docker image ls --no-trunc --quiet ace-it-center-backend
  sudo docker image ls --no-trunc --quiet ace-it-center-web
  sudo docker image ls --no-trunc --quiet ace-it-center-windows-builder-windows-builder
  sudo docker image ls --no-trunc --quiet ace-it-go-base-test
  sudo docker image ls --no-trunc --quiet "$backend_image"
  sudo docker image ls --no-trunc --quiet "$web_image"
}

mapfile -t image_ids < <(collect_candidates | awk 'NF && !seen[$0]++')

removed=0
retained=0
for image_id in "${image_ids[@]}"; do
  [[ "$image_id" =~ ^(sha256:)?[0-9a-f]{12,64}$ ]] || fail "Docker returned an invalid image ID"
  if [[ -n "$(sudo docker ps -aq --filter "ancestor=$image_id")" ]]; then
    retained=$((retained + 1))
    continue
  fi
  if sudo docker image rm "$image_id" >/dev/null; then
    removed=$((removed + 1))
  else
    printf 'warning: could not remove unused image %s\n' "$image_id" >&2
  fi
done

printf 'project image cleanup: removed=%d retained=%d\n' "$removed" "$retained"
