package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCohereAdapterUsesV2ChatAndValidatesOutput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/chat" || r.Header.Get("Authorization") != "Bearer test" {
			t.Errorf("request path/auth = %q/%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		fmt.Fprintf(w, `{"finish_reason":"COMPLETE","message":{"content":[{"type":"text","text":%q}]}}`, validAIResultJSON)
	}))
	defer server.Close()
	spec, _ := ResolveProvider("cohere")
	result, err := NewCohereAdapter(server.Client()).Explain(context.Background(), Request{Provider: spec, Model: "command-test", BaseURL: server.URL, APIKey: "test", SystemPrompt: "system", UserPrompt: "finding"})
	if err != nil || result == nil || result.Summary != "summary" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestHuggingFaceAdapterUsesRouterChatAndValidatesOutput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer test" {
			t.Errorf("request path/auth = %q/%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		var request struct {
			Model    string `json:"model"`
			Messages []struct {
				Role string `json:"role"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if request.Model != "org/model" || len(request.Messages) != 2 {
			t.Errorf("chat request model/messages = %#v", request)
		}
		fmt.Fprintf(w, `{"choices":[{"finish_reason":"stop","message":{"content":%q}}]}`, validAIResultJSON)
	}))
	defer server.Close()
	spec, _ := ResolveProvider("huggingface")
	result, err := NewHuggingFaceAdapter(server.Client()).Explain(context.Background(), Request{Provider: spec, Model: "org/model", BaseURL: server.URL + "/v1", APIKey: "test", SystemPrompt: "system", UserPrompt: "finding"})
	if err != nil || result == nil || result.Summary != "summary" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}
