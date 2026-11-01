package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

type TimelineEntry struct {
	At   time.Time
	Text string
}

// Finding is a structured fact read from pod status. Unlike the LLM's prose, findings are what the
// remediation rules act on.
type Finding struct {
	Kind      string // "oom-killed" or "crash-loop"
	Pod       string
	Container string
}

const (
	FindingOOM        = "oom-killed"
	FindingCrashLoop  = "crash-loop"
	crashLoopRestarts = 3 // restarts at which a pod counts as crash looping even if it is momentarily Running
)

// podFindings inspects container statuses for OOM kills and crash loops.
func podFindings(p corev1.Pod) []Finding {
	var out []Finding
	for _, c := range p.Status.ContainerStatuses {
		oom := (c.LastTerminationState.Terminated != nil && c.LastTerminationState.Terminated.Reason == "OOMKilled") ||
			(c.State.Terminated != nil && c.State.Terminated.Reason == "OOMKilled")
		if oom {
			out = append(out, Finding{FindingOOM, p.Name, c.Name})
		}
		looping := (c.State.Waiting != nil && c.State.Waiting.Reason == "CrashLoopBackOff") || c.RestartCount >= crashLoopRestarts
		if looping && !oom {
			out = append(out, Finding{FindingCrashLoop, p.Name, c.Name})
		}
	}
	return out
}

// Evidence is everything we could learn about an incident, rendered as text for the LLM.
type Evidence struct {
	Findings    []Finding // structured facts, used by the remediation rules
	Subject     string
	Namespace   string
	Pods        []string // pods named by the alerts
	Alerts      []string // alert names in the group
	Events      []string
	Siblings    []string
	Logs        string
	PromStats   string
	Context     []Hit           // runbook sections and similar past incidents
	Timeline    []TimelineEntry // alerts, Kubernetes events; rendered in time order
	Unavailable []string        // human-readable notes about data we could not collect
}

// siblingPrefix turns "demo-app-5877c4b47d-vvp5m" into "demo-app-5877c4b47d",
// the ReplicaSet prefix shared by replacement pods.
func siblingPrefix(pod string) string {
	i := strings.LastIndex(pod, "-")
	if i <= 0 {
		return pod
	}
	return pod[:i]
}

func newKubeClient(cfg Config) (kubernetes.Interface, error) {
	rc, err := rest.InClusterConfig()
	if err != nil {
		kc := os.Getenv("KUBECONFIG")
		if kc == "" {
			return nil, fmt.Errorf("no in-cluster config and KUBECONFIG not set")
		}
		rc, err = clientcmd.BuildConfigFromFlags("", kc)
		if err != nil {
			return nil, err
		}
		if cfg.KubeAPIHost != "" {
			rc.Host = cfg.KubeAPIHost
		}
	}
	rc.Timeout = 10 * time.Second
	return kubernetes.NewForConfig(rc)
}

func eventTime(e corev1.Event) time.Time {
	switch {
	case !e.LastTimestamp.IsZero():
		return e.LastTimestamp.Time
	case !e.EventTime.IsZero():
		return e.EventTime.Time
	}
	return e.CreationTimestamp.Time
}

// renderTimeline lists entries oldest first, as "15:04:05 text" lines.
func renderTimeline(entries []TimelineEntry) []string {
	sorted := append([]TimelineEntry{}, entries...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].At.Before(sorted[j].At) })
	out := make([]string, 0, len(sorted))
	for _, e := range sorted {
		out = append(out, e.At.UTC().Format("15:04:05")+" "+e.Text)
	}
	return out
}

// collectKubeEvidence gathers events for the alerting pods (up to maxPods), plus status and recent
// logs of the pods in their ReplicaSets, which stand in for the alerting pod if it was deleted.
func collectKubeEvidence(ctx context.Context, cs kubernetes.Interface, ev *Evidence, tail int64, maxPods int) {
	pods := ev.Pods
	if maxPods > 0 && len(pods) > maxPods {
		pods = pods[:maxPods]
	}
	for _, name := range pods {
		events, err := cs.CoreV1().Events(ev.Namespace).List(ctx, metav1.ListOptions{FieldSelector: "involvedObject.name=" + name})
		if err != nil {
			ev.Unavailable = append(ev.Unavailable, "events: "+err.Error())
			continue
		}
		for _, e := range events.Items {
			line := fmt.Sprintf("%s %s: %s", e.Type, e.Reason, e.Message)
			if len(pods) > 1 {
				line = name + " - " + line
			}
			ev.Events = append(ev.Events, line)
			ev.Timeline = append(ev.Timeline, TimelineEntry{eventTime(e), fmt.Sprintf("%s %s (%s)", name, e.Reason, trim(e.Message, 80))})
		}
	}

	all, err := cs.CoreV1().Pods(ev.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		ev.Unavailable = append(ev.Unavailable, "pod list: "+err.Error())
		return
	}
	prefixes := map[string]bool{}
	var logParts []string
	for _, name := range pods {
		prefix := siblingPrefix(name)
		if prefixes[prefix] {
			continue
		}
		prefixes[prefix] = true
		found, logged := false, false
		for _, p := range all.Items {
			if !strings.HasPrefix(p.Name, prefix) {
				continue
			}
			found = true
			restarts := int32(0)
			for _, c := range p.Status.ContainerStatuses {
				restarts += c.RestartCount
			}
			label := "sibling"
			if p.Name == name {
				label = "the alerting pod (still exists)"
			}
			ev.Siblings = append(ev.Siblings, fmt.Sprintf("%s [%s]: phase=%s restarts=%d", p.Name, label, p.Status.Phase, restarts))
			for _, f := range podFindings(p) {
				ev.Findings = append(ev.Findings, f)
				ev.Siblings[len(ev.Siblings)-1] += " (" + f.Kind + ")"
			}

			// Logs of the alerting pod, or of one replacement as a stand-in.
			if !logged && (p.Name == name || len(logParts) < 2) {
				if l := podLogs(ctx, cs, p, tail); l != "" {
					logParts = append(logParts, fmt.Sprintf("--- logs of %s ---\n%s", p.Name, l))
					logged = true
				}
			}
		}
		if !found {
			ev.Unavailable = append(ev.Unavailable, "no pods with prefix "+prefix+" found")
		}
	}
	ev.Logs = strings.Join(logParts, "\n")
	if ev.Logs == "" {
		ev.Unavailable = append(ev.Unavailable, "pod logs (the pod was deleted, so its logs are gone)")
	}
}

func podLogs(ctx context.Context, cs kubernetes.Interface, p corev1.Pod, tail int64) string {
	// Previous container logs first (crash loops), then current.
	for _, previous := range []bool{true, false} {
		raw, err := cs.CoreV1().Pods(p.Namespace).GetLogs(p.Name, &corev1.PodLogOptions{
			TailLines: &tail,
			Previous:  previous,
		}).DoRaw(ctx)
		if err == nil && len(raw) > 0 {
			return strings.TrimSpace(string(raw))
		}
	}
	return ""
}
