#!/usr/bin/env bash
# Unit tests for all three components, each in its own container, so nothing needs installing but Docker.
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

step "devforge-cli (Go)"
docker run --rm -v "$ROOT/devforge-cli:/app" -w /app golang:1.22 sh -c "go vet ./... && go test -count=1 ./..." \
  && pass "devforge-cli" || fail "devforge-cli"

step "devforge-webhook (Go, race detector on)"
docker run --rm -v "$ROOT/devforge-webhook:/app" -w /app golang:1.22 sh -c "go vet ./... && go test -race -count=1 ./..." \
  && pass "devforge-webhook" || fail "devforge-webhook"

step "devforge-chaos (Python)"
docker run --rm -v "$ROOT/devforge-chaos:/app" -w /app python:3.12-slim \
  sh -c "pip install -q -r requirements-dev.txt >/dev/null 2>&1 && python -m pytest -q -p no:cacheprovider" \
  && pass "devforge-chaos" || fail "devforge-chaos"

summary
