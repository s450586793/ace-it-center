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
require_command git
require_command sudo

read_env_value() {
  local key="$1"
  sed -n "s/^${key}=//p" .env | tail -n 1 | tr -d '\r'
}

project_root="$(git rev-parse --show-toplevel 2>/dev/null)" || fail "run this script inside the Git checkout"
cd "$project_root"

[[ -f .env ]] || fail ".env is required"
git diff --quiet || fail "tracked working tree changes must be committed or reverted"
git diff --cached --quiet || fail "staged changes must be committed or reverted"

branch="${ACE_DEPLOY_BRANCH:-main}"
mode="${ACE_DEPLOY_MODE:-$(read_env_value ACE_DEPLOY_MODE)}"
mode="${mode:-source}"
remote="${ACE_DEPLOY_REMOTE:-origin}"

git fetch --prune "$remote" "$branch"
git merge-base --is-ancestor HEAD "$remote/$branch" || fail "local revision is not an ancestor of $remote/$branch"
git merge --ff-only "$remote/$branch"

sudo docker compose config --quiet

case "$mode" in
  source)
    sudo docker compose build --pull backend web
    sudo docker compose up -d --remove-orphans
    ;;
  images)
    sudo docker compose pull backend web
    sudo docker compose up -d --no-build --remove-orphans
    ;;
  *)
    fail "ACE_DEPLOY_MODE must be source or images"
    ;;
esac

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

printf 'deployed revision %s in %s mode\n' "$(git rev-parse --short=12 HEAD)" "$mode"
