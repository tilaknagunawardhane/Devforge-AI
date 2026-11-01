package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
)

const sampleManifest = `spec:
  template:
    spec:
      containers:
        - name: web
          image: web:latest
          resources:
            requests:
              cpu: 50m
              memory: 64Mi
            limits:
              cpu: 250m
              memory: 128Mi
`

// ---------- fixtures -----------------------------------------------------------------------

func rsFor(d *appsv1.Deployment, name string, rev string, image string, age time.Duration) *appsv1.ReplicaSet {
	yes := true
	return &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: d.Namespace, Annotations: map[string]string{revisionAnnotation: rev},
			CreationTimestamp: metav1.NewTime(time.Now().Add(-age)),
			OwnerReferences:   []metav1.OwnerReference{{UID: d.UID, Controller: &yes, Name: d.Name, Kind: "Deployment", APIVersion: "apps/v1"}},
		},
		Spec: appsv1.ReplicaSetSpec{Template: corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": d.Name, "pod-template-hash": name}},
			Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: d.Name, Image: image}}},
		}},
	}
}

// cluster returns a fake clientset holding Deployment shop/web at revision 2 (image v2, rolled out
// `age` ago) with revision 1 (image v1) available to roll back to.
func cluster(age time.Duration, withPrevious bool, labels map[string]string) *fake.Clientset {
	d := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "shop", UID: types.UID("d1"),
		Annotations: map[string]string{revisionAnnotation: "2"}, Labels: labels}}
	d.Spec.Template = rsFor(d, "web-v2", "2", "web:v2", 0).Spec.Template
	objs := []any{d, rsFor(d, "web-v2", "2", "web:v2", age)}
	if withPrevious {
		objs = append(objs, rsFor(d, "web-v1", "1", "web:v1", 2*time.Hour))
	}
	cs := fake.NewSimpleClientset()
	for _, o := range objs {
		switch v := o.(type) {
		case *appsv1.Deployment:
			cs.Tracker().Add(v)
		case *appsv1.ReplicaSet:
			cs.Tracker().Add(v)
		}
	}
	return cs
}

func remediationApp(t *testing.T, cs kubernetes.Interface, cfg Config) *App {
	t.Helper()
	if cfg.RemediationNamespaces == nil {
		cfg.RemediationNamespaces = []string{"shop"}
	}
	app := newTestApp(t, cfg)
	app.kube = func() (kubernetes.Interface, error) { return cs, nil }
	return app
}

func newIncident(t *testing.T, app *App) *Incident {
	t.Helper()
	inc, err := app.store.Create(Incident{Key: "k", Subject: "web", Namespace: "shop", Pods: "web-v2-aaaaa", AlertCount: 1, Alertnames: "HighErrorRate"})
	if err != nil {
		t.Fatal(err)
	}
	return inc
}

func proposeFor(t *testing.T, app *App, inc *Incident, kind string) *Action {
	t.Helper()
	acts := app.createProposals(*inc, []Proposal{{Kind: kind, Target: "web", Rule: "test", Reason: "because"}})
	if len(acts) != 1 {
		t.Fatal("proposal not stored")
	}
	return &acts[0]
}

