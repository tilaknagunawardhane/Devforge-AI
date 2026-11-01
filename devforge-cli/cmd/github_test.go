package cmd

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type ghCall struct {
	method, path, auth string
	body               map[string]any
}

func fakeGitHubAPI(t *testing.T, status int, reply string) (*httptest.Server, *[]ghCall) {
	var calls []ghCall
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		calls = append(calls, ghCall{r.Method, r.URL.Path, r.Header.Get("Authorization"), body})
		w.WriteHeader(status)
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("GITHUB_API_URL", srv.URL)
	t.Setenv("GITHUB_TOKEN", "tok")
	t.Setenv("GITHUB_USERNAME", "alice")
	return srv, &calls
}

func TestCreateGitHubRepo(t *testing.T) {
	_, calls := fakeGitHubAPI(t, 201, `{}`)
	if err := createGitHubRepo("devforge-app-svc"); err != nil {
		t.Fatal(err)
	}
	c := (*calls)[0]
	if c.method != "POST" || c.path != "/user/repos" || c.auth != "token tok" || c.body["name"] != "devforge-app-svc" || c.body["private"] != false {
		t.Errorf("unexpected request: %+v", c)
	}
}

func TestGitHubErrorsAreReportedNotSwallowed(t *testing.T) {
	fakeGitHubAPI(t, 422, `{"message":"name already exists on this account"}`)

	err := createGitHubRepo("dup")
	if err == nil || !strings.Contains(err.Error(), "422") || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("create should report GitHub's reply, got %v", err)
	}
	// This used to return nil whatever GitHub answered.
	if err := uploadFileToGitHub("repo", "main.go", "package main"); err == nil || !strings.Contains(err.Error(), "422") {
		t.Errorf("a refused upload must be an error, got %v", err)
	}
}

func TestUploadFileToGitHub(t *testing.T) {
	_, calls := fakeGitHubAPI(t, 201, `{}`)
	if err := uploadFileToGitHub("devforge-app-svc", ".github/workflows/ci.yml", "name: ci\n"); err != nil {
		t.Fatal(err)
	}
	c := (*calls)[0]
	if c.method != "PUT" || c.path != "/repos/alice/devforge-app-svc/contents/.github/workflows/ci.yml" {
		t.Errorf("unexpected request: %s %s", c.method, c.path)
	}
	raw, _ := base64.StdEncoding.DecodeString(c.body["content"].(string))
	if string(raw) != "name: ci\n" {
		t.Errorf("content = %q", raw)
	}
}
