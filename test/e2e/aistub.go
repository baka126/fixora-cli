//go:build e2e

package e2e

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// aiStub is an OpenAI-shaped server that records every request body it
// receives. Recording is the point: it lets a test assert that a secret never
// crossed the network boundary, which no black-box output check can prove.
type aiStub struct {
	URL string

	mu       sync.Mutex
	received []string
}

// newAIStub starts a stub serving a fixed AIResult. patchYAML may be empty.
// The response shape matches what internal/ai/openai.go parses:
// choices[0].message.content holding JSON that unmarshals into analyzer.AIResult.
func newAIStub(t *testing.T, patchYAML string) *aiStub {
	t.Helper()
	s := &aiStub{}

	content, err := json.Marshal(map[string]any{
		"summary":        "stubbed analysis",
		"rootCause":      "stubbed root cause",
		"recommendedFix": "stubbed fix",
		"patchYAML":      patchYAML,
		"confidence":     50,
	})
	if err != nil {
		t.Fatalf("marshal stub content: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.received = append(s.received, string(body))
		s.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"message": map[string]any{
					"role":    "assistant",
					"content": string(content),
				},
			}},
		})
	}))
	t.Cleanup(srv.Close)

	s.URL = srv.URL
	return s
}

// Received returns every request body the stub was sent.
func (s *aiStub) Received() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.received...)
}

// Env returns the environment that points fixora at this stub.
func (s *aiStub) Env() []string {
	return []string{
		"FIXORA_AI_PROVIDER=openai",
		"FIXORA_AI_BASE_URL=" + s.URL,
		"FIXORA_AI_API_KEY=e2e",
		"FIXORA_AI_MODEL=stub-model",
	}
}
