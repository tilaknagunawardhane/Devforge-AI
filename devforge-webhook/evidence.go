package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// Evidence is everything we could learn about an incident, rendered as text for the LLM.
type Evidence struct {
	Pod         string
	Namespace   string
	Events      []string
	Siblings    []string
	Logs        string
	PromStats   string
	Unavailable []string // human-readable notes about data we could not collect
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

// collectKubeEvidence gathers events for the pod, plus status and recent logs of the
// pod itself (if it still exists) and of its replacement pods.
func collectKubeEvidence(ctx context.Context, cs kubernetes.Interface, ev *Evidence, tail int64) {
	events, err := cs.CoreV1().Events(ev.Namespace).List(ctx, metav1.ListOptions{
		FieldSelector: "involvedObject.name=" + ev.Pod,
	})
	if err != nil {
		ev.Unavailable = append(ev.Unavailable, "events: "+err.Error())
	} else {
		for _, e := range events.Items {
			ev.Events = append(ev.Events, fmt.Sprintf("%s %s: %s", e.Type, e.Reason, e.Message))
		}
	}

	pods, err := cs.CoreV1().Pods(ev.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		ev.Unavailable = append(ev.Unavailable, "pod list: "+err.Error())
		return
	}
	prefix := siblingPrefix(ev.Pod)
	var logParts []string
	found := false
	for _, p := range pods.Items {
		if !strings.HasPrefix(p.Name, prefix) {
			continue
		}
		found = true
		restarts := int32(0)
		for _, c := range p.Status.ContainerStatuses {
			restarts += c.RestartCount
		}
		label := "sibling"
		if p.Name == ev.Pod {
			label = "the alerting pod (still exists)"
		}
		ev.Siblings = append(ev.Siblings, fmt.Sprintf("%s [%s]: phase=%s restarts=%d", p.Name, label, p.Status.Phase, restarts))

		// Fetch logs for the alerting pod, or for one replacement as a stand-in.
		if p.Name == ev.Pod || len(logParts) == 0 {
			if l := podLogs(ctx, cs, p, tail); l != "" {
				logParts = append(logParts, fmt.Sprintf("--- logs of %s ---\n%s", p.Name, l))
			}
		}
	}
	if !found {
		ev.Unavailable = append(ev.Unavailable, "no pods with prefix "+prefix+" found")
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
