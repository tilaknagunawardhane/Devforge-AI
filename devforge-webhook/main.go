package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
)

type AlertPayload struct {
	Alerts []struct {
		Labels map[string]string `json:"labels"`
	} `json:"alerts"`
}

type OllamaRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
	Stream bool   `json:"stream"`
}

type OllamaResponse struct {
	Response string `json:"response"`
}

func sendToSlack(report string) {
	slackURL := os.Getenv("SLACK_WEBHOOK_URL")
	if slackURL == "" {
		fmt.Println("⚠️ SLACK_WEBHOOK_URL not set, skipping Slack notification.")
		return
	}

	payload := map[string]string{"text": report}
	jsonData, _ := json.Marshal(payload)
	http.Post(slackURL, "application/json", bytes.NewBuffer(jsonData))
}

func generateRCA(podName string) {
	fmt.Println("🧠 Forwarding logs to Llama 3 for Root Cause Analysis...")

	// Simulated logs retrieved from our "log aggregator" showing the exact moment of failure
	logs := `[10:04:12] INFO: System execution algorithm started.
[10:04:15] WARN: External API latency detected (3000ms).
[10:04:20] ERROR: Timeout mechanism triggered but failed to gracefully close connections.
[10:04:28] FATAL: Process hung due to unhandled timeout state. Out of service.
[10:04:45] SYSTEM: Pod violently terminated by chaos-engine.`

	// The AI prompt specifically targets the out-of-service breakdown metrics
	prompt := fmt.Sprintf(`You are a Senior Site Reliability Engineer AI.
The pod %s just crashed. Here is the final log trace:
%s

Generate a Root Cause Analysis (RCA) report for a Slack channel.
You must calculate the out-of-service time breakdowns. Explicitly identify exactly how much downtime (in seconds) was caused by the timeout mechanism failing to handle the hung state versus the time the pod sat dead before the chaos script termination hit it.
Keep the formatting professional, use Slack-friendly markdown (bolding with *asterisks*), and use emojis. Keep it under 150 words.`, podName, logs)

	reqBody := OllamaRequest{
		Model:  "llama3",
		Prompt: prompt,
		Stream: false,
	}

	jsonData, _ := json.Marshal(reqBody)
	resp, err := http.Post("http://host.docker.internal:11434/api/generate", "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		fmt.Println("❌ Failed to contact Ollama:", err)
		return
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	var ollamaResp OllamaResponse
	json.Unmarshal(bodyBytes, &ollamaResp)

	fmt.Println("✅ RCA Generated! Pushing to Slack...")
	sendToSlack(ollamaResp.Response)
}

func alertHandler(w http.ResponseWriter, r *http.Request) {
	var payload AlertPayload
	json.NewDecoder(r.Body).Decode(&payload)

	for _, alert := range payload.Alerts {
		podName := alert.Labels["pod"]
		if podName != "" {
			fmt.Printf("\n🚨 ALERT RECEIVED for pod: %s\n", podName)
			// Run the AI analysis in a separate goroutine so we immediately reply 200 OK to Prometheus
			go generateRCA(podName)
		}
	}
	w.WriteHeader(http.StatusOK)
}

func main() {
	http.HandleFunc("/alert", alertHandler)
	fmt.Println("🎧 DevForge AI Webhook listening on port 9090...")
	http.ListenAndServe(":9090", nil)
}