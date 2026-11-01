package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func alertFor(name, pod, ns string) Alert {
	return Alert{Status: "firing", Labels: map[string]string{"alertname": name, "pod": pod, "namespace": ns}}
}

// waitRCA waits for the background analysis to store its report, so the test can end cleanly.
func waitRCA(t *testing.T, app *App, id int64) *Incident {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if inc, err := app.store.Get(id); err == nil && inc.RCA != "" {
			return inc
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("analysis did not finish")
	return nil
}

func TestStoreLifecycle(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	inc, err := s.Create(Incident{Key: "k1", Subject: "web", Namespace: "default", Pods: "web-1-a", AlertCount: 1})
	if err != nil || inc.ID == 0 || inc.Status != StatusOpen {
		t.Fatalf("create: %+v %v", inc, err)
	}
	if found, _ := s.FindRecent("k1", time.Hour); found == nil || found.ID != inc.ID {
		t.Error("FindRecent should find the open incident")
	}
	if found, _ := s.FindRecent("other", time.Hour); found != nil {
		t.Error("different key must not match")
	}

	s.AddAlerts(inc.ID, []string{"web-1-b", "web-1-a"})
	got, _ := s.Get(inc.ID)
	if got.AlertCount != 2 || got.Pods != "web-1-a,web-1-b" {
		t.Errorf("AddAlerts: count=%d pods=%q (only the new pod counts)", got.AlertCount, got.Pods)
	}
	// Alertmanager re-sends the same group every group_interval: that must not inflate the count.
	s.AddAlerts(inc.ID, []string{"web-1-b", "web-1-a"})
	s.AddAlerts(inc.ID, nil)
	if got, _ = s.Get(inc.ID); got.AlertCount != 2 {
		t.Errorf("repeat notifications changed the count to %d", got.AlertCount)
	}

	s.SetStatus(inc.ID, StatusAcknowledged, "alice")
	got, _ = s.Get(inc.ID)
	if got.Status != StatusAcknowledged || got.AckedBy != "alice" {
		t.Errorf("ack: %+v", got)
	}

	s.ResolveByKey("k1")
	if found, _ := s.FindRecent("k1", time.Hour); found != nil {
		t.Error("resolved incidents must not absorb new alerts")
	}
	if list, _ := s.List(10); len(list) != 1 {
		t.Errorf("List returned %d", len(list))
	}
}

func TestStoreWindowExpires(t *testing.T) {
	s, _ := OpenStore(filepath.Join(t.TempDir(), "s.db"))
	defer s.Close()
	s.Create(Incident{Key: "k", Subject: "x", Namespace: "default"})
	s.db.Exec(`UPDATE incidents SET updated_at = ?`, time.Now().Add(-2*time.Hour).Unix())
	if found, _ := s.FindRecent("k", time.Hour); found != nil {
		t.Error("an incident older than the window must not be reused")
	}
}

func TestGroupKeyAndSubject(t *testing.T) {
	if got := groupKey(AlertPayload{GroupKey: "{}:{alertname=\"X\"}"}); got != "{}:{alertname=\"X\"}" {
		t.Errorf("Alertmanager key must be used as-is, got %q", got)
	}
	p := AlertPayload{Alerts: []Alert{alertFor("A", "web-5d-aaa", "shop"), alertFor("A", "web-5d-bbb", "shop")}}
	if got := groupKey(p); got != "A|shop|web-5d" {
		t.Errorf("derived key = %q", got)
	}
	names, pods, ns, _ := summarize(p.Alerts)
	if subjectFor(names, pods, "") != "web-5d (2 pods)" || ns != "shop" {
		t.Errorf("subject = %q ns = %q", subjectFor(names, pods, ""), ns)
	}
	if subjectFor([]string{"A"}, []string{"only-1-x"}, "") != "only-1-x" {
		t.Error("single pod subject should be the pod")
	}
	if subjectFor([]string{"HighErrorRate"}, nil, "billing") != "billing" {
		t.Error("service-level alert subject should be the service")
	}
}

func TestHandleGroupCreatesOneIncidentForManyPods(t *testing.T) {
	app := newTestApp(t, Config{})
	p := AlertPayload{GroupKey: "g1", Status: "firing",
		Alerts: []Alert{alertFor("Chaos", "web-5d-aaa", "default"), alertFor("Chaos", "web-5d-bbb", "default"), alertFor("Chaos", "web-5d-ccc", "default")}}

	if got := app.handleGroup(p); got != "created" {
		t.Fatalf("got %q, want created", got)
	}
	list, _ := app.store.List(10)
	if len(list) != 1 || list[0].AlertCount != 3 || list[0].Subject != "web-5d (3 pods)" {
		t.Fatalf("expected one incident for the 3-pod group, got %+v", list)
	}
	inc := waitRCA(t, app, list[0].ID)
	if !strings.Contains(inc.RCA, "AI analysis unavailable") {
		t.Errorf("with Ollama down the stored report should be the fallback, got %q", inc.RCA)
	}
}

func TestHandleGroupDeduplicates(t *testing.T) {
	app := newTestApp(t, Config{})
	first := AlertPayload{GroupKey: "g1", Alerts: []Alert{alertFor("Chaos", "web-5d-aaa", "default")}}
	if app.handleGroup(first) != "created" {
		t.Fatal("first group should create an incident")
	}
	waitRCA(t, app, 1)

	// Same group again (Alertmanager re-notifies), now with an extra pod: no second RCA.
	again := AlertPayload{GroupKey: "g1", Alerts: []Alert{alertFor("Chaos", "web-5d-aaa", "default"), alertFor("Chaos", "web-5d-zzz", "default")}}
	if got := app.handleGroup(again); got != "deduplicated" {
		t.Fatalf("got %q, want deduplicated", got)
	}
	list, _ := app.store.List(10)
	if len(list) != 1 || list[0].AlertCount != 2 || !strings.Contains(list[0].Pods, "web-5d-zzz") {
		t.Errorf("incident should absorb the one new pod (and not recount the repeated one): %+v", list)
	}

	// A different group is a different incident.
	other := AlertPayload{GroupKey: "g2", Alerts: []Alert{alertFor("Other", "db-1-a", "default")}}
	if app.handleGroup(other) != "created" {
		t.Error("different group key should open a new incident")
	}
	waitRCA(t, app, 2)
}

func TestHandleGroupResolvedAndIgnored(t *testing.T) {
	app := newTestApp(t, Config{})
	if got := app.handleGroup(AlertPayload{GroupKey: "g", Alerts: nil}); got != "ignored" {
		t.Errorf("empty group: %q", got)
	}
	app.handleGroup(AlertPayload{GroupKey: "g", Alerts: []Alert{alertFor("A", "p-1-a", "default")}})
	waitRCA(t, app, 1)
	if got := app.handleGroup(AlertPayload{GroupKey: "g", Status: "resolved"}); got != "resolved" {
		t.Errorf("resolved: %q", got)
	}
	if inc, _ := app.store.Get(1); inc.Status != StatusResolved {
		t.Errorf("status = %q, want resolved", inc.Status)
	}
	// After resolution the same group opens a fresh incident.
	if got := app.handleGroup(AlertPayload{GroupKey: "g", Alerts: []Alert{alertFor("A", "p-1-a", "default")}}); got != "created" {
		t.Errorf("after resolve: %q, want created", got)
	}
	waitRCA(t, app, 2)
}

func TestResolvedAlertsInFiringGroupAreSkipped(t *testing.T) {
	app := newTestApp(t, Config{})
	al := alertFor("A", "p-1-a", "default")
	al.Status = "resolved"
	if got := app.handleGroup(AlertPayload{GroupKey: "g", Status: "firing", Alerts: []Alert{al}}); got != "ignored" {
		t.Errorf("got %q, want ignored", got)
	}
}
