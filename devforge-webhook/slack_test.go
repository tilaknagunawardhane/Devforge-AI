package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
)

const secret = "s3cret"

func signedRequest(t *testing.T, ts time.Time, body string, sig string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/slack/actions", strings.NewReader(body))
	stamp := strconv.FormatInt(ts.Unix(), 10)
	req.Header.Set("X-Slack-Request-Timestamp", stamp)
	if sig == "" {
		sig = signSlack(secret, stamp, []byte(body))
	}
	req.Header.Set("X-Slack-Signature", sig)
	return req
}

func actionBody(action, incidentID string) string {
	payload, _ := json.Marshal(map[string]any{
		"user":    map[string]string{"username": "alice"},
		"actions": []map[string]string{{"action_id": action, "value": incidentID}},
	})
	return url.Values{"payload": {string(payload)}}.Encode()
}

func TestVerifySlack(t *testing.T) {
	now := time.Now()
	body := []byte("payload=x")
	good := signedRequest(t, now, string(body), "")
	if err := verifySlack(secret, good.Header, body, now); err != nil {
		t.Errorf("valid signature rejected: %v", err)
	}
	bad := signedRequest(t, now, string(body), "v0=deadbeef")
	if err := verifySlack(secret, bad.Header, body, now); err == nil {
		t.Error("wrong signature accepted")
	}
	if err := verifySlack("other-secret", good.Header, body, now); err == nil {
		t.Error("signature from another secret accepted")
	}
	old := signedRequest(t, now.Add(-10*time.Minute), string(body), "")
	if err := verifySlack(secret, old.Header, body, now); err == nil {
		t.Error("replayed (old) request accepted")
	}
	if err := verifySlack(secret, http.Header{}, body, now); err == nil {
		t.Error("request without headers accepted")
	}
}

func TestSlackPayloadButtonsOnlyWithSecret(t *testing.T) {
	inc := Incident{ID: 4, Subject: "web", AlertCount: 2}
	plain := string(slackPayload(Config{PublicURL: "http://x"}, inc, "report", nil))
	if strings.Contains(plain, "blocks") || !strings.Contains(plain, "http://x/incidents/4") {
		t.Errorf("plain payload wrong: %s", plain)
	}
	rich := string(slackPayload(Config{PublicURL: "http://x", SlackSecret: secret}, inc, "report", nil))
	for _, want := range []string{`"action_id":"ack"`, `"action_id":"restart"`, `"action_id":"rollback"`, `"confirm"`, `"value":"4"`} {
		if !strings.Contains(rich, want) {
			t.Errorf("buttons payload missing %s", want)
		}
	}
}

func TestSlackActionsRejectUnsigned(t *testing.T) {
	app := newTestApp(t, Config{SlackSecret: secret})
	for name, req := range map[string]*http.Request{
		"bad signature": signedRequest(t, time.Now(), actionBody("ack", "1"), "v0=00"),
		"old timestamp": signedRequest(t, time.Now().Add(-time.Hour), actionBody("ack", "1"), ""),
	} {
		rec := httptest.NewRecorder()
		app.slackActions(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: got %d, want 401", name, rec.Code)
		}
	}
	// Without a configured secret the endpoint does not exist at all.
	off := newTestApp(t, Config{})
	rec := httptest.NewRecorder()
	off.slackActions(rec, signedRequest(t, time.Now(), actionBody("ack", "1"), ""))
	if rec.Code != http.StatusNotFound {
		t.Errorf("unconfigured: got %d, want 404", rec.Code)
	}
}

func TestSlackAcknowledge(t *testing.T) {
	app := newTestApp(t, Config{SlackSecret: secret})
	inc, _ := app.store.Create(Incident{Key: "k", Subject: "web", Namespace: "default", Pods: "web-5d-aaa"})

	rec := httptest.NewRecorder()
	app.slackActions(rec, signedRequest(t, time.Now(), actionBody("ack", strconv.FormatInt(inc.ID, 10)), ""))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "acknowledged by alice") {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	got, _ := app.store.Get(inc.ID)
	if got.Status != StatusAcknowledged || got.AckedBy != "alice" {
		t.Errorf("incident not acknowledged: %+v", got)
	}

	rec = httptest.NewRecorder()
	app.slackActions(rec, signedRequest(t, time.Now(), actionBody("explode", strconv.FormatInt(inc.ID, 10)), ""))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("unknown action: got %d", rec.Code)
	}
}

