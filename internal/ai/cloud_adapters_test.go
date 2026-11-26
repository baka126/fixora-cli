package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"golang.org/x/oauth2"
)

type fakeBedrockConverse struct {
	input  *bedrockruntime.ConverseInput
	output *bedrockruntime.ConverseOutput
	err    error
}

type staticOAuthTokenSource struct{ token *oauth2.Token }

func (s staticOAuthTokenSource) Token() (*oauth2.Token, error) { return s.token, nil }
func staticTokenSource(value string) oauth2.TokenSource {
	return staticOAuthTokenSource{token: &oauth2.Token{AccessToken: value, TokenType: "Bearer"}}
}

func (f *fakeBedrockConverse) Converse(_ context.Context, input *bedrockruntime.ConverseInput, _ ...func(*bedrockruntime.Options)) (*bedrockruntime.ConverseOutput, error) {
	f.input = input
	return f.output, f.err
}

func TestBedrockAdapterUsesConverseAndParsesValidatedText(t *testing.T) {
	message := types.ConverseOutputMemberMessage{Value: types.Message{Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: validAIResultJSON}}}}
	fake := &fakeBedrockConverse{output: &bedrockruntime.ConverseOutput{Output: &message, StopReason: types.StopReasonEndTurn}}
	spec, _ := ResolveProvider("amazonbedrockconverse")
	result, err := NewBedrockConverseAdapter(fake).Explain(context.Background(), Request{Provider: spec, Model: "us.anthropic.claude-test", SystemPrompt: "system", UserPrompt: "finding"})
	if err != nil || result == nil || result.Summary != "summary" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if fake.input == nil || fake.input.ModelId == nil || *fake.input.ModelId != "us.anthropic.claude-test" || len(fake.input.System) != 1 {
		t.Fatalf("Bedrock request not populated: %#v", fake.input)
	}
}

func TestBedrockProbeConfigDisablesSDKRetries(t *testing.T) {
	cfg, err := bedrockAWSConfig(context.Background(), "us-east-1", true)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Retryer().(interface{ MaxAttempts() int }).MaxAttempts(); got != 1 {
		t.Fatalf("probe retry attempts = %d, want 1", got)
	}
}

type fakeBedrockInvoke struct {
	input  *bedrockruntime.InvokeModelInput
	output *bedrockruntime.InvokeModelOutput
}

func (f *fakeBedrockInvoke) InvokeModel(_ context.Context, input *bedrockruntime.InvokeModelInput, _ ...func(*bedrockruntime.Options)) (*bedrockruntime.InvokeModelOutput, error) {
	f.input = input
	return f.output, nil
}

func TestBedrockInvokeAdapterUsesAnthropicEnvelopeAndValidatesOutput(t *testing.T) {
	fake := &fakeBedrockInvoke{output: &bedrockruntime.InvokeModelOutput{Body: []byte(`{"stop_reason":"end_turn","content":[{"type":"text","text":` + mustJSON(validAIResultJSON) + `}]}`)}}
	spec, _ := ResolveProvider("amazonbedrock")
	result, err := NewBedrockInvokeAdapter(fake).Explain(context.Background(), Request{Provider: spec, Model: "global.anthropic.claude-test", SystemPrompt: "system", UserPrompt: "finding"})
	if err != nil || result == nil || result.Summary != "summary" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if fake.input == nil || aws.ToString(fake.input.ModelId) != "global.anthropic.claude-test" || !strings.Contains(string(fake.input.Body), "anthropic_version") {
		t.Fatalf("request=%#v", fake.input)
	}
}

func mustJSON(value string) string { data, _ := json.Marshal(value); return string(data) }

func TestVertexAdapterUsesProjectLocationBearerAndGenerateContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/v1/projects/project-a/locations/us-central1/publishers/google/models/gemini-test:generateContent") || r.Header.Get("Authorization") != "Bearer vertex-token" {
			t.Errorf("path/auth = %q/%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		fmt.Fprintf(w, `{"candidates":[{"finishReason":"STOP","content":{"parts":[{"text":%q}]}}]}`, validAIResultJSON)
	}))
	defer server.Close()
	spec, _ := ResolveProvider("googlevertexai")
	spec.EndpointOptions = map[string]string{"project": "project-a", "location": "us-central1"}
	adapter := NewVertexAdapter(server.Client(), staticTokenSource("vertex-token"))
	result, err := adapter.Explain(context.Background(), Request{Provider: spec, Model: "gemini-test", BaseURL: server.URL})
	if err != nil || result == nil || result.Summary != "summary" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestVertexCredentialDiscoveryHonorsProbeContext(t *testing.T) {
	started := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-r.Context().Done()
	}))
	defer server.Close()
	credentials := `{"type":"external_account","audience":"//iam.googleapis.com/projects/123/locations/global/workloadIdentityPools/pool/providers/provider","subject_token_type":"urn:ietf:params:oauth:token-type:jwt","token_url":"https://sts.googleapis.com/v1/token","credential_source":{"url":"` + server.URL + `"}}`
	credentialFile := filepath.Join(t.TempDir(), "external-account.json")
	if err := os.WriteFile(credentialFile, []byte(credentials), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", credentialFile)

	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
	defer cancel()
	spec, _ := ResolveProvider("googlevertexai")
	spec.EndpointOptions = map[string]string{"project": "project-a", "location": "us-central1"}
	adapter := newVertexAdapter(ctx, http.DefaultClient, nil)
	_, err := adapter.Explain(ctx, Request{Provider: spec, Model: "gemini-test", BaseURL: "https://example.test"})
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("credential source request was not started")
	}
	if err == nil {
		t.Fatal("expected credential request cancellation")
	}
}
