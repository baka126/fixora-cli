package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
)

func TestExplainGeminiParsesStructuredContent(t *testing.T) {
	var gotPath, gotKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotKey = r.Header.Get("x-goog-api-key")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"candidates": []map[string]any{{
				"content": map[string]any{
					"parts": []map[string]string{{
						"text": `{"summary":"s","rootCause":"r","recommendedFix":"f","commands":["kubectl get pods"],"warnings":["verify"]}`,
					}},
				},
			}},
		})
	}))
	defer server.Close()

	client := Client{
		Provider: "gemini",
		BaseURL:  server.URL,
		APIKey:   "test key",
		Model:    "gemini-test",
		HTTP:     server.Client(),
	}
	result, err := client.Explain(context.Background(), analyzer.Finding{Summary: "pod failed"})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/models/gemini-test:generateContent" || gotKey != "test key" {
		t.Fatalf("unexpected Gemini request path/key: %q %q", gotPath, gotKey)
	}
	if result.Summary != "s" || result.RootCause != "r" || len(result.Commands) != 1 {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestExplainOpenAIMarksNonJSONUnstructured(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"choices":[{"message":{"content":"sorry, I cannot help"}}]}`)
	}))
	defer srv.Close()
	c := Client{Provider: "openai", BaseURL: srv.URL, Model: "gpt-x", HTTP: srv.Client()}
	res, err := c.explainOpenAI(context.Background(), "payload")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Unstructured {
		t.Fatalf("expected Unstructured=true for non-JSON content")
	}
}

func TestExplainAzureOpenAIUsesDeploymentEndpoint(t *testing.T) {
	var gotPath, gotKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotKey = r.Header.Get("api-key")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message": map[string]string{"content": `{"summary":"ok","rootCause":"known","recommendedFix":"fix"}`},
			}},
		})
	}))
	defer server.Close()

	client := Client{
		Provider: "azureopenai",
		BaseURL:  server.URL + "/openai/deployments/fixora",
		APIKey:   "azure-key",
		Model:    "ignored-by-deployment",
		HTTP:     server.Client(),
	}
	result, err := client.Explain(context.Background(), analyzer.Finding{Summary: "pod failed"})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/openai/deployments/fixora/chat/completions" || gotKey != "azure-key" {
		t.Fatalf("unexpected Azure request path/key: %q %q", gotPath, gotKey)
	}
	if result.Summary != "ok" || !strings.Contains(result.RecommendedFix, "fix") {
		t.Fatalf("unexpected result: %#v", result)
	}
}
