# DevForge project guide (start here)

This guide assumes you know nothing about the project. It explains what it is, the words you need, how
the pieces fit together, how to run it, and what every file is for.

---

## 1. What is this project, in plain words?

Imagine a company with many developers. Every time someone wants a new small program (a "service") running
on the company's servers, a lot of boring work is needed: write the code skeleton, write instructions to
package it, write instructions for the servers, set up monitoring, set up automatic testing. That takes
days or weeks. Later, when a service breaks at night, someone has to find out why.

**DevForge automates both halves:**

1. **Building (Day-1).** One command creates a complete, ready-to-run service with all the boring parts
   done properly: tests, monitoring, safe server settings, automatic checks, deployment files.
2. **Healing (Day-2).** Once services are running, DevForge breaks them on purpose (to prove they recover),
   notices failures, collects evidence, asks an AI to explain the likely cause, tells the team, and can
   *propose* a fix (roll back a bad release, raise a memory limit). A human always approves the fix.

The name for this kind of system is an **Internal Developer Platform (IDP)**. It is also a good example
of **DevOps/SRE** work (SRE = Site Reliability Engineering: keeping services up).

Everything uses free, open-source tools and runs in Docker, so anyone can try it on one computer.

---

## 2. Words you need (mini glossary)

| Word | Meaning |
|---|---|
| **Service / microservice** | A small program that does one job and talks over the network (HTTP). |
| **Docker / container / image** | A container is a program packed with everything it needs, so it runs the same everywhere. An image is the recipe/package it starts from. |
| **Docker Compose** | A tool that starts many containers together from one file (`docker-compose.yml`). |
| **Kubernetes (K8s)** | A system that runs containers across servers and keeps them alive. If a container dies, it starts a new one. |
| **Pod** | The smallest thing Kubernetes runs (one or more containers). |
| **Deployment** | A Kubernetes object that says "keep N copies of this pod running" and handles updates. |
| **k3s** | A small, free Kubernetes. Here it runs *inside a Docker container* as a throwaway practice cluster. |
| **Manifest** | A YAML file describing what Kubernetes should run. |
| **Kustomize** | A way to reuse one set of manifests with small changes per environment (dev, staging). |
| **Prometheus** | Collects numbers ("metrics") from services every few seconds and stores them. |
| **Alertmanager** | Receives alerts from Prometheus, groups them, and sends them to people or programs. |
| **Grafana** | Draws dashboards from Prometheus data. |
| **SLO / error budget** | A promise such as "99.9% of requests succeed". The 0.1% allowed to fail is the "budget". Burn-rate alerts warn when it is being used up too fast. |
| **Chaos engineering** | Breaking things on purpose, safely, to learn whether the system recovers. |
| **MTTR** | Mean Time To Recovery: how long it takes to be healthy again after a failure. |
| **RCA** | Root Cause Analysis: an explanation of *why* something broke. |
| **LLM / Ollama / Llama 3** | An LLM is an AI that writes text. Ollama runs one on your own computer; Llama 3 is the model. Nothing is sent to a paid cloud service. |
| **Embedding / RAG / Qdrant** | An embedding turns text into numbers so similar texts can be found. RAG (retrieval-augmented generation) means "look up relevant notes first, then give them to the AI". Qdrant is the free database that stores them. |
| **Runbook** | A written guide for fixing a known kind of problem. |
| **GitOps / Argo CD** | Keep the desired state of your servers in Git; a tool (Argo CD) makes the servers match Git automatically. |
| **CI/CD** | Automatic testing and delivery that runs when code is pushed (here: GitHub Actions). |
| **Webhook** | A web address that another program calls when something happens. |
| **Backstage** | A "developer portal" product; DevForge just writes the metadata file it reads. |

---

## 3. The big picture

