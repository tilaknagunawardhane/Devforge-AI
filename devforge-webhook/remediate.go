package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

// Auto-remediation design: rules PROPOSE actions from structured facts (pod status, rollout history),
// never from LLM text. A proposal runs only after a named human approves it, and only inside the
// configured namespaces. Every step is written to the append-only audit log.

var (
	ErrBlocked  = errors.New("blocked")
	ErrConflict = errors.New("conflict")
	ErrNotFound = errors.New("not found")
)

const (
	revisionAnnotation = "deployment.kubernetes.io/revision"
	optOutLabel        = "devforge.io/no-remediation"
)

type Proposal struct {
	Kind, Target, Rule, Reason, Params string
}

// ---------- Kubernetes helpers -------------------------------------------------------------

func revisionOf(o metav1.Object) int64 {
	n, _ := strconv.ParseInt(o.GetAnnotations()[revisionAnnotation], 10, 64)
	return n
}

// revisions returns the ReplicaSet of the Deployment's current revision and the newest older one.
func revisions(ctx context.Context, cs kubernetes.Interface, d *appsv1.Deployment) (cur, prev *appsv1.ReplicaSet, err error) {
	list, err := cs.AppsV1().ReplicaSets(d.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, nil, err
	}
	curRev := revisionOf(d)
	for i := range list.Items {
		rs := &list.Items[i]
		if !metav1.IsControlledBy(rs, d) {
			continue
		}
		switch r := revisionOf(rs); {
		case r == curRev:
			cur = rs
		case r < curRev && (prev == nil || r > revisionOf(prev)):
			prev = rs
		}
	}
	return cur, prev, nil
}

// rollbackDeployment does what `kubectl rollout undo` does: it puts the previous ReplicaSet's pod
// template back into the Deployment, which starts a new rollout of the old version.
func rollbackDeployment(ctx context.Context, cs kubernetes.Interface, ns, name string) (from, to int64, err error) {
	d, err := cs.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return 0, 0, err
	}
	_, prev, err := revisions(ctx, cs, d)
	if err != nil {
		return 0, 0, err
	}
	if prev == nil {
		return 0, 0, fmt.Errorf("deployment %q has no previous revision to roll back to", name)
	}
	tmpl := prev.Spec.Template.DeepCopy()
	delete(tmpl.Labels, "pod-template-hash") // the Deployment controller re-adds it
	d.Spec.Template = *tmpl
	if _, err := cs.AppsV1().Deployments(ns).Update(ctx, d, metav1.UpdateOptions{}); err != nil {
		return 0, 0, err
	}
	return revisionOf(d), revisionOf(prev), nil
}

// restartDeploymentNamed does what `kubectl rollout restart` does.
func restartDeploymentNamed(ctx context.Context, cs kubernetes.Interface, ns, name string) error {
	deployments := cs.AppsV1().Deployments(ns)
	if _, err := deployments.Get(ctx, name, metav1.GetOptions{}); err != nil {
		return fmt.Errorf("deployment %q not found in %s: %w", name, ns, err)
	}
	patch := fmt.Sprintf(`{"spec":{"template":{"metadata":{"annotations":{"kubectl.kubernetes.io/restartedAt":%q}}}}}`,
		time.Now().UTC().Format(time.RFC3339))
	_, err := deployments.Patch(ctx, name, types.StrategicMergePatchType, []byte(patch), metav1.PatchOptions{})
	return err
}

// ---------- rules --------------------------------------------------------------------------

// proposeActions turns structured findings into proposals. It never looks at the LLM's text, and it
// stays quiet for chaos alerts, which are deliberate faults that Kubernetes heals on its own.
func (a *App) proposeActions(ctx context.Context, cs kubernetes.Interface, inc Incident, ev Evidence) []Proposal {
	for _, n := range ev.Alerts {
		if strings.HasPrefix(n, "Chaos") {
			return nil
		}
	}
	pods := inc.PodList()
	if len(pods) == 0 {
		return nil
	}
	name := deploymentName(pods[0])
	d, err := cs.AppsV1().Deployments(inc.Namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil
	}

	var out []Proposal
	var oom, loop []Finding
	for _, f := range ev.Findings {
		if f.Kind == FindingOOM {
			oom = append(oom, f)
		} else if f.Kind == FindingCrashLoop {
			loop = append(loop, f)
		}
	}

	if len(oom) > 0 && a.cfg.GitHubToken != "" && a.cfg.GitHubRepoTemplate != "" {
		out = append(out, Proposal{
			Kind: KindOpenPR, Target: name, Rule: "oom-raise-memory-limit-pr", Params: `{"factor":2}`,
			Reason: fmt.Sprintf("container %s in %s was OOMKilled: propose doubling its memory limit in the manifest repo (up to %dMi)",
				oom[0].Container, oom[0].Pod, a.cfg.MaxMemoryMi),
		})
	}

	if len(loop) > 0 {
		cur, prev, err := revisions(ctx, cs, d)
		switch {
		case err != nil || cur == nil || prev == nil:
			// nothing older to go back to
		case time.Since(cur.CreationTimestamp.Time) > a.cfg.RecentRolloutWindow:
			// the crash loop did not start with a recent rollout, so rolling back is not obviously the fix
		default:
			out = append(out, Proposal{
				Kind: KindRollback, Target: name, Rule: "crashloop-after-rollout-rollback",
				Reason: fmt.Sprintf("%s is crash looping and revision %d was rolled out %s ago: roll back to revision %d",
					loop[0].Pod, revisionOf(cur), time.Since(cur.CreationTimestamp.Time).Round(time.Second), revisionOf(prev)),
			})
		}
	}
	return out
}

