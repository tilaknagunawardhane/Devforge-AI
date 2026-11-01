#!/usr/bin/env bash
# GitOps demo: a DevForge-generated service is deployed and updated by Argo CD from a local Git server.
#
#   ./gitops/demo.sh [service-name]      (needs only Docker; runs the whole thing in containers)
#
# What it proves: the manifests the CLI generates really apply on Kubernetes, Argo CD syncs them from
# Git, a Git commit rolls out a new version, and Argo CD undoes manual drift (self-heal).
# Credentials below are for the throwaway local Gitea only.
set -euo pipefail
export MSYS_NO_PATHCONV=1   # keep Git Bash on Windows from rewriting container paths

SVC="${1:-demo-svc}"
ARGO_VERSION="${ARGO_VERSION:-v2.13.2}"
GITEA_USER=devforge
GITEA_PASS=devforge-local-demo
cd "$(dirname "$0")/.."
# Git Bash on Windows needs "D:/path" (pwd -W) for docker.exe; elsewhere plain pwd is right.
ROOT="$(pwd -W 2>/dev/null || pwd)"
OUT="$ROOT/gitops/out"
COMPOSE="docker compose --profile k8s --profile gitops"

step() { printf '\n\033[1m== %s\033[0m\n' "$*"; }
k() { $COMPOSE exec -T k3s kubectl "$@"; }
wait_for() { # description, attempts, command...
  local what="$1" tries="$2"; shift 2
  for _ in $(seq 1 "$tries"); do "$@" >/dev/null 2>&1 && return 0; sleep 3; done
  echo "TIMED OUT waiting for: $what" >&2; return 1
}

step "1/7 Start k3s (the cluster) and Gitea (the Git server)"
$COMPOSE up -d k3s gitea
wait_for "k3s ready" 60 k get nodes
wait_for "Gitea ready" 40 $COMPOSE exec -T gitea wget -qO- http://localhost:3000/api/healthz
$COMPOSE exec -T -u git gitea gitea admin user create --admin --username "$GITEA_USER" --password "$GITEA_PASS" \
  --email demo@example.com --must-change-password=false >/dev/null 2>&1 || echo "(Gitea admin user already exists)"
NET=$(docker inspect "$($COMPOSE ps -q k3s)" --format '{{range $k,$v := .NetworkSettings.Networks}}{{$k}}{{end}}')
GITEA_IP=$(docker inspect "$($COMPOSE ps -q gitea)" --format '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}')
REPO_URL="http://$GITEA_IP:3000/$GITEA_USER/devforge-manifests-$SVC.git"
echo "Gitea at $GITEA_IP (pods reach it by IP; the cluster's DNS cannot resolve Compose service names)"

step "2/7 Install Argo CD ($ARGO_VERSION, core install: controller + repo-server, no UI)"
k create namespace argocd >/dev/null 2>&1 || true
k apply -n argocd --server-side --force-conflicts \
  -f "https://raw.githubusercontent.com/argoproj/argo-cd/$ARGO_VERSION/manifests/core-install.yaml" >/dev/null
k -n argocd patch configmap argocd-cm --type merge -p '{"data":{"timeout.reconciliation":"15s"}}' >/dev/null
k -n argocd rollout restart statefulset/argocd-application-controller >/dev/null
k -n argocd rollout status deployment/argocd-repo-server --timeout=300s
k -n argocd rollout status statefulset/argocd-application-controller --timeout=300s

step "3/7 Generate the service with the DevForge CLI"
rm -rf "$OUT" && mkdir -p "$OUT"
docker build -q -t devforge-cli:gitops ./devforge-cli >/dev/null
docker run --rm -v "$OUT:/workspace" devforge-cli:gitops create --name "$SVC" --lang go --manifest-repo-url "$REPO_URL" | tail -3

step "4/7 Build the service image and load it into the cluster"
docker build -q -t "$SVC:v1" "$OUT/$SVC" >/dev/null
docker save "$SVC:v1" | $COMPOSE exec -T k3s ctr images import - >/dev/null
echo "imported $SVC:v1"