```
                         DAY-1: BUILD
  you ──► devforge CLI ──► a new service (code, tests, Docker, K8s manifests,
                           alerts, dashboard, CI) ──► Git repos
                                                         │  Argo CD (GitOps)
                                                         ▼
                         DAY-2: RUN AND HEAL        Kubernetes (k3s)
  chaos engine ── breaks pods ───────────────────────────►│
                                                          ▼
                                   Prometheus ──► Alertmanager ──► webhook (Go)
                                                                     │
                      Kubernetes events, logs, metrics, runbooks ◄───┤ gathers evidence
                      Llama 3 (Ollama) writes the analysis ◄─────────┤
                      Slack message + Approve / Reject buttons ◄─────┤
                      rules propose: rollback, fix pull request      │
                      a person approves ─────────────────────────────┘ → action runs
                      everything saved: incidents + audit log (SQLite)
  portal (:8088) shows services, status, incidents
```

There are **three programs I wrote** and a set of **ready-made free tools** wired together:

| Mine | Language | Job |
|---|---|---|
| `devforge-cli` | Go | Creates services. Also serves the portal web page. |
| `devforge-chaos` | Python | Breaks things on purpose and times the recovery. |
| `devforge-webhook` | Go | The "brain" of Day-2: incidents, evidence, AI analysis, approvals. |

| Ready-made (not mine) | Job |
|---|---|
| k3s | The practice Kubernetes cluster |
| Prometheus, Alertmanager, Grafana | Metrics, alerts, dashboards |
| Ollama (+ Llama 3, nomic-embed-text) | The local AI |
| Qdrant | Stores runbook text for search |
| Gitea, Argo CD | Only used in the optional GitOps demo |

---

## 4. How to run it

### Requirements
- **Docker Desktop** (includes Docker Compose). Nothing else to install: no Go, Python or Kubernetes.
- About **15 GB free disk** and **8 GB RAM** recommended. The first run downloads the AI models.

### First run
```bash
git clone https://github.com/tilaknagunawardhane/Devforge-AI.git
cd Devforge-AI
cp .env.example .env          # on Windows PowerShell: Copy-Item .env.example .env
```

For a **quick first try**, open `.env` and set `OLLAMA_MODEL=qwen2.5:0.5b` (small, fast, but its answers
are weak). For real answers leave the default `llama3` (a 4.7 GB download; on a computer without a GPU
each analysis can take a minute or more).

Optional: put a Slack incoming-webhook URL in `SLACK_WEBHOOK_URL`. Without it, the analysis is printed in
the webhook's log and shown on the incident page instead.

```bash
docker compose --profile k8s up -d --build
```

This starts everything, including the practice cluster, a demo app (3 copies of nginx) and the chaos
engine. Wait a few minutes the first time (downloads and builds).

### Where to look

| What | Address |
|---|---|
| Portal (services, status, incidents) | http://localhost:8088 |
| Incidents, with the AI analysis | http://localhost:9090/incidents |
| Audit log (every proposed/approved action) | http://localhost:9090/audit |
| Grafana dashboards (login `admin` / `admin`) | http://localhost:3000 |
| Prometheus | http://localhost:9091 |
| Alertmanager | http://localhost:9093 |

Watch it work:
```bash
docker compose --profile k8s logs -f chaos webhook
```
You will see the chaos engine delete pods, Kubernetes replace them, an alert fire, and the webhook open
one incident and analyse it. A report is written to `reports/chaos-report.md`.

### Create your own service
```bash
docker build -t devforge-cli ./devforge-cli
docker run --rm -v "$PWD:/workspace" devforge-cli create --name billing-service --lang python --dry-run   # preview only
docker run --rm -v "$PWD:/workspace" devforge-cli create --name billing-service --lang python
docker run --rm -v "$PWD:/workspace" devforge-cli list
```
A folder `billing-service/` appears with the app and a `manifests/` folder. (In PowerShell use
`${PWD}` instead of `$PWD`.) Run `docker run --rm devforge-cli create --help` for all options.

