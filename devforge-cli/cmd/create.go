package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/spf13/cobra"
)

// These variables will store the inputs from the user's flags
var (
	projectName string
	language    string
	database    string
	dryRun      bool
	planOnly    bool
	useAI       bool
	slo         float64
	owner       string
	manifestURL string
	operator    bool
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

// printPlan shows what `create` would do without touching the disk or GitHub.
func printPlan(s *Scaffold, name, lang, db string, githubConfigured bool) {
	fmt.Printf("📝 Plan for '%s' (%s, %s)\n\n", name, lang, db)
	fmt.Printf("Local folder ./%s  (app repo files):\n", name)
	for _, p := range sortedKeys(s.App) {
		fmt.Printf("  %-34s %5d bytes\n", p, len(s.App[p]))
	}
	fmt.Printf("\nLocal folder ./%s/manifests  (manifest repo files):\n", name)
	for _, p := range sortedKeys(s.Manifests) {
		fmt.Printf("  %-34s %5d bytes\n", p, len(s.Manifests[p]))
	}
	fmt.Println()
	if githubConfigured {
		fmt.Printf("GitHub: would create repos devforge-app-%s and devforge-manifests-%s and upload the files above.\n", name, name)
	} else {
		fmt.Println("GitHub: GITHUB_USERNAME/GITHUB_TOKEN not set, so no repositories would be created.")
	}
	fmt.Printf("Catalog: would record the service in %s\n\n", catalogPath())
	fmt.Println("Dry run: nothing was written. Re-run without --dry-run/--plan to create it.")
}

var createCmd = &cobra.Command{
	Use:   "create",
	Short: "Scaffold a new microservice project",
	Run: func(cmd *cobra.Command, args []string) {
		if err := validateName(projectName); err != nil {
			fmt.Println("❌ Error:", err)
			os.Exit(1)
		}
		lang, err := normalizeLanguage(language)
		if err != nil {
			fmt.Println("❌ Error:", err)
			os.Exit(1)
		}

		data, err := newScaffoldData(projectName, lang, database).WithSLO(slo)
		if err != nil {
			fmt.Println("❌ Error:", err)
			os.Exit(1)
		}
		ghUser := envOr("GITHUB_USERNAME", "YOUR_GITHUB_USER")
		data = data.WithPlatform(owner, ghUser, manifestURL, operator)
		scaffold, err := buildScaffold(data)
		if err != nil {
			fmt.Println("❌ Error:", err)
			os.Exit(1)
		}

		githubUser := os.Getenv("GITHUB_USERNAME")
		githubToken := os.Getenv("GITHUB_TOKEN")
		githubConfigured := githubUser != "" && githubToken != ""

		if useAI {
			fmt.Println("🧠 Asking the local Ollama model to write the README...")
			readme, err := generateReadme(context.Background(), data, sortedKeys(scaffold.App))
			if err != nil {
				fmt.Println("⚠️ AI README skipped:", err)
			} else {
				scaffold.App["README.md"] = readme
			}
		}

		if dryRun || planOnly {
			printPlan(scaffold, projectName, lang, database, githubConfigured)
			return
		}

		fmt.Printf("🚀 DevForge AI is building '%s' (%s)...\n", projectName, lang)

		// Local layout: <name>/ is the app repo, <name>/manifests/ is the manifest repo.
		if err := writeFiles(projectName, scaffold.App); err != nil {
			fmt.Println("❌ Error writing app files:", err)
			os.Exit(1)
		}
		if err := writeFiles(filepath.Join(projectName, "manifests"), scaffold.Manifests); err != nil {
			fmt.Println("❌ Error writing manifests:", err)
			os.Exit(1)
		}
		fmt.Printf("✅ Success! %s has been scaffolded at ./%s\n", projectName, projectName)
		fmt.Println("📈 Included: /health, /ready, /metrics, tests, ServiceMonitor, alert rules, Grafana dashboard, HPA, PDB, Kustomize dev/staging overlays and a CI pipeline with tests, gitleaks and Trivy")

		entry := CatalogEntry{
			Name: projectName, Language: lang, Database: database,
			Path: "./" + projectName, CreatedAt: time.Now().UTC(),
		}

		if !githubConfigured {
			fmt.Println("⚠️ GITHUB_USERNAME or GITHUB_TOKEN environment variables not set. Skipping GitHub upload.")
		} else {
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
			entry.AppRepo = fmt.Sprintf("https://github.com/%s/%s", githubUser, appRepo)
			entry.ManifestRepo = fmt.Sprintf("https://github.com/%s/%s", githubUser, manifestRepo)
			fmt.Printf("✅ GitOps Repositories created! Check https://github.com/%s\n", githubUser)
		}

		if err := recordService(entry); err != nil {
			fmt.Println("⚠️ Could not update the service catalog:", err)
		}
	},
}

func init() {
	// Register this command with the main DevForge CLI
	rootCmd.AddCommand(createCmd)

	// Define the flags (e.g., --name, --lang, --db) and their default values
	createCmd.Flags().StringVarP(&projectName, "name", "n", "my-service", "Name of the microservice")
	createCmd.Flags().StringVarP(&language, "lang", "l", "go", "Language template: go, python (FastAPI) or node")
	createCmd.Flags().StringVarP(&database, "db", "d", "postgres", "Database type (e.g., postgres, redis)")
	createCmd.Flags().Float64Var(&slo, "slo", DefaultSLO, "Availability target in percent; generates error-budget burn-rate alerts (90 to 99.999)")
	createCmd.Flags().StringVar(&owner, "owner", "platform-team", "Team that owns the service (Backstage catalog-info.yaml)")
	createCmd.Flags().StringVar(&manifestURL, "manifest-repo-url", "", "Git URL Argo CD pulls manifests from (default: the devforge-manifests-<name> repo on GitHub)")
	createCmd.Flags().BoolVar(&operator, "prometheus-operator", false, "Also generate ServiceMonitor and PrometheusRule resources (needs the Prometheus Operator CRDs in the cluster)")
	createCmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would be created without writing anything")
	createCmd.Flags().BoolVar(&planOnly, "plan", false, "Alias for --dry-run")
	createCmd.Flags().BoolVar(&useAI, "ai", false, "Also generate a README with the local Ollama model (OLLAMA_URL, OLLAMA_MODEL)")
}