// createProposals stores proposals as actions and audits them.
func (a *App) createProposals(inc Incident, props []Proposal) []Action {
	var out []Action
	for _, p := range props {
		act, err := a.store.CreateAction(Action{IncidentID: inc.ID, Kind: p.Kind, Namespace: inc.Namespace,
			Target: p.Target, Params: p.Params, Rule: p.Rule, Reason: p.Reason})
		if err != nil {
			slog.Error("could not store proposal", "error", err.Error())
			continue
		}
		a.audit(ActorSystem, auditProposed, inc.ID, act.ID, fmt.Sprintf("%s %s/%s by rule %s: %s", act.Kind, act.Namespace, act.Target, act.Rule, act.Reason))
		out = append(out, *act)
	}
	return out
}

// ---------- audit -----------------------------------------------------------------------------

// audit writes to the database and to the structured log, so the trail exists in both places.
func (a *App) audit(actor, event string, incidentID, actionID int64, detail string) {
	slog.Info("audit", "actor", actor, "event", event, "incident", incidentID, "action", actionID, "detail", detail)
	if err := a.store.Audit(actor, event, incidentID, actionID, detail); err != nil {
		slog.Error("AUDIT WRITE FAILED", "error", err.Error(), "event", event, "action", actionID)
	}
}

// ---------- policy ------------------------------------------------------------------------------

func isSystemNamespace(ns string) bool {
	return strings.HasPrefix(ns, "kube-") || ns == "kube-system"
}

// check returns why an action must not run right now, or nil.
func (a *App) check(ctx context.Context, cs kubernetes.Interface, act *Action, actor string) error {
	if isSystemNamespace(act.Namespace) || !slices.Contains(a.cfg.RemediationNamespaces, act.Namespace) {
		return fmt.Errorf("%w: namespace %q is not on the remediation allow-list (REMEDIATION_NAMESPACES)", ErrBlocked, act.Namespace)
	}
	if len(a.cfg.Approvers) > 0 && !slices.Contains(a.cfg.Approvers, actor) {
		return fmt.Errorf("%w: %q is not an authorised approver", ErrBlocked, actor)
	}
	if act.Kind == KindOpenPR {
		return nil // opening a PR changes nothing in the cluster; a reviewer still has to merge it
	}
	d, err := cs.AppsV1().Deployments(act.Namespace).Get(ctx, act.Target, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("%w: deployment %q not found in %s", ErrBlocked, act.Target, act.Namespace)
	}
	if d.Labels[optOutLabel] == "true" {
		return fmt.Errorf("%w: deployment %q has opted out of remediation (%s=true)", ErrBlocked, act.Target, optOutLabel)
	}
	last, err := a.store.LastExecuted(act.Namespace, act.Target)
	if err == nil && !last.IsZero() && time.Since(last) < a.cfg.RemediationCooldown {
		return fmt.Errorf("%w: cooldown, %s was changed %s ago (wait %s between actions)", ErrBlocked, act.Target,
			time.Since(last).Round(time.Second), a.cfg.RemediationCooldown)
	}
	return nil
}

// ---------- approval flow --------------------------------------------------------------------