func auditEvents(t *testing.T, app *App) []string {
	t.Helper()
	entries, err := app.store.ListAudit(100, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for i := len(entries) - 1; i >= 0; i-- { // oldest first
		out = append(out, entries[i].Event)
	}
	return out
}

// waitAudit waits for the background rollout check to write its entry, so tests end cleanly.
func waitAudit(t *testing.T, app *App, event string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, e := range auditEvents(t, app) {
			if e == event {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("audit event %q never appeared; have %v", event, auditEvents(t, app))
}

func hasEvent(events []string, want string) bool {
	for _, e := range events {
		if e == want {
			return true
		}
	}
	return false
}

// ---------- rules ---------------------------------------------------------------------------

func TestRulesProposeRollbackAfterRecentRollout(t *testing.T) {
	app := remediationApp(t, nil, Config{RecentRolloutWindow: time.Hour})
	inc := Incident{ID: 1, Namespace: "shop", Pods: "web-v2-aaaaa"}
	crash := Evidence{Alerts: []string{"HighErrorRate"}, Findings: []Finding{{FindingCrashLoop, "web-v2-aaaaa", "web"}}}

	props := app.proposeActions(context.Background(), cluster(5*time.Minute, true, nil), inc, crash)
	if len(props) != 1 || props[0].Kind != KindRollback || props[0].Target != "web" || props[0].Rule != "crashloop-after-rollout-rollback" {
		t.Fatalf("want one rollback proposal, got %+v", props)
	}
	if !strings.Contains(props[0].Reason, "revision 2") || !strings.Contains(props[0].Reason, "revision 1") {
		t.Errorf("reason should name both revisions: %q", props[0].Reason)
	}

	for name, cs := range map[string]kubernetes.Interface{
		"rollout too old":           cluster(3*time.Hour, true, nil),
		"no previous revision":      cluster(5*time.Minute, false, nil),
		"unrelated deployment gone": fake.NewSimpleClientset(),
	} {
		if got := app.proposeActions(context.Background(), cs, inc, crash); len(got) != 0 {
			t.Errorf("%s: expected no proposals, got %+v", name, got)
		}
	}
}

func TestRulesProposeMemoryPROnlyWhenConfigured(t *testing.T) {
	inc := Incident{ID: 1, Namespace: "shop", Pods: "web-v2-aaaaa"}
	oom := Evidence{Alerts: []string{"HighErrorRate"}, Findings: []Finding{{FindingOOM, "web-v2-aaaaa", "web"}}}

	configured := remediationApp(t, nil, Config{GitHubToken: "tok", GitHubRepoTemplate: "o/{deployment}", MaxMemoryMi: 1024})
	props := configured.proposeActions(context.Background(), cluster(time.Minute, true, nil), inc, oom)
	if len(props) != 1 || props[0].Kind != KindOpenPR || !strings.Contains(props[0].Reason, "OOMKilled") {
		t.Fatalf("want one open-pr proposal, got %+v", props)
	}

	plain := remediationApp(t, nil, Config{})
	if got := plain.proposeActions(context.Background(), cluster(time.Minute, true, nil), inc, oom); len(got) != 0 {
		t.Errorf("without GitHub configured nothing can be proposed, got %+v", got)
	}
}

func TestRulesStayQuietForChaosAndPodlessIncidents(t *testing.T) {
	app := remediationApp(t, nil, Config{GitHubToken: "tok", GitHubRepoTemplate: "o/{deployment}", RecentRolloutWindow: time.Hour})
	cs := cluster(time.Minute, true, nil)
	crash := []Finding{{FindingCrashLoop, "web-v2-aaaaa", "web"}}
	if got := app.proposeActions(context.Background(), cs, Incident{Namespace: "shop", Pods: "web-v2-aaaaa"},
		Evidence{Alerts: []string{"ChaosFaultInjected"}, Findings: crash}); len(got) != 0 {
		t.Errorf("chaos faults are deliberate and self-healing: %+v", got)
	}
	if got := app.proposeActions(context.Background(), cs, Incident{Namespace: "shop"}, Evidence{Findings: crash}); len(got) != 0 {
		t.Errorf("an incident with no pods names no workload: %+v", got)
	}
}

// ---------- executors ---------------------------------------------------------------------------

func TestRollbackRestoresPreviousTemplate(t *testing.T) {
	cs := cluster(time.Minute, true, nil)
	from, to, err := rollbackDeployment(context.Background(), cs, "shop", "web")
	if err != nil || from != 2 || to != 1 {
		t.Fatalf("rollback = (%d, %d, %v), want (2, 1, nil)", from, to, err)
	}
	d, _ := cs.AppsV1().Deployments("shop").Get(context.Background(), "web", metav1.GetOptions{})
	if img := d.Spec.Template.Spec.Containers[0].Image; img != "web:v1" {
		t.Errorf("image after rollback = %q, want web:v1", img)
	}
	if _, has := d.Spec.Template.Labels["pod-template-hash"]; has {
		t.Error("pod-template-hash must be removed so the controller can compute it")
	}

	if _, _, err := rollbackDeployment(context.Background(), cluster(time.Minute, false, nil), "shop", "web"); err == nil {
		t.Error("rolling back with no previous revision must fail")
	}
}

func TestRaiseMemoryLimit(t *testing.T) {
	out, old, nw, err := raiseMemoryLimit(sampleManifest, 2, 1024)
	if err != nil || old != 128 || nw != 256 {
		t.Fatalf("got (%d -> %d, %v)", old, nw, err)
	}
	if !strings.Contains(out, "memory: 256Mi") || !strings.Contains(out, "memory: 64Mi") {
		t.Errorf("only the limit may change (requests stay 64Mi):\n%s", out)
	}
	if out2, _, n2, _ := raiseMemoryLimit(strings.Replace(sampleManifest, "128Mi", "512Mi", 1), 2, 1024); n2 != 1024 || !strings.Contains(out2, "memory: 1Gi") {
		t.Errorf("1024Mi should be written as 1Gi: %d\n%s", n2, out2)
	}
	if _, _, _, err := raiseMemoryLimit(strings.Replace(sampleManifest, "128Mi", "1Gi", 1), 2, 1024); err == nil {
		t.Error("a limit already at the maximum must not be raised")
	}
	if _, _, _, err := raiseMemoryLimit("kind: Deployment\n", 2, 1024); err == nil {
		t.Error("a manifest without a memory limit must be rejected")
	}
}

type fakeGitHub struct {
	mu      sync.Mutex
	calls   []string
	putBody map[string]string
	prBody  map[string]string
	auth    string
}

func newFakeGitHub(t *testing.T) (*httptest.Server, *fakeGitHub) {
	f := &fakeGitHub{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls = append(f.calls, r.Method+" "+r.URL.Path)
		f.auth = r.Header.Get("Authorization")
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		switch {
		case r.Method == "GET" && r.URL.Path == "/repos/o/web":
			w.Write([]byte(`{"default_branch":"main"}`))
		case r.Method == "GET" && r.URL.Path == "/repos/o/web/contents/base/deployment.yaml":
			json.NewEncoder(w).Encode(map[string]string{"content": base64.StdEncoding.EncodeToString([]byte(sampleManifest)), "sha": "filesha"})
		case r.Method == "GET" && r.URL.Path == "/repos/o/web/git/ref/heads/main":
			w.Write([]byte(`{"object":{"sha":"basesha"}}`))
		case r.Method == "POST" && r.URL.Path == "/repos/o/web/git/refs":
			w.WriteHeader(201)
			w.Write([]byte(`{}`))
		case r.Method == "PUT" && r.URL.Path == "/repos/o/web/contents/base/deployment.yaml":
			f.putBody = body
			w.Write([]byte(`{}`))
		case r.Method == "POST" && r.URL.Path == "/repos/o/web/pulls":
			f.prBody = body
			w.WriteHeader(201)
			w.Write([]byte(`{"html_url":"https://github.com/o/web/pull/7"}`))
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, 500)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, f
}

func TestOpenMemoryPR(t *testing.T) {
	srv, gh := newFakeGitHub(t)
	app := remediationApp(t, nil, Config{GitHubToken: "tok", GitHubAPI: srv.URL, GitHubRepoTemplate: "o/{deployment}",
		GitHubManifestPath: "base/deployment.yaml", MaxMemoryMi: 1024})
	inc := newIncident(t, app)
	app.store.SetRCA(inc.ID, "the container ran out of memory")
	act, _ := app.store.CreateAction(Action{IncidentID: inc.ID, Kind: KindOpenPR, Namespace: "shop", Target: "web",
		Params: `{"factor":2}`, Rule: "oom-raise-memory-limit-pr", Reason: "OOMKilled"})
	act.DecidedBy = "alice"

	url, err := app.openMemoryPR(context.Background(), act)
	if err != nil || url != "https://github.com/o/web/pull/7" {
		t.Fatalf("got (%q, %v)", url, err)
	}
	if gh.auth != "Bearer tok" {
		t.Errorf("auth header = %q", gh.auth)
	}
	committed, _ := base64.StdEncoding.DecodeString(gh.putBody["content"])
	if !strings.Contains(string(committed), "memory: 256Mi") || gh.putBody["sha"] != "filesha" || !strings.HasPrefix(gh.putBody["branch"], "devforge/oom-web-") {
		t.Errorf("commit wrong: branch=%q sha=%q\n%s", gh.putBody["branch"], gh.putBody["sha"], committed)
	}
	pr := gh.prBody
	if pr["base"] != "main" || pr["head"] != gh.putBody["branch"] || !strings.Contains(pr["title"], "128Mi -> 256Mi") ||
		!strings.Contains(pr["body"], "alice") || !strings.Contains(pr["body"], "not merged") || !strings.Contains(pr["body"], "ran out of memory") {
		t.Errorf("PR body wrong: %+v", pr)
	}
	for _, c := range gh.calls {
		if strings.Contains(c, "merge") {
			t.Errorf("DevForge must never merge: %s", c)
		}
	}
}

func TestOpenMemoryPRRejectsBadConfig(t *testing.T) {
	act := &Action{Target: "web", Params: `{}`}
	if _, err := remediationApp(t, nil, Config{}).openMemoryPR(context.Background(), act); err == nil {
		t.Error("missing token must be an error")
	}
	bad := remediationApp(t, nil, Config{GitHubToken: "tok", GitHubRepoTemplate: "../../etc/{deployment}"})
	if _, err := bad.openMemoryPR(context.Background(), act); err == nil || !strings.Contains(err.Error(), "valid owner/repo") {
		t.Errorf("a path-like repo must be rejected, got %v", err)
	}
}

// ---------- approval flow, policy and audit ---------------------------------------------------------

func TestApproveRollbackEndToEnd(t *testing.T) {
	cs := cluster(time.Minute, true, nil)
	app := remediationApp(t, cs, Config{})
	inc := newIncident(t, app)
	act := proposeFor(t, app, inc, KindRollback)

	msg, err := app.decide(context.Background(), act.ID, "alice", true)
	if err != nil || !strings.Contains(msg, "rolled deployment `web` back from revision 2 to 1") {
		t.Fatalf("got (%q, %v)", msg, err)
	}
	got, _ := app.store.GetAction(act.ID)
	if got.Status != ActionExecuted || got.DecidedBy != "alice" || got.ExecutedAt.IsZero() {
		t.Errorf("action row wrong: %+v", got)
	}
	d, _ := cs.AppsV1().Deployments("shop").Get(context.Background(), "web", metav1.GetOptions{})
	if d.Spec.Template.Spec.Containers[0].Image != "web:v1" {
		t.Error("the Deployment was not rolled back")
	}
	waitAudit(t, app, auditVerifyFail)
	events := auditEvents(t, app)
	for _, want := range []string{auditProposed, auditApproved, auditExecuted} {
		if !hasEvent(events, want) {
			t.Errorf("audit trail missing %q: %v", want, events)
		}
	}
}

func TestApprovalIsSingleUse(t *testing.T) {
	cs := cluster(time.Minute, true, nil)
	app := remediationApp(t, cs, Config{})
	act := proposeFor(t, app, newIncident(t, app), KindRestart)

	if _, err := app.decide(context.Background(), act.ID, "alice", true); err != nil {
		t.Fatal(err)
	}
	if _, err := app.decide(context.Background(), act.ID, "bob", true); !errors.Is(err, ErrConflict) {
		t.Errorf("a second approval must conflict, got %v", err)
	}
	if _, err := app.decide(context.Background(), 999, "bob", true); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown action: %v", err)
	}
	waitAudit(t, app, auditVerifyFail)
}

func TestRejectDoesNothing(t *testing.T) {
	cs := cluster(time.Minute, true, nil)
	app := remediationApp(t, cs, Config{})
	act := proposeFor(t, app, newIncident(t, app), KindRollback)

	if _, err := app.decide(context.Background(), act.ID, "alice", false); err != nil {
		t.Fatal(err)
	}
	got, _ := app.store.GetAction(act.ID)
	d, _ := cs.AppsV1().Deployments("shop").Get(context.Background(), "web", metav1.GetOptions{})
	if got.Status != ActionRejected || d.Spec.Template.Spec.Containers[0].Image != "web:v2" {
		t.Errorf("reject must leave the cluster alone: status=%s image=%s", got.Status, d.Spec.Template.Spec.Containers[0].Image)
	}
	if _, err := app.decide(context.Background(), act.ID, "alice", true); !errors.Is(err, ErrConflict) {
		t.Error("a rejected action can no longer be approved")
	}
}

func TestPolicyBlocks(t *testing.T) {
	cases := []struct {
		name   string
		cfg    Config
		cs     kubernetes.Interface
		actor  string
		reason string
		setup  func(app *App, act *Action)
	}{
		{"namespace not allow-listed", Config{RemediationNamespaces: []string{"other"}}, cluster(time.Minute, true, nil), "alice", "allow-list", nil},
		{"remediation off by default", Config{RemediationNamespaces: []string{}}, cluster(time.Minute, true, nil), "alice", "allow-list", nil},
		{"not an approver", Config{Approvers: []string{"carol"}}, cluster(time.Minute, true, nil), "mallory", "not an authorised approver", nil},
		{"workload opted out", Config{}, cluster(time.Minute, true, map[string]string{optOutLabel: "true"}), "alice", "opted out", nil},
		{"cooldown", Config{RemediationCooldown: time.Hour}, cluster(time.Minute, true, nil), "alice", "cooldown", func(app *App, act *Action) {
			prev, _ := app.store.CreateAction(Action{IncidentID: 1, Kind: KindRestart, Namespace: "shop", Target: "web", Rule: "t"})
			app.store.FinishAction(prev.ID, ActionExecuted, "earlier")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.cfg.RemediationNamespaces == nil {
				tc.cfg.RemediationNamespaces = []string{"shop"}
			}
			app := remediationApp(t, tc.cs, tc.cfg)
			act := proposeFor(t, app, newIncident(t, app), KindRestart)
			if tc.setup != nil {
				tc.setup(app, act)
			}
			_, err := app.decide(context.Background(), act.ID, tc.actor, true)
			if !errors.Is(err, ErrBlocked) || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("want blocked (%s), got %v", tc.reason, err)
			}
			got, _ := app.store.GetAction(act.ID)
			if got.Status != ActionProposed {
				t.Errorf("a blocked attempt must not consume the proposal, status = %s", got.Status)
			}
			if !hasEvent(auditEvents(t, app), auditBlocked) {
				t.Error("blocked attempts must be audited")
			}
		})
	}
}

func TestSystemNamespacesAreNeverTouched(t *testing.T) {
	app := remediationApp(t, fake.NewSimpleClientset(), Config{RemediationNamespaces: []string{"kube-system"}})
	act, _ := app.store.CreateAction(Action{IncidentID: 1, Kind: KindRestart, Namespace: "kube-system", Target: "coredns", Rule: "t"})
	if _, err := app.decide(context.Background(), act.ID, "alice", true); !errors.Is(err, ErrBlocked) {
		t.Errorf("kube-system must be refused even if allow-listed, got %v", err)
	}
}

func TestExpiredProposalCannotBeApproved(t *testing.T) {
	app := remediationApp(t, cluster(time.Minute, true, nil), Config{ProposalTTL: time.Minute})
	act := proposeFor(t, app, newIncident(t, app), KindRestart)
	app.store.db.Exec(`UPDATE actions SET created_at = ? WHERE id = ?`, time.Now().Add(-time.Hour).Unix(), act.ID)

	if _, err := app.decide(context.Background(), act.ID, "alice", true); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("got %v", err)
	}
	if got, _ := app.store.GetAction(act.ID); got.Status != ActionExpired {
		t.Errorf("status = %s", got.Status)
	}
}

func TestDryRunChangesNothing(t *testing.T) {
	cs := cluster(time.Minute, true, nil)
	app := remediationApp(t, cs, Config{RemediationDryRun: true})
	act := proposeFor(t, app, newIncident(t, app), KindRollback)

	msg, err := app.decide(context.Background(), act.ID, "alice", true)
	if err != nil || !strings.Contains(msg, "dry-run") {
		t.Fatalf("got (%q, %v)", msg, err)
	}
	d, _ := cs.AppsV1().Deployments("shop").Get(context.Background(), "web", metav1.GetOptions{})
	if d.Spec.Template.Spec.Containers[0].Image != "web:v2" {
		t.Error("dry-run must not touch the cluster")
	}
	if !hasEvent(auditEvents(t, app), auditDryRun) {
		t.Error("dry-run must be audited")
	}
}

func TestFailedActionIsRecorded(t *testing.T) {
	app := remediationApp(t, cluster(time.Minute, false, nil), Config{}) // nothing to roll back to
	act := proposeFor(t, app, newIncident(t, app), KindRollback)

	if _, err := app.decide(context.Background(), act.ID, "alice", true); err == nil {
		t.Fatal("expected the rollback to fail")
	}
	got, _ := app.store.GetAction(act.ID)
	if got.Status != ActionFailed || !strings.Contains(got.Result, "no previous revision") || !hasEvent(auditEvents(t, app), auditFailed) {
		t.Errorf("failure not recorded: %+v", got)
	}
}

func TestAuditLogIsAppendOnly(t *testing.T) {
	app := newTestApp(t, Config{})
	app.audit("alice", auditApproved, 1, 2, "something")
	if _, err := app.store.db.Exec(`UPDATE audit SET detail = 'tampered'`); err == nil {
		t.Error("UPDATE on the audit table must be refused by the database")
	}
	if _, err := app.store.db.Exec(`DELETE FROM audit`); err == nil {
		t.Error("DELETE on the audit table must be refused by the database")
	}
	entries, _ := app.store.ListAudit(10, 0)
	if len(entries) != 1 || entries[0].Detail != "something" {
		t.Errorf("entry changed: %+v", entries)
	}
}

// ---------- HTTP API ---------------------------------------------------------------------------

func TestApprovalAPI(t *testing.T) {
	app := remediationApp(t, cluster(time.Minute, true, nil), Config{Token: "secret"})
	srv := httptest.NewServer(app.mux())
	defer srv.Close()
	act := proposeFor(t, app, newIncident(t, app), KindRestart)

	call := func(path, token, actor string) (int, string) {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if actor != "" {
			req.Header.Set("X-Actor", actor)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]string
		json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out["message"] + out["error"]
	}
	id := "/actions/" + itoa(act.ID)

	if code, _ := call(id+"/approve", "", ""); code != 401 {
		t.Errorf("no token: %d", code)
	}
	if code, _ := call("/actions/abc/approve", "secret", ""); code != 400 {
		t.Errorf("bad id: %d", code)
	}
	if code, _ := call("/actions/999/approve", "secret", ""); code != 404 {
		t.Errorf("missing action: %d", code)
	}
	if code, body := call(id+"/approve", "secret", "dana"); code != 200 || !strings.Contains(body, "approved by dana") {
		t.Errorf("approve: %d %s", code, body)
	}
	if code, _ := call(id+"/approve", "secret", "dana"); code != 409 {
		t.Errorf("repeat approval should conflict: %d", code)
	}
	waitAudit(t, app, auditVerifyFail)
}

func TestIncidentPageShowsActionsAndAudit(t *testing.T) {
	app := remediationApp(t, cluster(time.Minute, true, nil), Config{})
	inc := newIncident(t, app)
	proposeFor(t, app, inc, KindRollback)
	srv := httptest.NewServer(app.mux())
	defer srv.Close()

	for _, path := range []string{"/incidents/1", "/audit"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		var sb strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := resp.Body.Read(buf)
			sb.Write(buf[:n])
			if err != nil {
				break
			}
		}
		resp.Body.Close()
		body := sb.String()
		if resp.StatusCode != 200 || !strings.Contains(body, "rollback shop/web") && !strings.Contains(body, "proposed") {
			t.Errorf("%s: status %d, body missing the proposal", path, resp.StatusCode)
		}
	}
}

func TestSlackProposalButtonsAndApproval(t *testing.T) {
	app := remediationApp(t, cluster(time.Minute, true, nil), Config{SlackSecret: secret, PublicURL: "http://x"})
	inc := newIncident(t, app)
	act := proposeFor(t, app, inc, KindRollback)

	payload := string(slackPayload(app.cfg, *inc, "report", []Action{*act}))
	for _, want := range []string{`"action_id":"approve"`, `"action_id":"reject"`, "Proposed actions", "rollback shop/web"} {
		if !strings.Contains(payload, want) {
			t.Errorf("payload missing %s", want)
		}
	}

	rec := httptest.NewRecorder()
	app.slackActions(rec, signedRequest(t, time.Now(), actionBody("approve", itoa(act.ID)), ""))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "rolled deployment") {
		t.Fatalf("approve via Slack: %d %s", rec.Code, rec.Body.String())
	}
	// a second click is refused politely
	rec = httptest.NewRecorder()
	app.slackActions(rec, signedRequest(t, time.Now(), actionBody("approve", itoa(act.ID)), ""))
	if !strings.Contains(rec.Body.String(), "already executed") {
		t.Errorf("second click: %s", rec.Body.String())
	}
	waitAudit(t, app, auditVerifyFail)
}

func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}
