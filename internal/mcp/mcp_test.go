package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/config"
	"github.com/fixora/kubectl-fixora/internal/kube"
	"github.com/fixora/kubectl-fixora/internal/shadow"
	"io"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func connectTestClient(t *testing.T, version string) *sdk.ClientSession {
	return connectServerTestClient(t, version, Server{})
}

func connectServerTestClient(t *testing.T, version string, configured Server) *sdk.ClientSession {
	t.Helper()
	serverTransport, clientTransport := sdk.NewInMemoryTransports()
	server := configured.newSDKServer()
	serverSession, err := server.Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	client := sdk.NewClient(&sdk.Implementation{Name: "fixora-test", Version: "1.0"}, nil)
	session, err := client.Connect(context.Background(), clientTransport, &sdk.ClientSessionOptions{ProtocolVersion: version})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func TestSDKProtocolToolsAndResources(t *testing.T) {
	for _, version := range []string{"2025-11-25", "2026-07-28"} {
		t.Run(version, func(t *testing.T) {
			session := connectTestClient(t, version)
			if got := session.InitializeResult().ProtocolVersion; got != version {
				t.Fatalf("negotiated %q, want %q", got, version)
			}
			listed, err := session.ListTools(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, tool := range listed.Tools {
				if tool.Name == "analyze" {
					found = true
					if tool.InputSchema == nil || tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
						t.Fatalf("incomplete tool metadata: %#v", tool)
					}
				}
			}
			if !found {
				t.Fatal("analyze tool missing")
			}
			call, err := session.CallTool(context.Background(), &sdk.CallToolParams{Name: "config", Arguments: map[string]any{}})
			if err != nil || call == nil || call.IsError || len(call.Content) == 0 || call.StructuredContent == nil {
				t.Fatalf("call=%#v err=%v", call, err)
			}
			resources, err := session.ListResources(context.Background(), nil)
			if err != nil || len(resources.Resources) == 0 {
				t.Fatalf("resources=%#v err=%v", resources, err)
			}
			resource, err := session.ReadResource(context.Background(), &sdk.ReadResourceParams{URI: "fixora://config"})
			if err != nil || len(resource.Contents) != 1 {
				t.Fatalf("resource=%#v err=%v", resource, err)
			}
			prompts, err := session.ListPrompts(context.Background(), nil)
			if err != nil || len(prompts.Prompts) == 0 {
				t.Fatalf("prompts=%#v err=%v", prompts, err)
			}
			prompt, err := session.GetPrompt(context.Background(), &sdk.GetPromptParams{Name: "troubleshoot-pod", Arguments: map[string]string{"pod": "api"}})
			if err != nil || len(prompt.Messages) == 0 {
				t.Fatalf("prompt=%#v err=%v", prompt, err)
			}
		})
	}
}

type fakeReader struct {
	resource map[string]any
	items    []map[string]any
	events   []kube.Event
	logs     string
	err      error
	calls    int
}

func (f *fakeReader) GetPods(context.Context, string, bool) (kube.PodList, error) {
	return kube.PodList{}, f.err
}
func (f *fakeReader) GetPod(context.Context, string, string) (kube.Pod, error) {
	return kube.Pod{}, f.err
}
func (f *fakeReader) GetResource(context.Context, string, string) (map[string]any, error) {
	f.calls++
	return f.resource, f.err
}
func (f *fakeReader) GetResourceItems(context.Context, string, bool, string) ([]map[string]any, error) {
	f.calls++
	return f.items, f.err
}
func (f *fakeReader) GetEvents(context.Context, string, string) ([]kube.Event, error) {
	f.calls++
	return f.events, f.err
}
func (f *fakeReader) GetNodes(context.Context) ([]kube.Node, error) { return nil, f.err }
func (f *fakeReader) Logs(context.Context, string, string, bool) (string, error) {
	f.calls++
	return f.logs, f.err
}
func (f *fakeReader) Run(context.Context, ...string) ([]byte, error) { return nil, f.err }

func callTestTool(t *testing.T, session *sdk.ClientSession, name string, args map[string]any) *sdk.CallToolResult {
	t.Helper()
	result, err := session.CallTool(context.Background(), &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func resultText(result *sdk.CallToolResult) string {
	if len(result.Content) == 0 {
		return ""
	}
	text, ok := result.Content[0].(*sdk.TextContent)
	if !ok {
		return ""
	}
	return text.Text
}

func TestMCPRawReadsAreScopedAndRedacted(t *testing.T) {
	reader := &fakeReader{resource: map[string]any{"kind": "Pod", "metadata": map[string]any{"name": "api", "namespace": "prod"}, "spec": map[string]any{"containers": []any{map[string]any{"name": "api", "env": []any{map[string]any{"name": "PASSWORD", "value": "hunter2"}}}}}}, logs: "password=hunter2", events: []kube.Event{{Metadata: kube.ObjectMeta{Namespace: "prod"}, Message: "password=hunter2"}}}
	session := connectServerTestClient(t, "2025-11-25", Server{Reader: reader, AnalyzerOpt: analyzer.Options{Namespace: "prod", Redact: true}})
	for _, resource := range []string{"secret/db", "configmap/app", "widgets.example.com/x"} {
		res := callTestTool(t, session, "get-resource", map[string]any{"resource": resource})
		if !res.IsError {
			t.Fatalf("%s should be denied: %s", resource, resultText(res))
		}
	}
	if reader.calls != 0 {
		t.Fatalf("denied resources hit reader %d times", reader.calls)
	}
	deniedList := callTestTool(t, session, "list-resources", map[string]any{"type": "secrets"})
	if !deniedList.IsError || reader.calls != 0 {
		t.Fatalf("Secret list bypassed guard: %#v calls=%d", deniedList, reader.calls)
	}
	res := callTestTool(t, session, "get-resource", map[string]any{"resource": "pod/api"})
	if res.IsError || strings.Contains(resultText(res), "hunter2") {
		t.Fatalf("resource not redacted: %s", resultText(res))
	}
	res = callTestTool(t, session, "get-logs", map[string]any{"pod": "api", "namespace": "other"})
	if !res.IsError {
		t.Fatal("cross-namespace logs allowed")
	}
	res = callTestTool(t, session, "get-logs", map[string]any{"pod": "api"})
	if res.IsError || strings.Contains(resultText(res), "hunter2") {
		t.Fatalf("logs not redacted: %s", resultText(res))
	}
	res = callTestTool(t, session, "list-events", map[string]any{})
	if res.IsError || strings.Contains(resultText(res), "hunter2") {
		t.Fatalf("events not redacted: %s", resultText(res))
	}
	reader.err = errors.New("forbidden")
	res = callTestTool(t, session, "get-logs", map[string]any{"pod": "api"})
	if !res.IsError || !strings.Contains(resultText(res), "forbidden") {
		t.Fatalf("read error hidden: %#v", res)
	}
}

func TestMCPShadowRequiresConfirmationAndEligiblePlan(t *testing.T) {
	typedCalls := 0
	runnerCalls := 0
	server := Server{
		EnableShadow: true,
		analyzeResource: func(context.Context, string) (analyzer.Finding, error) {
			return analyzer.Finding{Namespace: "prod", ResourceKind: "Deployment", ResourceName: "api", Status: "CrashLoopBackOff"}, nil
		},
		newTypedClient: func() (*kube.TypedClient, error) { typedCalls++; return &kube.TypedClient{}, nil },
		runShadow: func(context.Context, *kube.TypedClient, shadow.Request) (shadow.Result, error) {
			runnerCalls++
			return shadow.Result{}, nil
		},
	}
	session := connectServerTestClient(t, "2025-11-25", server)
	args := map[string]any{"resource": "deployment/api", "confirm": false, "container": "api", "image": "ghcr.io/acme/api:v2"}
	result := callTestTool(t, session, "shadow-verify", args)
	if !result.IsError {
		t.Fatal("missing confirmation accepted")
	}
	args["confirm"] = true
	result = callTestTool(t, session, "shadow-verify", args)
	if !result.IsError {
		t.Fatal("review-only plan accepted")
	}
	if typedCalls != 0 || runnerCalls != 0 {
		t.Fatalf("side effects before gate: typed=%d runner=%d", typedCalls, runnerCalls)
	}
}

func TestMCPShadowRunsIsolatedAndReturnsMinimalSummary(t *testing.T) {
	typedCalls := 0
	runnerCalls := 0
	server := Server{
		EnableShadow: true,
		analyzeResource: func(context.Context, string) (analyzer.Finding, error) {
			return analyzer.Finding{Namespace: "prod", ResourceKind: "Deployment", ResourceName: "api", Status: "ImagePullBackOff"}, nil
		},
		newTypedClient: func() (*kube.TypedClient, error) { typedCalls++; return &kube.TypedClient{}, nil },
		runShadow: func(_ context.Context, _ *kube.TypedClient, req shadow.Request) (shadow.Result, error) {
			runnerCalls++
			if req.Keep || req.Retries != 0 || req.AI != nil || req.Delivery != shadow.DeliveryPatch || req.Namespace != "prod" || req.Egress != "deny" {
				t.Fatalf("unsafe request: %#v", req)
			}
			return shadow.Result{Verified: true, Parity: 95, Resource: req.Resource, Namespace: req.Namespace, VerifiedPatch: "password=hunter2", Attempts: []shadow.Attempt{{Logs: []string{"password=hunter2"}}}, Cleanup: []string{"deleted pod/fixora-shadow"}}, nil
		},
	}
	session := connectServerTestClient(t, "2025-11-25", server)
	result := callTestTool(t, session, "shadow-verify", map[string]any{"resource": "deployment/api", "confirm": true, "container": "api", "image": "ghcr.io/acme/api:v2"})
	if result.IsError || typedCalls != 1 || runnerCalls != 1 {
		t.Fatalf("result=%#v text=%q typed=%d runner=%d", result, resultText(result), typedCalls, runnerCalls)
	}
	text := resultText(result)
	if strings.Contains(text, "hunter2") || strings.Contains(text, "verifiedPatch") || strings.Contains(text, "logs") || !strings.Contains(text, "verified") {
		t.Fatalf("unsafe summary: %s", text)
	}
}

func TestMCPShadowToolHiddenByDefault(t *testing.T) {
	session := connectTestClient(t, "2025-11-25")
	listed, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range listed.Tools {
		if tool.Name == "shadow-verify" {
			t.Fatal("shadow tool exposed without server opt-in")
		}
	}
}

func TestMCPConfigOmitsProviderURLAndLimitsResults(t *testing.T) {
	cfg := config.Default()
	cfg.AIBaseURL = "https://user:password@example.invalid/?key=private"
	out := mcpPublicConfig(cfg)
	if _, ok := out["aiBaseURL"]; ok {
		t.Fatalf("provider URL exposed: %#v", out)
	}
	_, _, err := safeResult(map[string]string{"payload": strings.Repeat("x", maxMCPResultBytes)})
	if err == nil {
		t.Fatal("oversized MCP result accepted")
	}
}

func TestMCPSafeResultRedactsSecretObject(t *testing.T) {
	_, text, err := safeResult(map[string]any{"kind": "Secret", "data": map[string]any{"password": "dGVzdA=="}})
	if err != nil || strings.Contains(text, "dGVzdA==") || !strings.Contains(text, "REDACTED") {
		t.Fatalf("safe result=%q err=%v", text, err)
	}
}

func TestServeStdioDoesNotReplyToNotification(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	inputReader, inputWriter := io.Pipe()
	outputReader, outputWriter := io.Pipe()
	defer inputWriter.Close()
	defer outputReader.Close()
	done := make(chan error, 1)
	go func() { done <- Server{}.ServeStdio(ctx, inputReader, outputWriter) }()
	encoder := json.NewEncoder(inputWriter)
	reader := bufio.NewReader(outputReader)
	if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{}, "clientInfo": map[string]string{"name": "test", "version": "1"}}}); err != nil {
		t.Fatal(err)
	}
	line, err := reader.ReadBytes('\n')
	if err != nil || !strings.Contains(string(line), `"id":1`) {
		t.Fatalf("initialize response=%s err=%v", line, err)
	}
	if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}); err != nil {
		t.Fatal(err)
	}
	if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list"}); err != nil {
		t.Fatal(err)
	}
	line, err = reader.ReadBytes('\n')
	if err != nil || !strings.Contains(string(line), `"id":2`) {
		t.Fatalf("notification produced a response: %s err=%v", line, err)
	}
	cancel()
	_ = inputWriter.Close()
	_ = outputReader.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("normal shutdown returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stdio server did not stop")
	}
}

func TestMCPResourceListPaginates(t *testing.T) {
	items := make([]map[string]any, 120)
	for i := range items {
		items[i] = map[string]any{"metadata": map[string]any{"name": fmt.Sprintf("pod-%03d", i)}}
	}
	reader := &fakeReader{items: items}
	session := connectServerTestClient(t, "2025-11-25", Server{Reader: reader, AnalyzerOpt: analyzer.Options{Namespace: "prod"}})
	first := callTestTool(t, session, "list-resources", map[string]any{"type": "pods"})
	var page struct {
		Items      []map[string]any `json:"items"`
		NextOffset int              `json:"nextOffset"`
	}
	if err := json.Unmarshal([]byte(resultText(first)), &page); err != nil || len(page.Items) != 50 || page.NextOffset != 50 {
		t.Fatalf("first page=%#v err=%v", page, err)
	}
	second := callTestTool(t, session, "list-resources", map[string]any{"type": "pods", "offset": 50, "limit": 100})
	if err := json.Unmarshal([]byte(resultText(second)), &page); err != nil || len(page.Items) != 70 || page.NextOffset != 0 {
		t.Fatalf("second page=%#v err=%v", page, err)
	}
}