// decide is the single entry point for approving or rejecting an action, used by Slack and the API.
func (a *App) decide(ctx context.Context, id int64, actor string, approve bool) (string, error) {
	act, err := a.store.GetAction(id)
	if err != nil {
		return "", fmt.Errorf("%w: action #%d", ErrNotFound, id)
	}
	if act.Status != ActionProposed {
		a.audit(actor, auditBlocked, act.IncidentID, id, "already "+act.Status)
		return "", fmt.Errorf("%w: action #%d is already %s", ErrConflict, id, act.Status)
	}

	if !approve {
		if ok, _ := a.store.Claim(id, ActionRejected, actor); !ok {
			return "", fmt.Errorf("%w: action #%d was just decided by someone else", ErrConflict, id)
		}
		a.audit(actor, auditRejected, act.IncidentID, id, describe(act))
		return fmt.Sprintf("Action #%d rejected by %s.", id, actor), nil
	}

	if time.Since(act.CreatedAt) > a.cfg.ProposalTTL {
		if ok, _ := a.store.Claim(id, ActionExpired, ActorSystem); ok {
			a.audit(actor, auditExpired, act.IncidentID, id, "approval attempted after the proposal expired")
		}
		return "", fmt.Errorf("%w: action #%d expired; the situation may have changed, check the incident and propose again", ErrConflict, id)
	}

	var cs kubernetes.Interface // opening a PR does not touch the cluster, so it needs no client
	if act.Kind != KindOpenPR {
		if cs, err = a.kube(); err != nil {
			a.audit(actor, auditBlocked, act.IncidentID, id, "cannot reach Kubernetes: "+err.Error())
			return "", fmt.Errorf("%w: cannot reach Kubernetes: %v", ErrBlocked, err)
		}
	}
	if err := a.check(ctx, cs, act, actor); err != nil {
		a.audit(actor, auditBlocked, act.IncidentID, id, err.Error())
		return "", err
	}

	if ok, err := a.store.Claim(id, ActionApproved, actor); err != nil || !ok {
		return "", fmt.Errorf("%w: action #%d was just decided by someone else", ErrConflict, id)
	}
	a.audit(actor, auditApproved, act.IncidentID, id, describe(act))

	if a.cfg.RemediationDryRun {
		msg := "dry-run: would " + describe(act)
		a.store.FinishAction(id, ActionExecuted, msg)
		a.audit(actor, auditDryRun, act.IncidentID, id, msg)
		return fmt.Sprintf("Action #%d approved by %s (%s).", id, actor, msg), nil
	}

	result, err := a.execute(ctx, cs, act)
	if err != nil {
		a.store.FinishAction(id, ActionFailed, err.Error())
		a.audit(actor, auditFailed, act.IncidentID, id, err.Error())
		return "", fmt.Errorf("action #%d failed: %w", id, err)
	}
	a.store.FinishAction(id, ActionExecuted, result)
	a.audit(actor, auditExecuted, act.IncidentID, id, result)
	if act.Kind == KindRestart || act.Kind == KindRollback {
		go a.verifyRollout(act.IncidentID, id, act.Namespace, act.Target)
	}
	return fmt.Sprintf("Action #%d approved by %s: %s.", id, actor, result), nil
}

func describe(act *Action) string {
	return fmt.Sprintf("%s %s/%s", act.Kind, act.Namespace, act.Target)
}

func (a *App) execute(ctx context.Context, cs kubernetes.Interface, act *Action) (string, error) {
	switch act.Kind {
	case KindRestart:
		if err := restartDeploymentNamed(ctx, cs, act.Namespace, act.Target); err != nil {
			return "", err
		}
		return fmt.Sprintf("restarted deployment `%s`", act.Target), nil
	case KindRollback:
		from, to, err := rollbackDeployment(ctx, cs, act.Namespace, act.Target)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("rolled deployment `%s` back from revision %d to %d", act.Target, from, to), nil
	case KindOpenPR:
		url, err := a.openMemoryPR(ctx, act)
		if err != nil {
			return "", err
		}
		return "opened pull request " + url, nil
	}
	return "", fmt.Errorf("unknown action kind %q", act.Kind)
}

// verifyRollout watches the Deployment after a restart or rollback and records whether it became healthy.
func (a *App) verifyRollout(incidentID, actionID int64, ns, name string) {
	cs, err := a.kube()
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), a.verifyFor)
	defer cancel()
	start := time.Now()
	for {
		d, err := cs.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{})
		if err == nil {
			want := int32(1)
			if d.Spec.Replicas != nil {
				want = *d.Spec.Replicas
			}
			if d.Status.ObservedGeneration >= d.Generation && d.Status.UpdatedReplicas == want &&
				d.Status.ReadyReplicas == want && d.Status.Replicas == want {
				a.audit(ActorSystem, auditVerified, incidentID, actionID,
					fmt.Sprintf("rollout of %s complete in %s", name, time.Since(start).Round(time.Second)))
				return
			}
		}
		select {
		case <-ctx.Done():
			a.audit(ActorSystem, auditVerifyFail, incidentID, actionID,
				fmt.Sprintf("%s did not become fully ready within %s; check it manually", name, a.verifyFor))
			return
		case <-time.After(a.verifyEvery):
		}
	}
}

// manualAction records and decides a human-initiated action (a Slack button) in one step, so even
// "just restart it" goes through the same policy checks and audit trail.
func (a *App) manualAction(ctx context.Context, inc *Incident, kind, actor string) (string, error) {
	pods := inc.PodList()
	if len(pods) == 0 {
		return "", fmt.Errorf("%w: this incident names no pods, so there is no workload to act on", ErrBlocked)
	}
	act, err := a.store.CreateAction(Action{IncidentID: inc.ID, Kind: kind, Namespace: inc.Namespace,
		Target: deploymentName(pods[0]), Rule: "manual-slack-" + kind, Reason: "requested by " + actor + " from Slack"})
	if err != nil {
		return "", err
	}
	a.audit(actor, auditProposed, inc.ID, act.ID, describe(act)+" (manual)")
	return a.decide(ctx, act.ID, actor, true)
}