### Stop and clean up
```bash
docker compose --profile k8s down        # stop, keep data
docker compose --profile k8s down -v     # stop and delete data (incidents, downloaded models)
```

### Useful shortcuts (need `make`)
`make up` · `make chaos` · `make chaos-pause` (emergency stop for chaos) · `make test` · `make lint` ·
`make verify` · `make smoke` · `make e2e` · `make gitops-demo`

### If something goes wrong
- **Nothing happens for a long time at first start:** the AI models are downloading; check
  `docker compose logs ollama-pull`.
- **A page loads forever and never shows (Windows with WSL2; this happened to the author):** WSL's own
  port forwarder (`wslrelay`) grabs `localhost` ports that Docker publishes on *all* interfaces and then
  does not pass the request on. The compose file avoids this by publishing every port on `127.0.0.1`
  only, so if you changed a `ports:` entry back to `"9090:9090"`, put `127.0.0.1:` in front again. To
  check: `Get-NetTCPConnection -LocalPort 8088 -State Listen` should show `com.docker.backend`, not
  `wslrelay`, on `127.0.0.1`. Opening `http://127.0.0.1:8088` instead of `localhost` is also a little
  faster, because the browser skips an IPv6 attempt that times out first.
- **The pages say "can't be reached" and `docker compose ps` shows services as `Created`:** one service they
  depend on failed to start. Run `docker compose logs ollama-pull`. The most common cause is a typo in the
  model name in `.env` (for example `OLLAMA_MODEL=OLLAMA_MODEL=qwen2.5:0.5b`, with the name written twice).
- **Port already in use:** another program uses 9090, 3000 or 8088; stop it or change the port in
  `docker-compose.yml`.
- **The analysis says "AI analysis unavailable":** Ollama was not ready or the model was missing. The
  webhook retries three times, then shows the raw evidence instead. Check `docker compose logs ollama`.

---

## 5. What happens, step by step

### Day-1: creating a service (`devforge create`)
1. `cmd/create.go` reads your flags (name, language, database, SLO...) and validates the name.
2. `cmd/scaffold.go` fills in **templates** (`cmd/templates/`) with your values. The templates use
   `[[ ]]` instead of `{{ }}` so they can contain Prometheus, Grafana and GitHub syntax unchanged.
3. Files are written: the app folder, and `manifests/` (which is meant to be its own Git repo).
4. If `GITHUB_USERNAME` and `GITHUB_TOKEN` are set, two GitHub repos are created and files uploaded
   (`cmd/github.go`). The service is recorded in a small catalog file (`cmd/catalog.go`).

What every generated service contains: `/health`, `/ready` and `/metrics` endpoints; unit tests; a
non-root Dockerfile; Kubernetes Deployment/Service/autoscaler/disruption budget with safe settings;
`overlays/dev` and `overlays/staging`; SLO alert rules plus a `promtool` test; a Grafana dashboard; two
Argo CD Applications; a Backstage `catalog-info.yaml`; a GitHub Actions pipeline that tests, scans for
secrets (gitleaks) and vulnerabilities (Trivy), builds, pushes and updates the manifest repo.

### Day-2: a failure becomes an incident
1. **Break:** `devforge-chaos/chaos.py` picks a random demo-app pod and deletes it (or stresses CPU/memory,
   or drains a node). Kubernetes starts a replacement. The engine measures how long until everything is
   healthy again and records it.
2. **Detect:** the engine exposes metrics; Prometheus evaluates `monitoring/alerts.yml` and fires
   `ChaosFaultInjected`. Alertmanager groups alerts of the same kind and sends them to the webhook.
3. **Group:** `devforge-webhook/incident.go` turns the group into **one incident** (several failing pods
   are one problem). Repeats are folded in (deduplication).
