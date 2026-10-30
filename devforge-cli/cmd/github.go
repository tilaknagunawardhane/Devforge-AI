package cmd

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
)

// createGitHubRepo calls the GitHub API to create a new public repository
func createGitHubRepo(repoName string) error {
	token := os.Getenv("GITHUB_TOKEN")

	reqBody, _ := json.Marshal(map[string]interface{}{
		"name":    repoName,
		"private": false, // Public so ArgoCD can read it easily
	})

	req, _ := http.NewRequest("POST", "https://api.github.com/user/repos", bytes.NewBuffer(reqBody))
	req.Header.Set("Authorization", "token "+token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != 201 {
		return fmt.Errorf("failed to create repo %s", repoName)
	}
	return nil
}

// uploadFileToGitHub pushes a single file to a GitHub repository directly via API
func uploadFileToGitHub(repoName, filePath, content string) error {
	token := os.Getenv("GITHUB_TOKEN")
	username := os.Getenv("GITHUB_USERNAME")

	encodedContent := base64.StdEncoding.EncodeToString([]byte(content))

	reqBody, _ := json.Marshal(map[string]interface{}{
		"message": "Automated commit from DevForge AI",
		"content": encodedContent,
	})

	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/contents/%s", username, repoName, filePath)
	req, _ := http.NewRequest("PUT", url, bytes.NewBuffer(reqBody))
	req.Header.Set("Authorization", "token "+token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	client := &http.Client{}
	_, err := client.Do(req)
	return err
}
