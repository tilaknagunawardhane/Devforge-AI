#!/usr/bin/env bash
# Generates a service in every language with the CLI and proves the output really works:
# its own tests pass, the image builds and serves /health /ready /metrics as a non-root user on a
# read-only filesystem, the SLO alert rules pass promtool (including their unit test), and the
# Kustomize overlays render. Needs only Docker.
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

WORK="$ROOT/.verify-generated"
CLI_IMAGE="devforge-cli:verify"
KUBECTL_IMAGE="rancher/k3s:v1.30.5-k3s1"   # contains kubectl (for `kubectl kustomize`)
PROM_IMAGE="prom/prometheus:v2.54.1"        # contains promtool
cleanup() {
  docker rm -f verify-svc-run >/dev/null 2>&1 || true
  rm -rf "$WORK"
}
trap cleanup EXIT
rm -rf "$WORK" && mkdir -p "$WORK"

step "Build the CLI image"
docker build -q -t "$CLI_IMAGE" ./devforge-cli >/dev/null
pass "devforge-cli image builds"
cli() { docker run --rm -v "$WORK:/workspace" -e DEVFORGE_CATALOG=/workspace/.catalog.json "$CLI_IMAGE" "$@"; }

step "CLI behaviour"
cli create -n dry-svc -l python --dry-run | contains "Dry run: nothing was written" && pass "--dry-run prints a plan"
[ ! -e "$WORK/dry-svc" ] && pass "--dry-run writes nothing"
if cli create -n "Bad_Name" >/dev/null 2>&1; then fail "an invalid name must be rejected"; else pass "invalid name rejected"; fi
if cli create -n x -l cobol >/dev/null 2>&1; then fail "an unknown language must be rejected"; else pass "unknown language rejected"; fi

for lang in go python node; do
  name="svc-$lang"
  step "Generated $lang service"
  cli create -n "$name" -l "$lang" >/dev/null
  dir="$WORK/$name"
  [ -f "$dir/catalog-info.yaml" ] && [ -f "$dir/manifests/argocd/application-dev.yaml" ] && pass "$name has catalog-info.yaml and Argo CD applications"

  case "$lang" in
    go)     docker run --rm -v "$dir:/app" -w /app golang:1.22 sh -c "go mod tidy >/dev/null 2>&1; go vet ./... && go test ./..." >/dev/null 2>&1 ;;
    python) docker run --rm -v "$dir:/app" -w /app python:3.12-slim sh -c "pip install -q -r requirements-dev.txt >/dev/null 2>&1 && pytest -q" >/dev/null 2>&1 ;;
    node)   docker run --rm -v "$dir:/app" -w /app node:20-alpine sh -c "npm install --no-audit --no-fund >/dev/null 2>&1 && npm test" >/dev/null 2>&1 ;;
  esac && pass "$name: its own tests pass" || fail "$name: its own tests failed"

  docker build -q -t "verify-$name:test" "$dir" >/dev/null && pass "$name: image builds"
  docker rm -f verify-svc-run >/dev/null 2>&1 || true
  docker run -d --name verify-svc-run --read-only "verify-$name:test" >/dev/null
  sleep 4
  probe() { docker run --rm --network container:verify-svc-run curlimages/curl -s -o /dev/null -w '%{http_code}' "localhost:8080$1"; }
  for path in / /health /ready; do
    [ "$(probe $path)" = "200" ] && pass "$name: GET $path = 200" || fail "$name: GET $path"
  done
  [ "$(probe /nope)" = "404" ] && pass "$name: unknown path = 404"
  docker run --rm --network container:verify-svc-run curlimages/curl -s localhost:8080/metrics | grep -q '^http_requests_total' \
    && pass "$name: /metrics exposes http_requests_total"
  docker rm -f verify-svc-run >/dev/null
  docker rmi -f "verify-$name:test" >/dev/null 2>&1 || true
done

step "Alert rules and manifests"
mon="$WORK/svc-go/manifests/monitoring"
docker run --rm -v "$mon:/m" --entrypoint promtool "$PROM_IMAGE" check rules /m/alerts.rules.yml >/dev/null 2>&1 && pass "promtool check rules"
docker run --rm -v "$mon:/m" --entrypoint promtool "$PROM_IMAGE" test rules /m/alerts.rules.test.yml >/dev/null 2>&1 && pass "promtool test rules (burn-rate alerts fire and stay quiet correctly)"
cli create -n slo-95 -l go --slo 95 >/dev/null
docker run --rm -v "$WORK/slo-95/manifests/monitoring:/m" --entrypoint promtool "$PROM_IMAGE" test rules /m/alerts.rules.test.yml >/dev/null 2>&1 && pass "promtool test also passes for a 95% SLO"
cli create -n op-svc -l go --prometheus-operator >/dev/null

kustomize() { docker run --rm -v "$1:/m:ro" --entrypoint kubectl "$KUBECTL_IMAGE" kustomize "/m/$2"; }
for env in dev staging; do
  out="$(kustomize "$WORK/svc-go/manifests" "overlays/$env")"
  echo "$out" | contains "kind: Deployment" && echo "$out" | contains "namespace: svc-go-$env" && pass "overlay $env renders"
  echo "$out" | contains "kind: ServiceMonitor" && fail "overlay $env must not need Prometheus Operator CRDs" || pass "overlay $env needs no extra CRDs"
done
kustomize "$WORK/op-svc/manifests" overlays/dev | contains "kind: ServiceMonitor" && pass "--prometheus-operator adds the ServiceMonitor"

summary
