# Recording the demo

A 4-minute walkthrough that shows the whole story. I could not capture screenshots or a GIF for you
(that needs a screen and your Slack workspace), so this is the script and checklist to record them yourself
with any free recorder (ScreenToGif or OBS on Windows).

Before you start: `cp .env.example .env`, add your `SLACK_WEBHOOK_URL` and `SLACK_SIGNING_SECRET` if you
want real Slack messages, and pull the models once (`docker compose up -d` does it).

## 1. Create a service (45 s)

```bash
docker build -t devforge-cli ./devforge-cli
docker run --rm -v "$PWD:/workspace" devforge-cli create --name billing-service --lang python --dry-run
docker run --rm -v "$PWD:/workspace" devforge-cli create --name billing-service --lang python
```

Point at: the file list (tests, Dockerfile, CI with gitleaks and Trivy, Kustomize overlays, Argo CD
Applications, SLO alert rules, dashboard, `catalog-info.yaml`). Open `billing-service/manifests/monitoring/alerts.rules.yml`.

## 2. Start the platform (30 s)

```bash
docker compose --profile k8s up -d --build
```

Open the portal at http://localhost:8088 and Grafana at http://localhost:3000.
**Screenshot 1:** the portal with the service list.

## 3. Break things on purpose (60 s)

```bash
make logs
```

Show the chaos engine killing pods and the recovery time it measures. Open the Grafana dashboard
**Chaos Engineering - Faults vs Recovery**. **Screenshot 2:** that dashboard.

## 4. The incident (60 s)

Open http://localhost:9090/incidents. Explain: several pods failed, but there is **one** incident (grouping
and dedup). Open it: the RCA, the timeline, the runbook it cited.
**Screenshot 3:** the incident page. **Screenshot 4:** the Slack message if you configured Slack.

## 5. A human in the loop (45 s)

Break a deployment on purpose (see `gitops/demo.sh` for a crash-loop example), wait for the proposed
rollback, then approve it from Slack or:

```bash
curl -X POST -H "Authorization: Bearer $(cat monitoring/webhook-token)" -H "X-Actor: you" http://localhost:9090/actions/1/approve
```

Open http://localhost:9090/audit. **Screenshot 5:** the audit log showing proposed, approved, executed, verified.

## 6. GitOps (optional, 30 s)

```bash
make gitops-demo
```

Shows Argo CD syncing the generated service, a Git commit rolling out, and self-heal undoing a manual change.

## Where to put the files

Save screenshots in `docs/img/` and link them from the README, for example
`![Incident page](docs/img/incident.png)`.
