package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// Structs for the Ollama API Request/Response
type OllamaRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
	Stream bool   `json:"stream"`
	Format string `json:"format"` // Forces the AI to return JSON
}

type OllamaResponse struct {
	Response string `json:"response"`
}

// Struct for our expected AI output files
type ProjectFiles struct {
	MainCode   string `json:"main"`
	Dockerfile string `json:"dockerfile"`
	Yaml       string `json:"yaml"`
}

// Function to generate code using the local Llama 3 model
func generateCode(projectName, language, database string) (*ProjectFiles, error) {
	// The prompt strictly commands the AI to return a specific JSON structure
	prompt := fmt.Sprintf(`You are an expert DevOps engineer. Create a MINIMAL starter %s microservice named %s that connects to %s.
Keep all code as short as possible (under 50 lines total).
You must respond strictly with a JSON object. 
The values MUST be flat text strings containing the raw code with properly escaped newlines (\n). Do NOT use nested JSON objects.

Use EXACTLY this structure:
{
  "main": "// minimal code here",
  "dockerfile": "# minimal dockerfile here",
  "yaml": "# minimal yaml here"
}`, language, projectName, database)

	reqBody := OllamaRequest{
		Model:  "llama3",
		Prompt: prompt,
		Stream: false,
		Format: "json",
	}

	jsonData, _ := json.Marshal(reqBody)
	resp, err := http.Post("http://host.docker.internal:11434/api/generate", "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("failed to connect to Ollama: %v", err)
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)

	var ollamaResp OllamaResponse
	json.Unmarshal(bodyBytes, &ollamaResp)

	var files ProjectFiles
	err = json.Unmarshal([]byte(ollamaResp.Response), &files)
	if err != nil {
		// FIX 2: Print exactly what the AI outputted so we can see where it broke
		fmt.Println("\n--- DEBUG: RAW AI OUTPUT ---")
		fmt.Println(ollamaResp.Response)
		fmt.Println("----------------------------\n")
		return nil, fmt.Errorf("unexpected end of JSON input")
	}

	return &files, nil
}