#!/usr/bin/env bash
# Smoke test: bring up the core Compose stack and check that every part answers and the alert path
# works end to end (synthetic alert -> incident -> dedup -> pages). Needs only Docker.
# Set KEEP=1 to leave the stack running afterwards.
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

TOKEN="$(tr -d '[:space:]' < monitoring/webhook-token)"
cleanup() {
  local rc=$?
  if [ "$rc" -ne 0 ]; then   # keep the evidence: the teardown below removes the containers
    dc logs --no-color --tail 300 > "$ROOT/test-logs.txt" 2>&1 || true
    echo "container logs saved to test-logs.txt" >&2
  fi
  if [ "${KEEP:-0}" != "1" ]; then dc down -v >/dev/null 2>&1 || true; fi
}
trap cleanup EXIT
dc down -v >/dev/null 2>&1 || true

step "Start the stack (models: $OLLAMA_MODEL + $EMBED_MODEL)"
up_log="$(mktemp)"
if ! dc up -d --build > "$up_log" 2>&1; then
  dc ps -a >&2   # which service stopped?
  tail -40 "$up_log" >&2
  fail "docker compose up failed (output above)"
fi
pass "stack started"

step "Services answer"
wait_for "webhook healthy" 120 inside webhook http://localhost:9090/healthz
wait_for "Prometheus ready" 40 inside prometheus http://localhost:9090/-/ready
wait_for "Alertmanager ready" 40 inside alertmanager http://localhost:9093/-/ready
wait_for "Grafana healthy" 40 sh -c "docker compose exec -T grafana wget -qO- http://localhost:3000/api/health | grep -q ok"
wait_for "Grafana has the provisioned chaos dashboard" 40 sh -c "docker compose exec -T grafana wget -qO- 'http://admin:admin@localhost:3000/api/search?query=Chaos' | grep -q devforge-chaos"
wait_for "portal healthy" 40 inside portal http://localhost:8088/healthz
wait_for "Prometheus loaded the chaos alert rules" 40 sh -c "docker compose exec -T prometheus wget -qO- http://localhost:9090/api/v1/rules | grep -q ChaosFaultInjected"
wait_for "runbooks indexed into Qdrant" 60 sh -c "docker compose logs webhook 2>&1 | grep -q 'runbooks indexed'"

step "Alert path"
post() { dc exec -T alertmanager wget -qO- --header="Content-Type: application/json" "$@"; }
alert='{"groupKey":"smoke","alerts":[{"status":"firing","labels":{"alertname":"SmokeTest","pod":"web-5d-abcde","namespace":"smoke"}}]}'

if dc exec -T alertmanager wget -qO- --post-data="$alert" --header="Content-Type: application/json" http://webhook:9090/alert >/dev/null 2>&1; then
  fail "the webhook accepted an alert without the token"
else
  pass "alert without the token is rejected (401)"
fi
post --header="Authorization: Bearer $TOKEN" --post-data="$alert" http://webhook:9090/alert | contains '"created"' && pass "alert with the token creates an incident" || fail "alert was not accepted"
post --header="Authorization: Bearer $TOKEN" --post-data="$alert" http://webhook:9090/alert | contains '"deduplicated"' && pass "the same group again is deduplicated" || fail "dedup"
inside webhook http://localhost:9090/api/incidents.json | contains '"subject":"web-5d-abcde"' && pass "incident is in the JSON feed" || fail "incident feed"
check "incident page renders" inside webhook http://localhost:9090/incidents/1
check "audit page renders" inside webhook http://localhost:9090/audit
wait_for "portal lists the incident" 20 sh -c "docker compose exec -T portal wget -qO- http://localhost:8088/ | grep -q web-5d-abcde"

summary
