.PHONY: up chaos down clean demo logs ps

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

logs:
	docker compose --profile k8s logs -f webhook chaos

ps:
	docker compose --profile k8s ps

down:
	docker compose --profile k8s down

# Also delete volumes (downloaded models, Grafana data)
clean:
	docker compose --profile k8s down -v
