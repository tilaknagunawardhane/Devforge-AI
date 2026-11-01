.PHONY: test lint verify smoke e2e gitops-demo up chaos chaos-pause chaos-resume chaos-report down clean demo logs ps

# Core stack: webhook, Ollama (pulls the model), Prometheus, Alertmanager, Grafana
up:
	docker compose up -d --build

# Core stack + disposable k3s cluster, demo app and the chaos engine
chaos:
	docker compose --profile k8s up -d --build

# Send a fake alert straight to the webhook (no cluster needed)
demo:
	docker compose exec -T webhook wget -qO- --header="Content-Type: application/json" \
		--post-data='{"alerts":[{"labels":{"pod":"demo-app-test-1234"}}]}' http://localhost:9090/alert || true
	docker compose logs webhook --tail 10

# Kill switch: stops new experiments immediately and aborts any running hold
chaos-pause:
	docker compose exec chaos touch /var/run/chaos/pause

chaos-resume:
	docker compose exec chaos rm -f /var/run/chaos/pause

# Latest report is written to ./reports/chaos-report.md after every experiment
chaos-report:
	@cat reports/chaos-report.md

# Same checks CI runs (each in a container; only Docker is needed)
test:
	bash scripts/test.sh

lint:
	bash scripts/lint.sh

# Generate go/python/node services with the CLI and prove the output works
verify:
	bash scripts/verify-generated.sh

# Bring the stack up and push a synthetic alert through it
smoke:
	bash scripts/smoke.sh

# Chaos kills pods in a throwaway k3s cluster; follow the alert to an incident
e2e:
	bash scripts/e2e-chaos.sh

# Argo CD + a local Git server: deploys a generated service and shows a Git commit roll out (needs only Docker)
gitops-demo:
	bash gitops/demo.sh

logs:
	docker compose --profile k8s logs -f webhook chaos

ps:
	docker compose --profile k8s ps

down:
	docker compose --profile k8s down

# Also delete volumes (downloaded models, Grafana data)
clean:
	docker compose --profile k8s down -v
