package cmd

import (
	"fmt"
	"os"
	"github.com/spf13/cobra"
)

// These variables will store the inputs from the user's flags
var (
	projectName string
	language    string
	database    string
)

var createCmd = &cobra.Command{
	Use:   "create",
	Short: "Scaffold a new microservice project",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("🚀 DevForge AI is building '%s'...\n", projectName)
		fmt.Printf("⏳ Status: AI Agent is writing the code. This takes about 10-30 seconds...\n")

		// 1. Call the AI
		files, err := generateCode(projectName, language, database)
		if err != nil {
			fmt.Println("❌ Error:", err)
			return
		}

		// 2. Create the project folder
		// Because of our Docker volume mount, this folder will appear in Windows!
		os.MkdirAll(projectName, 0755)

		// 3. Determine the main file extension
		mainFileName := "main.go"
		if language == "python" {
			mainFileName = "main.py"
		} else if language == "node" || language == "javascript" {
			mainFileName = "index.js"
		}

		// 4. Save the files to disk
		os.WriteFile(projectName+"/"+mainFileName, []byte(files.MainCode), 0644)
		os.WriteFile(projectName+"/Dockerfile", []byte(files.Dockerfile), 0644)
		os.WriteFile(projectName+"/deployment.yaml", []byte(files.Yaml), 0644)

		fmt.Printf("✅ Success! %s has been scaffolded at ./%s\n", projectName, projectName)
		
		// 5. Generate the CI/CD Pipeline (Hardcoded for reliability)
		fmt.Println("⚙️ Injecting GitHub Actions CI/CD Pipeline...")
		
		ciYaml := fmt.Sprintf(`name: Build and Push
			on:
			push:
				branches: [ "main" ]

			jobs:
				build:
					runs-on: ubuntu-latest
					steps:
						- name: Checkout app code
							uses: actions/checkout@v3

						- name: Login to Docker Hub
							uses: docker/login-action@v2
							with:
							username: ${{ secrets.DOCKER_USERNAME }}
							password: ${{ secrets.DOCKER_TOKEN }}

						- name: Build and push Docker image
							uses: docker/build-push-action@v4
							with:
							context: .
							push: true
							tags: ${{ secrets.DOCKER_USERNAME }}/%[1]s:${{ github.sha }}

						- name: Checkout Manifest Repo
							uses: actions/checkout@v3
							with:
							# Clones the separate GitOps manifest repository
							repository: ${{ github.repository_owner }}/devforge-manifests-%[1]s
										token: ${{ secrets.PAT_TOKEN }}
										path: manifest-repo

						- name: Update YAML image tag and Push
							run: |
								cd manifest-repo
								# Use sed to find the old image line and replace it with the newly built Docker tag
								sed -i 's|image: .*|image: ${{ secrets.DOCKER_USERNAME }}/%[1]s:${{ github.sha }}|g' deployment.yaml
			
							# Commit the change back to the manifest repo
							git config user.name "DevForge GitOps Bot"
							git config user.email "bot@devforge.ai"
							git add deployment.yaml
							git commit -m "GitOps sync: Update image to ${{ github.sha }}"
							git push
						`, projectName)

		// Create the hidden GitHub Actions folders locally
		os.MkdirAll(projectName+"/.github/workflows", 0755)
		os.WriteFile(projectName+"/.github/workflows/ci.yml", []byte(ciYaml), 0644)
		// --- NEW GITHUB LOGIC ---
		githubUser := os.Getenv("GITHUB_USERNAME")
		githubToken := os.Getenv("GITHUB_TOKEN")

		if githubUser != "" && githubToken != "" {
			appRepo := "devforge-app-" + projectName
			manifestRepo := "devforge-manifests-" + projectName

			fmt.Println("🌐 Creating dual GitHub Repositories for GitOps...")
			createGitHubRepo(appRepo)
			createGitHubRepo(manifestRepo)

			fmt.Println("⬆️ Uploading files to GitHub APIs...")
			// Put code in the App Repo
			uploadFileToGitHub(appRepo, mainFileName, files.MainCode)
			uploadFileToGitHub(appRepo, "Dockerfile", files.Dockerfile)
			uploadFileToGitHub(appRepo, ".github/workflows/ci.yml", ciYaml)
			
			// Put YAML in the Manifest Repo
			uploadFileToGitHub(manifestRepo, "deployment.yaml", files.Yaml)
			
			fmt.Printf("✅ GitOps Repositories created! Check https://github.com/%s\n", githubUser)
		} else {
			fmt.Println("⚠️ GITHUB_USERNAME or GITHUB_TOKEN environment variables not set. Skipping GitHub upload.")
		}
	},
}

func init() {
	// Register this command with the main DevForge CLI
	rootCmd.AddCommand(createCmd)

	// Define the flags (e.g., --name, --lang, --db) and their default values
	createCmd.Flags().StringVarP(&projectName, "name", "n", "my-service", "Name of the microservice")
	createCmd.Flags().StringVarP(&language, "lang", "l", "go", "Programming language (e.g., go, python)")
	createCmd.Flags().StringVarP(&database, "db", "d", "postgres", "Database type (e.g., postgres, redis)")
}