4. **Gather evidence:** `evidence.go` reads Kubernetes events, the pods' status and logs; `prometheus.go`
   reads metrics; `rag.go` finds matching sections of `runbooks/*.md` in Qdrant.
5. **Explain:** `rca.go` builds a prompt that says "use only this evidence" and asks Llama 3. If the model
   is down it retries, then posts the raw evidence instead.
6. **Notify:** `slack.go` posts the analysis (with Approve/Reject buttons if Slack is configured). The
   incident is saved in SQLite and shown on the web pages.
7. **Propose (only for real problems, not chaos):** `remediate.go` looks at pod status. A crash loop right
   after a new release gives a *proposed rollback*; an out-of-memory kill gives a *proposed pull request*
   raising the memory limit (`github_pr.go`).
8. **Approve:** a named person approves in Slack or via the API. Safety checks run first (allowed
   namespace, approver list, cooldown, opt-out label, expiry, one decision only). Then the action runs, the
   rollout is watched, and **every step is written to the audit log**.

---

## 6. Every file and folder

### Top level
| File | Purpose |
|---|---|
| `README.md` | The project front page, quick start, roadmap with checkboxes, CV bullets. |
| `docker-compose.yml` | Starts the whole platform. Profiles: default (core), `k8s` (cluster + chaos), `gitops` (Gitea). |
| `.env.example` | Settings template. Copy to `.env` (never commit `.env`). |
| `Makefile` | Short commands (`make test`, ...). |
| `.github/workflows/ci.yml` | GitHub Actions: lint, tests, builds, smoke and end-to-end tests. |
| `.golangci.yml`, `devforge-chaos/ruff.toml` | Linter settings. |
| `.gitattributes`, `.gitignore` | Line-ending rules (stops Windows breaking templates); files Git must ignore. |
| `LICENSE`, `CONTRIBUTING.md` | MIT license; how to contribute. |
| `alertmanager-values.yaml` | Old settings from the first Minikube/Helm version; not used by Compose. |

### `devforge-cli/` (Go): the service generator and portal
| File | Purpose |
|---|---|
| `main.go`, `cmd/root.go` | Program entry and the command tree. |
| `cmd/create.go` | The `create` command and its flags. |
| `cmd/scaffold.go` | Template engine: loads templates, fills values, decides which files exist. |
| `cmd/templates/` | The templates: `go/`, `python/`, `node/` (app code), `ci/`, `manifests/` (Kubernetes + alerts + dashboard), `kustomize/`, `argocd/`, `backstage/`. |
| `cmd/catalog.go` | `devforge list` and the JSON catalog of created services. |
| `cmd/ui.go` | `devforge ui`: the portal web page. |
| `cmd/github.go` | Creates repos and uploads files through the GitHub API. |
| `cmd/ai.go` | Optional (`--ai`): asks Ollama for a README. |
| `*_test.go`, `Dockerfile` | Tests; how the CLI image is built. |

### `devforge-chaos/` (Python): the chaos engine
| File | Purpose |
|---|---|
| `chaos.py` | Main loop, kill switch, shutdown, one experiment at a time. |
| `config.py` | Reads and validates settings (namespaces, interval, limits). Refuses unsafe settings. |
| `targets.py` | Chooses which pods may be attacked; decides what "healthy" means. |
| `experiments.py` | The four faults: pod-kill, cpu-stress, memory-stress, node-drain. Always cleans up. |
| `recovery.py` | Times the recovery (MTTR). |
| `metrics.py` | Prometheus metrics the engine exposes. |
| `report.py` | Writes `reports/chaos-report.md` and the history file. |
| `k8s_api.py` | Thin wrapper around the Kubernetes library (easy to fake in tests). |
| `rbac.yaml`, `chaos-pod.yaml` | Permissions and pod definition if you run the engine inside a cluster. |
| `tests/` | 26 tests using fake Kubernetes. |

