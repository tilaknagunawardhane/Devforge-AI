package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"k8s.io/client-go/kubernetes"
)

// Alert and AlertPayload follow the Alertmanager webhook format (version 4).
type Alert struct {
	Status      string            `json:"status"`
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
	StartsAt    time.Time         `json:"startsAt"`
}

type AlertPayload struct {
	Status   string  `json:"status"`
	GroupKey string  `json:"groupKey"`
	Alerts   []Alert `json:"alerts"`
}

// App holds the long-lived dependencies of the webhook.
type App struct {
	cfg   Config
	store *Store
	rag   *RAG // nil when Qdrant is not configured
	kube  func() (kubernetes.Interface, error)
}

// groupKey identifies "the same incident". Alertmanager supplies one per notification group; if a
// caller (e.g. `make demo`) does not, derive it from what the alerts are about.
func groupKey(p AlertPayload) string {
	if p.GroupKey != "" {
		return p.GroupKey
	}
	names, pods, ns, svc := summarize(p.Alerts)
	subject := svc
	if len(pods) > 0 {
		subject = siblingPrefix(pods[0])
	}
	return strings.Join(names, ",") + "|" + ns + "|" + subject
}

// summarize pulls the alert names, pods, namespace and service out of a set of alerts.
func summarize(alerts []Alert) (names, pods []string, ns, svc string) {
	ns = "default"
	for _, a := range alerts {
		names = append(names, a.Labels["alertname"])
		pods = append(pods, a.Labels["pod"])
		if v := a.Labels["namespace"]; v != "" {
			ns = v
		}
		if v := a.Labels["service"]; v != "" && svc == "" {
			svc = v
		}
	}
	return mergeUnique(names, nil), mergeUnique(pods, nil), ns, svc
}

func subjectFor(names, pods []string, svc string) string {
	switch {
	case len(pods) == 1:
		return pods[0]
	case len(pods) > 1:
		return fmt.Sprintf("%s (%d pods)", siblingPrefix(pods[0]), len(pods))
	case svc != "":
		return svc
	case len(names) > 0 && names[0] != "":
		return names[0]
	}
	return "unknown"
}

// handleGroup turns one Alertmanager notification (a group of alerts) into at most one RCA.
// It returns what it did: "created", "deduplicated", "resolved" or "ignored".
func (a *App) handleGroup(p AlertPayload) string {
	key := groupKey(p)
	if p.Status == "resolved" {
		if err := a.store.ResolveByKey(key); err != nil {
			slog.Error("resolve failed", "error", err.Error())
		}
		return "resolved"
	}
	var firing []Alert
	for _, al := range p.Alerts {
		if al.Status != "resolved" {
			firing = append(firing, al)
		}
	}
	if len(firing) == 0 {
		return "ignored"
	}
	names, pods, ns, svc := summarize(firing)

	if existing, err := a.store.FindRecent(key, a.cfg.DedupWindow); err != nil {
		slog.Error("incident lookup failed", "error", err.Error())
	} else if existing != nil {
		if err := a.store.AddAlerts(existing.ID, pods); err != nil {
			slog.Error("could not update incident", "error", err.Error())
		}
		slog.Info("alerts folded into open incident", "incident", existing.ID, "alerts", len(firing), "key", key)
		return "deduplicated"
	}

	inc, err := a.store.Create(Incident{
		Key: key, Subject: subjectFor(names, pods, svc), Namespace: ns,
		Alertnames: strings.Join(names, ","), Pods: strings.Join(pods, ","), AlertCount: len(firing),
	})
	if err != nil {
		slog.Error("could not create incident", "error", err.Error())
		return "ignored"
	}
	slog.Info("incident opened", "incident", inc.ID, "subject", inc.Subject, "alerts", len(firing))
	// Analyze in the background so Alertmanager gets its 200 immediately.
	go a.analyze(context.Background(), *inc, firing)
	return "created"
}

// analyze collects evidence, asks the LLM, stores the RCA and posts it.
func (a *App) analyze(ctx context.Context, inc Incident, alerts []Alert) {
	names, _, _, _ := summarize(alerts)
	ev := Evidence{Subject: inc.Subject, Namespace: inc.Namespace, Pods: inc.PodList(), Alerts: names}
	for _, al := range alerts {
		if !al.StartsAt.IsZero() && al.StartsAt.Year() > 2000 {
			what := "alert " + al.Labels["alertname"] + " fired"
			if p := al.Labels["pod"]; p != "" {
				what += " for " + p
			}
			ev.Timeline = append(ev.Timeline, TimelineEntry{al.StartsAt, what})
		}
	}

	if cs, err := a.kube(); err != nil {
		ev.Unavailable = append(ev.Unavailable, "Kubernetes API: "+err.Error())
	} else {
		collectKubeEvidence(ctx, cs, &ev, a.cfg.LogTailLines, a.cfg.MaxEvidencePods)
	}
	collectPromEvidence(ctx, a.cfg, &ev)

	if a.rag != nil {
		query := fmt.Sprintf("%s %s %s", inc.Subject, strings.Join(names, " "), trim(strings.Join(ev.Events, " "), 400))
		if hits, err := a.rag.Search(ctx, query); err != nil {
			ev.Unavailable = append(ev.Unavailable, "runbook search: "+err.Error())
		} else {
			ev.Context = hits
		}
	}
	slog.Info("evidence collected", "incident", inc.ID, "events", len(ev.Events), "siblings", len(ev.Siblings),
		"has_logs", ev.Logs != "", "context_hits", len(ev.Context))

	report, err := askOllama(ctx, a.cfg, buildPrompt(ev))
	if err != nil {
		slog.Error("RCA generation failed, sending fallback", "incident", inc.ID, "error", err.Error())
		report = fallbackReport(ev, err)
	}
	if err := a.store.SetRCA(inc.ID, report); err != nil {
		slog.Error("could not store RCA", "incident", inc.ID, "error", err.Error())
	}
	latest, err := a.store.Get(inc.ID)
	if err != nil {
		latest = &inc
	}
	a.postSlack(*latest, report)
	// The RCA is only added to the retrieval index once a human acknowledges it (see slackActions),
	// so a poor model answer never becomes "knowledge" that later incidents are told to trust.
}
