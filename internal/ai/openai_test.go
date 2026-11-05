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

func TestExplainRejectsOversizedProviderResponse(t *testing.T) {
	large := strings.Repeat("x", 2<<20)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, large)
	}))
	defer server.Close()
	for _, provider := range []string{"openai", "azureopenai", "ollama", "anthropic", "gemini", "cohere", "huggingface"} {
		t.Run(provider, func(t *testing.T) {
			client := Client{Provider: provider, BaseURL: server.URL, APIKey: "test", Model: "test", HTTP: server.Client()}
			_, err := client.Explain(context.Background(), analyzer.Finding{Summary: "pod failed"})
			if err == nil || !strings.Contains(err.Error(), "response exceeds") {
				t.Fatalf("oversized %s response accepted or unclear error: %v", provider, err)
			}
		})
	}
}

func TestParseAIContentFailureCategories(t *testing.T) {
	for _, tc := range []struct{ content, reason string }{{"", "empty response"}, {"secret text", "invalid JSON"}, {"{}", "missing explanation fields"}} {
		res, err := parseAIContent(tc.content)
		if err != nil || res == nil || !res.Unstructured || !strings.Contains(strings.Join(res.Warnings, " "), tc.reason) {
			t.Fatalf("%q: %#v %v", tc.content, res, err)
		}
	}
}

func TestGeminiCombinesAnswerPartsAndSkipsThoughts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"candidates":[{"content":{"parts":[{"text":"private reasoning","thought":true},{"text":"{\"summary\":\"s\","},{"text":"\"rootCause\":\"r\",\"recommendedFix\":\"f\"}"}]}}]}`)
	}))
	defer srv.Close()
	c := Client{Provider: "gemini", BaseURL: srv.URL, Model: "test", HTTP: srv.Client()}
	res, err := c.Explain(context.Background(), analyzer.Finding{})
	if err != nil || res == nil || res.Unstructured || res.RootCause != "r" {
		t.Fatalf("must parse answer parts, not thoughts: %#v %v", res, err)
	}
}

func TestGeminiHonorsRetryAfterWithinRequestDeadline(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Retry-After", "3")
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	c := Client{Provider: "gemini", BaseURL: srv.URL, Model: "test", HTTP: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 1800*time.Millisecond)
	defer cancel()
	_, err := c.Explain(ctx, analyzer.Finding{})
	if !errors.Is(err, context.DeadlineExceeded) || calls != 1 {
		t.Fatalf("must respect server backoff without retrying early: calls=%d err=%v", calls, err)
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		header string
		delay  time.Duration
	}{
		{"2", 2 * time.Second}, {" 3 ", 3 * time.Second},
		{now.Add(5 * time.Second).Format(http.TimeFormat), 5 * time.Second},
		{now.Add(-time.Second).Format(http.TimeFormat), 0}, {"-1", 0}, {"invalid", 0}, {"", 0},
		{"9223372036854775807", time.Duration(1<<63 - 1)},
	} {
		if got := parseRetryAfter(tc.header, now); got != tc.delay {
			t.Fatalf("%q: got %v want %v", tc.header, got, tc.delay)
		}
	}
}

func TestGeminiRequestsTypedRemediationSchema(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		gen, _ := body["generationConfig"].(map[string]any)
		format, _ := gen["responseFormat"].(map[string]any)
		text, _ := format["text"].(map[string]any)
		schema, _ := text["schema"].(map[string]any)
		props, _ := schema["properties"].(map[string]any)
		confidence, _ := props["confidence"].(map[string]any)
		if text["mimeType"] != "APPLICATION_JSON" || confidence["type"] != "integer" {
			t.Errorf("missing typed JSON output: %#v", gen)
		}
		fmt.Fprint(w, `{"candidates":[{"content":{"parts":[{"text":"{\"summary\":\"s\",\"rootCause\":\"r\",\"recommendedFix\":\"f\",\"confidence\":90,\"patchYAML\":\"\"}"}]},"finishReason":"STOP"}]}`)
	}))
	defer srv.Close()
	c := Client{Provider: "gemini", BaseURL: srv.URL, Model: "gemini-3.8-flash", HTTP: srv.Client()}
	if res, err := c.Explain(context.Background(), analyzer.Finding{}); err != nil || res.Unstructured {
		t.Fatalf("%#v %v", res, err)
	}
}

func TestGeminiRejectsTruncatedRemediation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"candidates":[{"finishReason":"MAX_TOKENS","content":{"parts":[{"text":"{\"summary\":\"s\",\"rootCause\":\"r\",\"recommendedFix\":\"f\"}"}]}}]}`)
	}))
	defer srv.Close()
	c := Client{Provider: "gemini", BaseURL: srv.URL, Model: "test", HTTP: srv.Client()}
	res, err := c.Explain(context.Background(), analyzer.Finding{})
	if err == nil {
		t.Fatalf("truncated generation must not become a remediation: %#v", res)
	}
}