### `devforge-webhook/` (Go): the Day-2 brain
| File | Purpose |
|---|---|
| `main.go` | Web server and routes (`/alert`, `/incidents`, `/audit`, `/actions/{id}/approve`, `/slack/actions`, ...). |
| `config.go` | All settings from environment variables. |
| `incident.go` | Grouping, deduplication, and the analysis flow. |
| `evidence.go` | Reads Kubernetes events, status, logs; finds OOM kills and crash loops. |
| `prometheus.go` | Asks Prometheus questions. |
| `rca.go` | Builds the AI prompt; talks to Ollama with retries and a fallback. |
| `rag.go` | Splits runbooks, stores/searches them in Qdrant. |
| `slack.go` | Slack messages, buttons, and signature checking. |
| `remediate.go` | Rules, safety checks, approval flow, rollback/restart, rollout watching, audit. |
| `github_pr.go` | Opens the "raise memory limit" pull request. Never merges. |
| `store.go`, `store_actions.go` | SQLite: incidents, actions, the append-only audit table. |
| `web.go` | The incident and audit web pages, and the JSON feed the portal reads. |
| `*_test.go` | 46 tests. |

### Other folders
| Folder | Purpose |
|---|---|
| `monitoring/` | Configs for Prometheus, Alertmanager and Grafana (including the chaos dashboard). `webhook-token` is the shared secret between Alertmanager and the webhook (a demo value; change it for anything real). |
| `runbooks/` | Four Markdown fix guides the AI can look up. Add your own `.md` files. |
| `scripts/` | Test scripts that CI also runs (`test`, `lint`, `verify-generated`, `smoke`, `e2e-chaos`). |
| `gitops/demo.sh` | The Argo CD demo (needs only Docker). |
| `docs/` | `design-decisions.md` (why and what I learned), `demo-script.md`, and this guide. |
| `examples/k8s/` | **Used.** `demo-app.yaml` is the 3-copy nginx app that the practice cluster runs and the chaos engine attacks. Do not delete. |
| `examples/billing-service/`, `examples/notification-api/` | Samples from the very first version (old layout). Nothing uses them; safe to delete. |
| `reports/` | Chaos reports are written here (ignored by Git). |

---

## 7. Safety rules (important to understand)

- The **AI only writes explanations**. It never runs commands. Actions come from simple rules reading
  structured facts (for example "container was OOMKilled"), so a tricky log line cannot make the AI do
  something.
- **Nothing changes a cluster without a named human approving it**, and only in allowed namespaces
  (`REMEDIATION_NAMESPACES`, default `default`; `kube-*` is always refused).
- The chaos engine has a **kill switch** (`make chaos-pause`), a **dry-run mode**, a limit on how many pods
  it touches, and it refuses to start on protected namespaces.
- The **audit log** cannot be edited or deleted: the database rejects it.
- The web pages have **no login**. Do not expose ports 9090 or 8088 to the internet.

---

## 8. What is tested, and what is not

**Tested here:** 95 unit tests; scripts that generate services in three languages and run them, start the
whole stack and send a test alert, and break pods in a real (practice) cluster and follow the alert to an
incident; linters; a secret scan; the Argo CD GitOps loop on a real cluster.

**Not tested against the real thing:** Slack (buttons need a public HTTPS address; I tested with signed
requests I built), the GitHub API (tested against a fake), and the GitHub Actions workflow (checked and
run locally but never run on GitHub). Llama 3 answers were not evaluated; most testing used a tiny model.

**Not built:** network-latency chaos, CPU/memory numbers in the AI analysis, OpenCost, the demo GIF.

---

## 9. Where to go next

1. Read the front `README.md`, then `docs/design-decisions.md` for the reasoning.
2. Run the platform (section 4) and watch an incident appear.
3. Read a small file end to end: `devforge-chaos/targets.py` or `devforge-webhook/store.go`.
4. Run `make test`, then change something and see a test fail.
5. Follow `docs/demo-script.md` to record your demo.
