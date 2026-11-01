#!/usr/bin/env bash
# The same linters CI runs, in containers.
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

for d in devforge-cli devforge-webhook; do
  step "golangci-lint $d"
  docker run --rm -v "$ROOT:/repo" -w "/repo/$d" golangci/golangci-lint:v1.64.8 golangci-lint run ./... \
    && pass "$d" || fail "$d"
done

step "ruff devforge-chaos"
docker run --rm -v "$ROOT/devforge-chaos:/app" -w /app python:3.12-slim sh -c "pip install -q ruff==0.8.4 >/dev/null 2>&1 && ruff check ." \
  && pass "devforge-chaos" || fail "devforge-chaos"

step "shellcheck scripts"
docker run --rm -v "$ROOT:/mnt" -w /mnt koalaman/shellcheck:v0.10.0 --severity=warning scripts/*.sh gitops/demo.sh \
  && pass "shell scripts" || fail "shell scripts"

step "actionlint workflows"
docker run --rm -v "$ROOT:/repo" -w /repo rhysd/actionlint:1.7.4 -color \
  && pass "workflows" || fail "workflows"

summary
