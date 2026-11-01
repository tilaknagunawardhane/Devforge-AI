package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The fix PR edits the manifest the DevForge CLI generates:
//
//	limits:
//	  cpu: 250m
//	  memory: 128Mi
var memLimitRE = regexp.MustCompile(`(?s)(limits:\s*\n(?:[ \t]+cpu:[^\n]*\n)?[ \t]+memory:[ \t]*)(\d+)(Mi|Gi)`)

var repoRE = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// raiseMemoryLimit multiplies the first memory limit in the manifest by factor, capped at maxMi.
func raiseMemoryLimit(manifest string, factor, maxMi int) (out string, oldMi, newMi int, err error) {
	loc := memLimitRE.FindStringSubmatchIndex(manifest)
	if loc == nil {
		return "", 0, 0, fmt.Errorf("no `limits: memory: <n>Mi|Gi` block found in the manifest")
	}
	n, _ := strconv.Atoi(manifest[loc[4]:loc[5]])
	oldMi = n
	if manifest[loc[6]:loc[7]] == "Gi" {
		oldMi = n * 1024
	}
	newMi = min(oldMi*factor, maxMi)
	if newMi <= oldMi {
		return "", oldMi, oldMi, fmt.Errorf("the memory limit is already %dMi, at or above the %dMi maximum", oldMi, maxMi)
	}
	value := fmt.Sprintf("%dMi", newMi)
	if newMi%1024 == 0 {
		value = fmt.Sprintf("%dGi", newMi/1024)
	}
	return manifest[:loc[4]] + value + manifest[loc[7]:], oldMi, newMi, nil
}

func (a *App) github(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.cfg.GitHubAPI+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+a.cfg.GitHubToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("github %s %s returned %d: %s", method, path, resp.StatusCode, trim(string(raw), 200))
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// openMemoryPR proposes a higher memory limit as a pull request. It never merges: a reviewer decides.
func (a *App) openMemoryPR(ctx context.Context, act *Action) (string, error) {
	if a.cfg.GitHubToken == "" || a.cfg.GitHubRepoTemplate == "" {
		return "", fmt.Errorf("GitHub is not configured (GITHUB_TOKEN and GITHUB_REPO_TEMPLATE)")
	}
	repo := strings.ReplaceAll(a.cfg.GitHubRepoTemplate, "{deployment}", act.Target)
	if !repoRE.MatchString(repo) {
		return "", fmt.Errorf("%q is not a valid owner/repo", repo)
	}
	var params struct {
		Factor int `json:"factor"`
	}
	json.Unmarshal([]byte(act.Params), &params)
	if params.Factor < 2 {
		params.Factor = 2
	}
	path := a.cfg.GitHubManifestPath
	base := "/repos/" + repo

	var meta struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := a.github(ctx, http.MethodGet, base, nil, &meta); err != nil {
		return "", err
	}
	var file struct {
		Content string `json:"content"`
		SHA     string `json:"sha"`
	}
	if err := a.github(ctx, http.MethodGet, base+"/contents/"+path+"?ref="+url.QueryEscape(meta.DefaultBranch), nil, &file); err != nil {
		return "", err
	}
	current, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(file.Content, "\n", ""))
	if err != nil {
		return "", fmt.Errorf("could not decode %s: %w", path, err)
	}
	updated, oldMi, newMi, err := raiseMemoryLimit(string(current), params.Factor, a.cfg.MaxMemoryMi)
	if err != nil {
		return "", err
	}

	var ref struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if err := a.github(ctx, http.MethodGet, base+"/git/ref/heads/"+meta.DefaultBranch, nil, &ref); err != nil {
		return "", err
	}
	branch := fmt.Sprintf("devforge/oom-%s-%d", act.Target, time.Now().Unix())
	if err := a.github(ctx, http.MethodPost, base+"/git/refs", map[string]string{"ref": "refs/heads/" + branch, "sha": ref.Object.SHA}, nil); err != nil {
		return "", err
	}
	title := fmt.Sprintf("fix(%s): raise memory limit %dMi -> %dMi (OOMKilled)", act.Target, oldMi, newMi)
	err = a.github(ctx, http.MethodPut, base+"/contents/"+path, map[string]string{
		"message": title, "branch": branch, "sha": file.SHA,
		"content": base64.StdEncoding.EncodeToString([]byte(updated)),
	}, nil)
	if err != nil {
		return "", err
	}

	inc, _ := a.store.Get(act.IncidentID)
	body := fmt.Sprintf("Proposed automatically by DevForge after incident #%d.\n\n"+
		"- **Rule:** `%s`\n- **Why:** %s\n- **Change:** memory limit %dMi -> %dMi in `%s` (capped at %dMi)\n- **Approved by:** %s\n\n"+
		"This PR was **not merged** by DevForge. Please review whether the application really needs more memory or has a leak.\n",
		act.IncidentID, act.Rule, act.Reason, oldMi, newMi, path, a.cfg.MaxMemoryMi, act.DecidedBy)
	if inc != nil && inc.RCA != "" {
		body += "\n<details><summary>Root cause analysis</summary>\n\n" + trim(inc.RCA, 1500) + "\n\n</details>\n"
	}
	var pr struct {
		URL string `json:"html_url"`
	}
	err = a.github(ctx, http.MethodPost, base+"/pulls", map[string]string{
		"title": title, "head": branch, "base": meta.DefaultBranch, "body": body,
	}, &pr)
	if err != nil {
		return "", err
	}
	return pr.URL, nil
}
