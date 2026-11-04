#!/usr/bin/env bash
# End-to-end test of the Day-2 loop on a real (throwaway k3s) cluster:
#   chaos engine kills pods -> Kubernetes heals them -> Prometheus fires an alert
#   -> Alertmanager calls the webhook -> one incident is created -> a report is written.
# Uses k3s in Docker (the cluster this project already ships) instead of kind: same idea, no extra install.
# Set KEEP=1 to leave the stack running afterwards.
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

export CHAOS_INTERVAL_SECONDS=15 CHAOS_MAX_TARGETS=2 CHAOS_EXPERIMENTS=pod-kill
dcp() { docker compose --profile k8s "$@"; }
cleanup() {
  local rc=$?
  if [ "$rc" -ne 0 ]; then   # keep the evidence: the teardown below removes the containers
    dcp logs --no-color --tail 300 > "$ROOT/test-logs.txt" 2>&1 || true
    echo "container logs saved to test-logs.txt" >&2
  fi
  if [ "${KEEP:-0}" != "1" ]; then dcp down -v >/dev/null 2>&1 || true; fi
}
trap cleanup EXIT
dcp down -v >/dev/null 2>&1 || true
rm -f reports/results.jsonl reports/chaos-report.md

step "Start the stack with a cluster, a demo app and the chaos engine"
up_log="$(mktemp)"
if ! dcp up -d --build > "$up_log" 2>&1; then
  tail -40 "$up_log" >&2
  fail "docker compose up failed (output above)"
fi
pass "stack started"
wait_for "demo app has 3 ready replicas" 100 sh -c "[ \"\$(docker compose --profile k8s exec -T k3s kubectl get deploy demo-app -o jsonpath='{.status.readyReplicas}')\" = '3' ]"

step "Chaos kills pods and the cluster heals"
wait_for "chaos engine killed pods" 60 sh -c "docker compose --profile k8s logs chaos 2>&1 | grep -q 'experiment=pod-kill result=success'"
wait_for "demo app is back to 3 ready replicas" 60 sh -c "[ \"\$(docker compose --profile k8s exec -T k3s kubectl get deploy demo-app -o jsonpath='{.status.readyReplicas}')\" = '3' ]"
dcp logs chaos 2>&1 | grep -E 'recovery=' | grep -q 'recovery=' && pass "recovery time (MTTR) was measured"

step "Monitoring and incident"
wait_for "Prometheus alert ChaosFaultInjected is firing" 60 sh -c "docker compose --profile k8s exec -T prometheus wget -qO- http://localhost:9090/api/v1/alerts | grep -q '\"state\":\"firing\"'"
check "recovery time is exported to Prometheus" sh -c "docker compose --profile k8s exec -T prometheus wget -qO- 'http://localhost:9090/api/v1/query?query=chaos_last_recovery_seconds' | grep -q pod-kill"
wait_for "webhook opened an incident for demo-app" 60 sh -c "docker compose --profile k8s exec -T webhook wget -qO- http://localhost:9090/api/incidents.json | grep -q demo-app"
feed="$(dcp exec -T webhook wget -qO- http://localhost:9090/api/incidents.json)"
echo "$feed" | grep -q '"pendingActions":0' && pass "no remediation was proposed for a deliberate chaos fault" || fail "chaos faults must not produce proposals"
[ "$(echo "$feed" | grep -o '"id":' | wc -l | tr -d ' ')" = "1" ] && pass "all pods of the wave are in ONE incident" || fail "grouping: expected exactly one incident"

step "Report"
wait_for "chaos report written" 10 sh -c "grep -q pod-kill reports/chaos-report.md"

summary