func TestAcknowledgeTeachesRetrievalOnlyRealAnalyses(t *testing.T) {
	srv, fake := newFakeStack(t)
	app := newTestApp(t, Config{SlackSecret: secret})
	app.cfg.OllamaURL = srv.URL
	app.rag = NewRAG(Config{QdrantURL: srv.URL, OllamaURL: srv.URL, EmbedModel: "e", QdrantCollec: "c", RAGResults: 3})

	real, _ := app.store.Create(Incident{Key: "a", Subject: "web", Namespace: "default"})
	app.store.SetRCA(real.ID, "*Summary* memory limit too low")
	fallback, _ := app.store.Create(Incident{Key: "b", Subject: "db", Namespace: "default"})
	app.store.SetRCA(fallback.ID, ":warning: *Incident: db* AI analysis unavailable")
	unanalysed, _ := app.store.Create(Incident{Key: "c", Subject: "cache", Namespace: "default"})

	for _, id := range []int64{real.ID, fallback.ID, unanalysed.ID} {
		rec := httptest.NewRecorder()
		app.slackActions(rec, signedRequest(t, time.Now(), actionBody("ack", strconv.FormatInt(id, 10)), ""))
		if rec.Code != 200 {
			t.Fatalf("ack %d: %d", id, rec.Code)
		}
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		fake.mu.Lock()
		n := len(fake.points)
		fake.mu.Unlock()
		if n >= 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond) // give any wrongly-indexed incident time to show up
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.points) != 1 {
		t.Fatalf("expected only the real analysis to be indexed, got %d points", len(fake.points))
	}
	if src := fake.points[0]["payload"].(map[string]any)["source"]; !strings.Contains(src.(string), "#1") {
		t.Errorf("wrong incident indexed: %v", src)
	}
}

func TestSlackRestartPatchesDeployment(t *testing.T) {
	app := newTestApp(t, Config{SlackSecret: secret, RemediationNamespaces: []string{"shop"}})
	cs := fake.NewSimpleClientset(&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "shop"}})
	app.kube = func() (kubernetes.Interface, error) { return cs, nil }
	inc, _ := app.store.Create(Incident{Key: "k", Subject: "web", Namespace: "shop", Pods: "web-5d-aaa"})

	rec := httptest.NewRecorder()
	app.slackActions(rec, signedRequest(t, time.Now(), actionBody("restart", strconv.FormatInt(inc.ID, 10)), ""))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "restarted deployment `web`") {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	dep, _ := cs.AppsV1().Deployments("shop").Get(context.Background(), "web", metav1.GetOptions{})
	if dep.Spec.Template.Annotations["kubectl.kubernetes.io/restartedAt"] == "" {
		t.Error("restartedAt annotation was not set")
	}
	waitAudit(t, app, auditVerifyFail) // the fake cluster never reports a finished rollout
}

func TestSlackRestartFailsSafely(t *testing.T) {
	app := newTestApp(t, Config{SlackSecret: secret, RemediationNamespaces: []string{"shop"}})
	app.kube = func() (kubernetes.Interface, error) { return fake.NewSimpleClientset(), nil } // no deployments
	withPods, _ := app.store.Create(Incident{Key: "a", Subject: "web", Namespace: "shop", Pods: "web-5d-aaa"})
	noPods, _ := app.store.Create(Incident{Key: "b", Subject: "billing", Namespace: "shop"})

	for _, tc := range []struct {
		id   int64
		want string
	}{{withPods.ID, "not found"}, {noPods.ID, "names no pods"}} {
		rec := httptest.NewRecorder()
		app.slackActions(rec, signedRequest(t, time.Now(), actionBody("restart", strconv.FormatInt(tc.id, 10)), ""))
		if !strings.Contains(rec.Body.String(), tc.want) {
			t.Errorf("incident %d: reply %q should mention %q", tc.id, rec.Body.String(), tc.want)
		}
	}
}
