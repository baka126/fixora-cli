package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const validAIResultJSON = `{"summary":"summary","rootCause":"root cause","recommendedFix":"recommended fix","patchYAML":"","strategy":"runtime","confidence":85,"analyzers":[],"commands":[],"warnings":[]}`

func TestOpenAIChatAdapterUsesStrictSchemaWhenSupported(t *testing.T) {
	var received map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("request path/auth = %q/%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Error(err)
		}
		fmt.Fprintf(w, `{"choices":[{"message":{"content":%q}}]}`, validAIResultJSON)
	}))
	defer server.Close()

	spec, _ := ResolveProvider("openai")
	result, err := NewOpenAIChatAdapter(server.Client()).Explain(context.Background(), Request{
		Provider: spec, Model: "gpt-4o-mini", BaseURL: server.URL + "/v1", APIKey: "test-key",
		SystemPrompt: "system", UserPrompt: "finding", Contract: OutputContract{Schema: remediationSchema()},
	})
	if err != nil || result == nil || result.RootCause != "root cause" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	format, ok := received["response_format"].(map[string]any)
	schema, _ := format["json_schema"].(map[string]any)
	if !ok || format["type"] != "json_schema" || schema["strict"] != true {
		t.Fatalf("request did not use strict JSON schema: %#v", received["response_format"])
	}
}

func TestOpenAIResponsesAdapterParsesOutputText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" || r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("request path/auth = %q/%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		fmt.Fprintf(w, `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":%q}]}]}`, validAIResultJSON)
	}))
	defer server.Close()

	spec, _ := ResolveProvider("openai")
	spec.Protocol = ProtocolOpenAIResponses
	result, err := NewOpenAIResponsesAdapter(server.Client()).Explain(context.Background(), Request{
		Provider: spec, Model: "gpt-6-luna", BaseURL: server.URL + "/v1", APIKey: "test-key",
		SystemPrompt: "system", UserPrompt: "finding", Contract: OutputContract{Schema: remediationSchema()},
	})
	if err != nil || result == nil || result.Summary != "summary" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestOpenAIAdapterOmitsUnsupportedSamplingFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if _, ok := body["temperature"]; ok {
			t.Error("unsupported temperature field was sent")
		}
		if _, ok := body["max_tokens"]; ok {
			t.Error("unconfigured token limit was sent")
		}
		fmt.Fprintf(w, `{"choices":[{"message":{"content":%q}}]}`, validAIResultJSON)
	}))
	defer server.Close()
	spec, _ := ResolveProvider("groq")
	if _, err := NewOpenAIChatAdapter(server.Client()).Explain(context.Background(), Request{
		Provider: spec, Model: "provider-model", BaseURL: server.URL + "/v1", APIKey: "test-key",
		SystemPrompt: "system", UserPrompt: "finding",
	}); err != nil {
		t.Fatal(err)
	}
}

func TestOpenAIAdapterMapsProviderErrorSafely(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":{"message":"private prompt api_key=secret","code":"unsupported_parameter","param":"temperature"}}`)
	}))
	defer server.Close()
	spec, _ := ResolveProvider("openai")
	_, err := NewOpenAIChatAdapter(server.Client()).Explain(context.Background(), Request{Provider: spec, Model: "model", BaseURL: server.URL, APIKey: "secret"})
	if err == nil {
		t.Fatal("expected provider request failure")
	}
	summary := FailureSummary(err)
	if !strings.Contains(summary, "unsupported_parameter") || !strings.Contains(summary, "temperature") {
		t.Fatalf("safe provider details missing: %q", summary)
	}
	if strings.Contains(summary, "private prompt") || strings.Contains(summary, "secret") {
		t.Fatalf("provider error leaked sensitive content: %q", summary)
	}
}

func TestAzureOpenAIUsesConfiguredAPIVersion(t *testing.T) {
	var gotPath, gotKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotKey = r.URL.RequestURI(), r.Header.Get("api-key")
		fmt.Fprintf(w, `{"choices":[{"message":{"content":%q}}]}`, validAIResultJSON)
	}))
	defer server.Close()
	spec, _ := ResolveProvider("azureopenai")
	spec.EndpointOptions = map[string]string{"deployment": "fixora", "api-version": "2024-10-21"}
	if _, err := NewOpenAIChatAdapter(server.Client()).Explain(context.Background(), Request{
		Provider: spec, Model: "ignored-model", BaseURL: server.URL, APIKey: "azure-key",
		SystemPrompt: "system", UserPrompt: "finding",
	}); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/openai/deployments/fixora/chat/completions?api-version=2024-10-21" || gotKey != "azure-key" {
		t.Fatalf("request URI/auth = %q/%q", gotPath, gotKey)
	}
}