func TestJSONContractAllowsNetworkProbeRepairs(t *testing.T) {
	contract := jsonContract()
	for _, field := range []string{"readinessProbe", "livenessProbe", "startupProbe"} {
		if !strings.Contains(contract, field) {
			t.Fatalf("missing allowed probe field %s", field)
		}
	}
	if !strings.Contains(contract, "exec probes") {
		t.Fatal("exec probe commands must remain forbidden")
	}
}

func TestJSONContractRequestsDynamicEvidenceGroundedFilesystemPatch(t *testing.T) {
	contract := strings.ToLower(jsonContract())
	for _, required := range []string{
		"when logs prove a cache write is blocked by readonlyrootfilesystem",
		"workload-specific volume name and mount path",
		"existing volumes and mounts are preserved",
		"keep readonlyrootfilesystem enabled",
		"when no volumes or fsgroup exist and runasuser is explicit, set fsgroup to that uid",
		"do not leave patchyaml empty when this evidence supports a repair",
	} {
		if !strings.Contains(contract, required) {
			t.Fatalf("AI contract must require dynamic, evidence-grounded filesystem repairs; missing %q", required)
		}
	}
	if strings.Contains(contract, "fixora-cache") {
		t.Fatal("AI contract must not prescribe a hardcoded volume name")
	}
}

func TestExplainOpenAIRequestsStrictRemediationJSON(t *testing.T) {
	var got chatRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode OpenAI request: %v", err)
		}
		fmt.Fprint(w, `{"choices":[{"message":{"content":"{\"summary\":\"s\",\"rootCause\":\"r\",\"recommendedFix\":\"f\",\"patchYAML\":\"\",\"strategy\":\"runtime\",\"confidence\":70,\"analyzers\":[],\"commands\":[],\"warnings\":[]}"}}]}`)
	}))
	defer srv.Close()
	c := Client{Provider: "openai", BaseURL: srv.URL, Model: "gpt-6-luna", HTTP: srv.Client()}
	result, err := c.Explain(context.Background(), analyzer.Finding{Summary: "probe failing"})
	if err != nil || result == nil || result.RootCause != "r" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	format, ok := got.ResponseFormat["json_schema"].(map[string]any)
	if got.ResponseFormat["type"] != "json_schema" || !ok || format["strict"] != true {
		t.Fatalf("OpenAI request must enforce strict JSON Schema: %#v", got.ResponseFormat)
	}
	schema, ok := format["schema"].(map[string]any)
	if !ok || schema["additionalProperties"] != false {
		t.Fatalf("unexpected remediation schema: %#v", format["schema"])
	}
}

func TestExplainOpenAIGPT6OmitsUnsupportedTemperature(t *testing.T) {
	for _, tt := range []struct {
		model           string
		wantTemperature any
	}{
		{model: "gpt-6-luna", wantTemperature: nil},
		{model: "gpt-4o-mini", wantTemperature: float64(0.1)},
	} {
		t.Run(tt.model, func(t *testing.T) {
			var got map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
					t.Errorf("decode OpenAI request: %v", err)
				}
				fmt.Fprint(w, `{"choices":[{"message":{"content":"{\"summary\":\"s\",\"rootCause\":\"r\",\"recommendedFix\":\"f\",\"patchYAML\":\"\",\"strategy\":\"runtime\",\"confidence\":70,\"analyzers\":[],\"commands\":[],\"warnings\":[]}"}}]}`)
			}))
			defer srv.Close()
			c := Client{Provider: "openai", BaseURL: srv.URL, Model: tt.model, HTTP: srv.Client()}
			if _, err := c.Explain(context.Background(), analyzer.Finding{Summary: "probe failing"}); err != nil {
				t.Fatal(err)
			}
			gotTemperature, exists := got["temperature"]
			if tt.wantTemperature == nil {
				if exists {
					t.Fatalf("GPT-6 request must omit unsupported temperature parameter, got %#v", gotTemperature)
				}
			} else if !exists || gotTemperature != tt.wantTemperature {
				t.Fatalf("legacy model temperature = %#v, want %#v", gotTemperature, tt.wantTemperature)
			}
		})
	}
}
