package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/spf13/cobra"
)

// These variables will store the inputs from the user's flags
var (
	projectName string
	language    string
	database    string
)

// writeFiles writes a map of relative path -> content under dir, creating folders as needed.
func writeFiles(dir string, files map[string]string) error {
	for rel, content := range files {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			return err
		}
		if err := os.WriteFile(full, []byte(content), 0644); err != nil {
			return err
		}
	}
	return nil
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

var createCmd = &cobra.Command{
	Use:   "create",
	Short: "Scaffold a new microservice project",
	Run: func(cmd *cobra.Command, args []string) {
		if err := validateName(projectName); err != nil {
			fmt.Println("❌ Error:", err)
			return
		}
		fmt.Printf("🚀 DevForge AI is building '%s'...\n", projectName)

		data := newScaffoldData(projectName, database)
		useTemplates := language == "go"

		// The Go service comes from a tested template, so /health, /ready and /metrics are guaranteed.
		scaffold, err := buildScaffold(data, useTemplates)
		if err != nil {
			fmt.Println("❌ Error:", err)
			return
		}

		// Other languages still use the local AI model for the application code.
		if !useTemplates {
			fmt.Printf("⏳ Status: AI Agent is writing the %s code. This takes about 10-30 seconds...\n", language)
			fmt.Println("⚠️ Only Go uses the built-in template; check the generated code exposes /health and /metrics on port 8080.")
			files, err := generateCode(projectName, language, database)
			if err != nil {
				fmt.Println("❌ Error:", err)
				return
			}
			mainFileName := "main.py"
			if language == "node" || language == "javascript" {
				mainFileName = "index.js"
			}
			scaffold.App[mainFileName] = files.MainCode
			scaffold.App["Dockerfile"] = files.Dockerfile
		}

		// Local layout: <name>/ is the app repo, <name>/manifests/ is the manifest repo.
		if err := writeFiles(projectName, scaffold.App); err != nil {
			fmt.Println("❌ Error writing app files:", err)
			return
		}
		if err := writeFiles(filepath.Join(projectName, "manifests"), scaffold.Manifests); err != nil {
			fmt.Println("❌ Error writing manifests:", err)
			return
		}
		fmt.Printf("✅ Success! %s has been scaffolded at ./%s\n", projectName, projectName)
		fmt.Println("📈 Included: /health, /ready, /metrics, ServiceMonitor, alert rules, Grafana dashboard, HPA, PDB, hardened Deployment and CI pipeline")

		githubUser := os.Getenv("GITHUB_USERNAME")
		githubToken := os.Getenv("GITHUB_TOKEN")
		if githubUser == "" || githubToken == "" {
			fmt.Println("⚠️ GITHUB_USERNAME or GITHUB_TOKEN environment variables not set. Skipping GitHub upload.")
			return
		}

		appRepo := "devforge-app-" + projectName
		manifestRepo := "devforge-manifests-" + projectName

		fmt.Println("🌐 Creating dual GitHub Repositories for GitOps...")
		for _, repo := range []string{appRepo, manifestRepo} {
			if err := createGitHubRepo(repo); err != nil {
				fmt.Println("⚠️", err)
			}
		}

		fmt.Println("⬆️ Uploading files to GitHub APIs...")
		for _, path := range sortedKeys(scaffold.App) {
			if err := uploadFileToGitHub(appRepo, path, scaffold.App[path]); err != nil {
				fmt.Println("⚠️ upload failed:", path, err)
			}
		}
		for _, path := range sortedKeys(scaffold.Manifests) {
			if err := uploadFileToGitHub(manifestRepo, path, scaffold.Manifests[path]); err != nil {
				fmt.Println("⚠️ upload failed:", path, err)
			}
		}
		fmt.Printf("✅ GitOps Repositories created! Check https://github.com/%s\n", githubUser)
	},
}

func init() {
	// Register this command with the main DevForge CLI
	rootCmd.AddCommand(createCmd)

	// Define the flags (e.g., --name, --lang, --db) and their default values
	createCmd.Flags().StringVarP(&projectName, "name", "n", "my-service", "Name of the microservice")
	createCmd.Flags().StringVarP(&language, "lang", "l", "go", "Programming language (go uses the built-in template; others use the local AI model)")
	createCmd.Flags().StringVarP(&database, "db", "d", "postgres", "Database type (e.g., postgres, redis)")
}
