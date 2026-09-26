package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

func TestParseAIContentEnforcesContract(t *testing.T) {
	if res, _ := parseAIContent("not json at all"); res == nil || !res.Unstructured {
		t.Fatalf("malformed content must be Unstructured, got %#v", res)
	}
	if res, _ := parseAIContent("{}"); res == nil || !res.Unstructured {
		t.Fatalf("empty object must be Unstructured (no required fields), got %#v", res)
	}
	res, err := parseAIContent(`{"summary":"s","rootCause":"r","recommendedFix":"f"}`)
	if err != nil || res == nil || res.Unstructured {
		t.Fatalf("valid contract content must be structured, got %#v err=%v", res, err)
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

func TestExplainRetriesTransientHTTPFailure(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(w, `{"error":"overloaded"}`)
			return
		}
		fmt.Fprint(w, `{"candidates":[{"content":{"parts":[{"text":"{\"summary\":\"ok\",\"rootCause\":\"known\",\"recommendedFix\":\"inspect\"}"}]}}]}`)
	}))
	defer srv.Close()
	c := Client{Provider: "gemini", BaseURL: srv.URL, Model: "test", HTTP: srv.Client()}
	result, err := c.Explain(context.Background(), analyzer.Finding{Summary: "pod failed"})
	if err != nil || result == nil || result.Summary != "ok" || calls != 3 {
		t.Fatalf("result=%#v err=%v calls=%d", result, err, calls)
	}
}

func TestExplainDoesNotRetryPermanentHTTPFailure(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(http.StatusBadRequest) }))
	defer srv.Close()
	c := Client{Provider: "gemini", BaseURL: srv.URL, Model: "test", HTTP: srv.Client()}
	_, err := c.Explain(context.Background(), analyzer.Finding{Summary: "pod failed"})
	if err == nil || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

func TestExplainOpenAIRetriesProviderErrorBody(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(w, `{"error":{"message":"busy"}}`)
			return
		}
		fmt.Fprint(w, `{"choices":[{"message":{"content":"{\"summary\":\"ok\",\"rootCause\":\"known\",\"recommendedFix\":\"inspect\"}"}}]}`)
	}))
	defer srv.Close()
	c := Client{Provider: "openai", BaseURL: srv.URL, Model: "test", HTTP: srv.Client()}
	result, err := c.Explain(context.Background(), analyzer.Finding{Summary: "pod failed"})
	if err != nil || result == nil || result.Summary != "ok" || calls != 2 {
		t.Fatalf("result=%#v err=%v calls=%d", result, err, calls)
	}
}

func TestExplainStopsRetriesOnContextCancel(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(http.StatusServiceUnavailable) }))
	defer srv.Close()
	c := Client{Provider: "gemini", BaseURL: srv.URL, Model: "test", HTTP: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := c.Explain(ctx, analyzer.Finding{Summary: "pod failed"})
	if !errors.Is(err, context.DeadlineExceeded) || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}
