#!/usr/bin/env bash
# Shared helpers for the test scripts. Source this file; do not run it.
set -euo pipefail
export MSYS_NO_PATHCONV=1   # keep Git Bash on Windows from rewriting container paths

# Git Bash on Windows needs "D:/path" (pwd -W) for docker.exe; elsewhere plain pwd is right.
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && (pwd -W 2>/dev/null || pwd))"
cd "$ROOT"

# A dedicated Compose project, so test runs never touch the volumes (models, incidents) of a real stack.
export COMPOSE_PROJECT_NAME="${COMPOSE_PROJECT_NAME:-devforge-test}"
# Small models keep CI fast; the embedding model must be a real embedding model.
export OLLAMA_MODEL="${OLLAMA_MODEL:-qwen2.5:0.5b}"
export EMBED_MODEL="${EMBED_MODEL:-nomic-embed-text}"

PASSED=0
step() { printf '\n\033[1m== %s\033[0m\n' "$*"; }
pass() { PASSED=$((PASSED + 1)); printf '  \033[32mok\033[0m   %s\n' "$*"; }
fail() { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; exit 1; }

# check "description" command...   -> runs the command, fails the script if it does not succeed
check() {
  local what="$1"; shift
  if "$@" >/dev/null 2>&1; then pass "$what"; else fail "$what"; fi
}

# wait_for "description" attempts command...   (3 seconds between attempts)
wait_for() {
  local what="$1" tries="$2"; shift 2
  local i
  for ((i = 0; i < tries; i++)); do
    if "$@" >/dev/null 2>&1; then pass "$what"; return 0; fi
    sleep 3
  done
  fail "timed out waiting for: $what"
}

dc() { docker compose "$@"; }
# Run wget inside a service container (busybox wget exists in the alpine-based images we use).
inside() { local svc="$1"; shift; dc exec -T "$svc" wget -qO- "$@"; }
contains() { grep -q -- "$1"; }

# Delete a folder that containers wrote into. Containers run as root, and on Linux (CI) a normal user
# cannot delete root-owned files, so the delete is done from a container too. Never fails the caller:
# a clean-up problem must not turn a passing run into a failing one.
wipe_dir() {
  [ -e "$1" ] || return 0
  docker run --rm -v "$1:/wipe" python:3.12-slim sh -c 'rm -rf /wipe/* /wipe/.[!.]* /wipe/..?*' >/dev/null 2>&1 || true
  rm -rf "$1" 2>/dev/null || true
}

summary() { printf '\n\033[1m%d checks passed\033[0m\n' "$PASSED"; }
