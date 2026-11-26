package cache

import (
	"strings"
	"testing"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
)

func TestKeyChangesWhenWorkloadEvidenceChanges(t *testing.T) {
	a := analyzer.Finding{ID: "prod/api", Summary: "crash", Status: "CrashLoopBackOff", Evidence: []analyzer.Evidence{{Label: "Container command", Value: "old command"}}}
	b := a
	b.Evidence = []analyzer.Evidence{{Label: "Container command", Value: "new command"}}
	if Key(a) == Key(b) {
		t.Fatal("must not reuse a remediation after its workload evidence changes")
	}
}

func TestKeyIncludesModelAndPrompt(t *testing.T) {
	f := analyzer.Finding{ID: "prod/api"}
	if Key(f, "gemini", "old-model", "prompt") == Key(f, "gemini", "new-model", "prompt") || Key(f, "gemini", "model", "old prompt") == Key(f, "gemini", "model", "new prompt") {
		t.Fatal("must not reuse AI across model or prompt changes")
	}
}

func TestAIKeySeparatesRequestModesAndExcludesURLSecrets(t *testing.T) {
	finding := analyzer.Finding{ID: "f-1", Summary: "pod failed"}
	key := AIKey(finding, "openai", "openai-chat-completions", "gpt-4o-mini", "https://user:pass@example.test/v1?token=do-not-cache", "sre", "json")
	otherProtocol := AIKey(finding, "openai", "openai-responses", "gpt-4o-mini", "https://example.test/v1", "sre", "json")
	otherMode := AIKey(finding, "openai", "openai-chat-completions", "gpt-4o-mini", "https://example.test", "sre", "prompt-json")
	if key == otherProtocol || key == otherMode {
		t.Fatal("AI cache key did not separate protocol/output mode")
	}
	if strings.Contains(key, "do-not-cache") || strings.Contains(key, "pass") || strings.Contains(key, "token") {
		t.Fatal("cache key exposed endpoint credentials")
	}
}