step "5/7 Push the manifests to Git (image set to $SVC:v1, as the CI pipeline would)"
curl_api() { docker run --rm --network "$NET" curlimages/curl -s -u "$GITEA_USER:$GITEA_PASS" -H 'Content-Type: application/json' "$@"; }
curl_api -X POST "http://gitea:3000/api/v1/user/repos" -d "{\"name\":\"devforge-manifests-$SVC\",\"private\":false}" >/dev/null || true
sed -i "s|image: .*|image: $SVC:v1|" "$OUT/$SVC/manifests/base/deployment.yaml"
git_in_repo() { docker run --rm --network "$NET" -v "$OUT/$SVC/manifests:/repo" -w /repo --entrypoint sh alpine/git -c "$1"; }
git_in_repo "git init -q -b main . 2>/dev/null || git init -q . ; git config user.email demo@example.com; git config user.name demo;
  git add -A; git commit -q -m 'initial manifests'; git remote remove origin 2>/dev/null; git remote add origin http://$GITEA_USER:$GITEA_PASS@gitea:3000/$GITEA_USER/devforge-manifests-$SVC.git;
  git push -q -f origin HEAD:main && echo pushed"

step "6/7 Register the Argo CD Application and wait for it to sync"
# The core install has no API server, which is what normally creates the "default" project.
k apply -n argocd -f - <<'EOF'
apiVersion: argoproj.io/v1alpha1
kind: AppProject
metadata:
  name: default
  namespace: argocd
spec:
  sourceRepos: ["*"]
  destinations:
    - {server: "https://kubernetes.default.svc", namespace: "*"}
  clusterResourceWhitelist:
    - {group: "*", kind: "*"}
EOF
k apply -n argocd -f - < "$OUT/$SVC/manifests/argocd/application-dev.yaml"
app_state() { k -n argocd get application "$SVC-dev" -o jsonpath='{.status.sync.status}/{.status.health.status}' 2>/dev/null; }
wait_for "application Synced/Healthy" 80 sh -c "[ \"\$($COMPOSE exec -T k3s kubectl -n argocd get application $SVC-dev -o jsonpath='{.status.sync.status}/{.status.health.status}' 2>/dev/null)\" = 'Synced/Healthy' ]" \
  || { echo "state: $(app_state)"; k -n argocd get application "$SVC-dev" -o jsonpath='{.status.conditions}' ; echo; exit 1; }
echo "Argo CD says: $(app_state)"
k -n "$SVC-dev" get deploy,pods,svc,hpa

step "7/7 GitOps in action: commit a new image tag, then try to cheat with kubectl"
docker tag "$SVC:v1" "$SVC:v2"
docker save "$SVC:v2" | $COMPOSE exec -T k3s ctr images import - >/dev/null
sed -i "s|image: .*|image: $SVC:v2|" "$OUT/$SVC/manifests/base/deployment.yaml"
git_in_repo "git add -A; git commit -q -m 'GitOps sync: update image to v2'; git push -q origin HEAD:main && echo pushed v2"
wait_for "rollout of v2" 60 sh -c "[ \"\$($COMPOSE exec -T k3s kubectl -n $SVC-dev get deploy $SVC -o jsonpath='{.spec.template.spec.containers[0].image}')\" = '$SVC:v2' ]"
k -n "$SVC-dev" rollout status "deployment/$SVC" --timeout=120s
echo "Deployment now runs: $(k -n "$SVC-dev" get deploy "$SVC" -o jsonpath='{.spec.template.spec.containers[0].image}')"

echo "-- manual drift: kubectl scale --replicas=3 (Git says 1)"
k -n "$SVC-dev" scale "deployment/$SVC" --replicas=3 >/dev/null
wait_for "self-heal back to 1 replica" 40 sh -c "[ \"\$($COMPOSE exec -T k3s kubectl -n $SVC-dev get deploy $SVC -o jsonpath='{.spec.replicas}')\" = '1' ]"
echo "Replicas after self-heal: $(k -n "$SVC-dev" get deploy "$SVC" -o jsonpath='{.spec.replicas}')"
echo "Final state: $(app_state)"
echo; echo "Done. Clean up with: $COMPOSE down -v"
