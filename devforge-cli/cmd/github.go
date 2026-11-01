package cmd

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

var githubHTTP = &http.Client{Timeout: 30 * time.Second}

// githubAPI is the GitHub REST API root. GITHUB_API_URL lets tests (or GitHub Enterprise) point elsewhere.
func githubAPI() string {
	return strings.TrimRight(envOr("GITHUB_API_URL", "https://api.github.com"), "/")
}

// githubDo sends one authenticated request and returns an error that includes GitHub's reply
// for any non-2xx status, so a refused request is never mistaken for success.
func githubDo(method, url string, body any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "token "+os.Getenv("GITHUB_TOKEN"))
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := githubHTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Errorf("github %s returned %d: %s", method, resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return nil
}

// createGitHubRepo calls the GitHub API to create a new public repository
func createGitHubRepo(repoName string) error {
	err := githubDo(http.MethodPost, githubAPI()+"/user/repos", map[string]any{
		"name":    repoName,
		"private": false, // Public so Argo CD can read it without credentials
	})
	if err != nil {
		return fmt.Errorf("failed to create repo %s: %w", repoName, err)
	}
	return nil
}

// uploadFileToGitHub pushes a single file to a GitHub repository directly via API
func uploadFileToGitHub(repoName, filePath, content string) error {
	url := fmt.Sprintf("%s/repos/%s/%s/contents/%s", githubAPI(), os.Getenv("GITHUB_USERNAME"), repoName, filePath)
	return githubDo(http.MethodPut, url, map[string]any{
		"message": "Automated commit from DevForge AI",
		"content": base64.StdEncoding.EncodeToString([]byte(content)),
	})
}
