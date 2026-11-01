# Design decisions and what I learned

This is the "why" behind DevForge: the choices I made, the bugs that taught me something, and what I
would do differently. Everything here happened in this repository; each point can be found in the code
or the tests.

## The shape of the system

- **Day-1:** a CLI renders tested templates into a service with health checks, metrics, tests, hardened
  Kubernetes manifests, alert rules, a dashboard and a CI pipeline, then pushes it to Git.
- **Day-2:** a chaos engine breaks things on purpose; Prometheus notices; a webhook turns the alerts into
  one incident, gathers evidence, asks a local LLM for a root-cause analysis, and posts it. Rules may
  *propose* a fix; a person approves; everything is audited.

## Decisions

### 1. Templates write the service code, not the LLM
The first version asked Llama 3 for the code. It sometimes returned invalid JSON, wrong ports, or no
metrics endpoint, and the generated CI file was not even valid YAML. I replaced it with tested templates
(Go, Python, Node) so every service is guaranteed to have `/health`, `/ready` and `/metrics`, and the
alerts and dashboard can depend on exact metric names.
*Trade-off:* less flexible than free-form generation. The LLM now only writes optional prose (a README).

### 2. The LLM explains; rules decide; people approve
The model sees evidence and writes an analysis. It never chooses or runs an action. Remediation rules read
**structured pod status** (OOMKilled, CrashLoopBackOff, rollout history), produce *proposals*, and a named
person must approve each one. Before anything runs: namespace allow-list, optional approver list, opt-out
label, cooldown, expiry, single-use approval. Every step goes to an append-only audit log.
*Why:* pod logs and events are untrusted text that ends up in the prompt, so a crafted log line could try
to steer the model. Because no action depends on the model's text, that cannot become a command.
*Trade-off:* slower than full automation. For a platform that can change a cluster, I chose safe over fast.

### 3. Alerts become incidents
One bad deploy can fire an alert per pod. Alertmanager groups by name and namespace, and the webhook folds
repeat notifications into the open incident. The result is one RCA and one Slack message instead of a
flood. The alert count only grows by *new* pods, not by Alertmanager's periodic re-sends.

### 4. Evidence first, then the prompt
Before asking the model anything, the webhook collects Kubernetes events, sibling-pod status and logs,
Prometheus stats, and matching runbook sections. The prompt says "use only this evidence; if it does not
show a cause, say so" and lists what could not be collected. A pod deleted by chaos has no logs, so the
webhook reads a replacement pod's logs and says that is what it did.

### 5. Proposals do not wait for the LLM
Proposals come from rules, not the model, so they are created *before* the slow LLM call. A slow or
unavailable model never delays or blocks remediation, and the RCA falls back to raw evidence if the model
is down.

### 6. Only human-acknowledged analyses are remembered
Runbooks are indexed for retrieval (Qdrant). Past RCAs are added only after someone clicks Acknowledge.
Indexing every model answer made a weak answer rank first for later, similar incidents.

### 7. The audit log is append-only in the database, not just in code
SQLite triggers make `UPDATE` and `DELETE` on the audit table fail. Entries are also written to the JSON
log, so the trail exists in two places.

### 8. Free, local, and in Docker
Ollama, Qdrant, Prometheus, Alertmanager, Grafana, k3s, Gitea and Argo CD are all free and open source and
run in containers, so `docker compose up` works for anyone. Pure-Go SQLite (no CGO) keeps the webhook a
small static binary.

### 9. Kustomize, with monitoring CRDs as an opt-in component
`base/` applies on any cluster. `ServiceMonitor` and `PrometheusRule` need Prometheus Operator CRDs, so
they live in a Kustomize component that the overlays include only with `--prometheus-operator`.

## Bugs that taught me something

| Bug | What it taught me |
|---|---|
| The pod-kill alert never fired. `increase()` of a counter that has just appeared is 0, because there is no earlier sample. | Test alert rules against real data. I switched to a "last injection timestamp" gauge, and later generated `promtool` unit tests for the SLO rules. |
| The incident count jumped from 27 to 36 with no new failures. Alertmanager re-sends a still-firing group and I counted each re-send. | Notification delivery is not event delivery. Count distinct things, not messages. |
| Runbook search returned nothing. I had reused the chat model for embeddings and chat models can't embed. | Use the right model for the job, and surface the error (it was logged clearly). |
| Approvals returned 404 until the model finished answering. Proposals were created after the LLM call. | Don't couple things that don't depend on each other. This produced decision 5. |
| Generated manifests failed on a normal cluster: `base/` contained `ServiceMonitor` and `PrometheusRule`. I only found it when Argo CD applied them to a real cluster. | A rendered manifest is not a working manifest. Test against the real thing. |
| Templates picked up Windows line endings and generated different files depending on the checkout. | Normalise input in code and pin it with `.gitattributes`. |
| Linters found swallowed errors: a failed GitHub upload reported success, fault clean-up errors were ignored, a database write after an executed action was unchecked. | Silent failure is the worst failure mode for an automation tool. Lint in CI. |
| My own test script ran a stale image (forgot `--build`) and then probed `localhost` ports that Docker did not publish. | Test through the network the services actually use, and make scripts rebuild what they test. |

## What I would do next

- **Authentication on the web pages.** The incident, audit and portal pages are read-only but have no login.
- **High availability.** The webhook is one replica on SQLite. Postgres and several replicas would be next.
- **Real integrations.** Slack buttons and GitHub pull requests are tested with signed requests and a fake
  GitHub API, not against the real services.
- **More signals in the RCA.** CPU and memory need cAdvisor or kube-state-metrics, which the demo cluster
  does not expose. That would also unlock OpenCost.
- **Network faults.** A latency experiment needs `tc` and the NET_ADMIN capability in the target container.
- **Prompt-injection hardening.** Today the safety comes from "rules decide, people approve". I would also
  strip or quote untrusted log text in the prompt.
