package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/fixora/kubectl-fixora/internal/config"
	"github.com/fixora/kubectl-fixora/internal/redact"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type noInput struct{}
type resourceInput struct {
	Resource string `json:"resource" jsonschema:"Kubernetes kind/name to inspect"`
}
type listInput struct {
	Type   string `json:"type,omitempty" jsonschema:"Kubernetes resource type to list; defaults to pods"`
	Limit  int    `json:"limit,omitempty" jsonschema:"Page size, maximum 100"`
	Offset int    `json:"offset,omitempty" jsonschema:"Zero-based offset for the next page"`
}
type eventsInput struct {
	Limit  int `json:"limit,omitempty"`
	Offset int `json:"offset,omitempty"`
}
type logsInput struct {
	Pod       string `json:"pod" jsonschema:"Pod name"`
	Namespace string `json:"namespace,omitempty" jsonschema:"Pod namespace within server scope"`
	Previous  bool   `json:"previous,omitempty" jsonschema:"Read previous container logs"`
}
type fixInput struct {
	Resource      string `json:"resource" jsonschema:"Kubernetes kind/name to diagnose"`
	Container     string `json:"container,omitempty"`
	Image         string `json:"image,omitempty"`
	MemoryRequest string `json:"memoryRequest,omitempty"`
	MemoryLimit   string `json:"memoryLimit,omitempty"`
	CPURequest    string `json:"cpuRequest,omitempty"`
	Strategy      string `json:"strategy,omitempty"`
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

func (s Server) newSDKServer() *sdk.Server {
	server := sdk.NewServer(&sdk.Implementation{Name: "fixora-cli", Version: "v1alpha1"}, &sdk.ServerOptions{Capabilities: &sdk.ServerCapabilities{}, PageSize: 100})
	addTool[resourceInput](server, s, "analyze", "Diagnose a Kubernetes resource and its related evidence.")
	addTool[noInput](server, s, "incidents", "Scan the configured Kubernetes scope for incidents.")
	addTool[noInput](server, s, "health", "Summarize cluster health in the configured scope.")
	addTool[resourceInput](server, s, "runbook", "Create an incident runbook for a resource.")
	addTool[fixInput](server, s, "plan-fix", "Build a deterministic repair plan; does not change the cluster.")
	addTool[fixInput](server, s, "preview-fix", "Preview a concrete repair patch and diff.")
	addTool[fixInput](server, s, "validate-fix", "Check whether a concrete repair is eligible for apply.")
	addTool[listInput](server, s, "list-resources", "List approved Kubernetes resources in the configured scope.")
	addTool[resourceInput](server, s, "get-resource", "Read an approved Kubernetes resource in the configured scope.")
	addTool[logsInput](server, s, "get-logs", "Read bounded, redacted Pod logs in the configured scope.")
	addTool[eventsInput](server, s, "list-events", "List redacted Kubernetes Events in the configured scope.")
	addTool[noInput](server, s, "list-filters", "List available Fixora analyzers.")
	addTool[noInput](server, s, "config", "Show non-secret Fixora configuration.")
	if s.EnableShadow {
		addShadowTool(server, s)
	}
	for _, item := range []struct{ name, description string }{
		{"troubleshoot-pod", "Guide Pod incident triage."}, {"troubleshoot-deployment", "Guide Deployment rollout triage."},
		{"troubleshoot-cluster", "Guide cluster incident triage."}, {"incident-runbook", "Create a production incident runbook."},
	} {
		name := item.name
		server.AddPrompt(&sdk.Prompt{Name: name, Description: item.description, Arguments: []*sdk.PromptArgument{{Name: "resource", Description: "Kubernetes resource to investigate"}}}, func(ctx context.Context, req *sdk.GetPromptRequest) (*sdk.GetPromptResult, error) {
			args := req.Params.Arguments
			target := firstNonEmpty(args["resource"], args["pod"], args["deployment"], "<target>")
			text := "Use Fixora tools to gather status, events, logs, owner chain, recent changes, policy, networking, storage, and safe rollback evidence for " + target + ". Prefer GitOps-safe fixes and call out verification commands."
			return &sdk.GetPromptResult{Description: name, Messages: []*sdk.PromptMessage{{Role: sdk.Role("user"), Content: &sdk.TextContent{Text: text}}}}, nil
		})
	}
	server.AddResource(&sdk.Resource{URI: "fixora://config", Name: "Fixora Config", MIMEType: "application/json"}, func(ctx context.Context, req *sdk.ReadResourceRequest) (*sdk.ReadResourceResult, error) {
		cfg, err := config.Load()
		if err != nil {
			return nil, err
		}
		_, text, err := safeResult(mcpPublicConfig(cfg))
		if err != nil {
			return nil, err
		}
		return &sdk.ReadResourceResult{Contents: []*sdk.ResourceContents{{URI: "fixora://config", MIMEType: "application/json", Text: string(text)}}}, nil
	})
	server.AddResource(&sdk.Resource{URI: "fixora://cluster/info", Name: "Cluster Info", MIMEType: "application/json"}, func(ctx context.Context, req *sdk.ReadResourceRequest) (*sdk.ReadResourceResult, error) {
		info, err := s.Kubectl.Status(ctx)
		if err != nil {
			return nil, err
		}
		_, text, err := safeResult(info)
		if err != nil {
			return nil, err
		}
		return &sdk.ReadResourceResult{Contents: []*sdk.ResourceContents{{URI: "fixora://cluster/info", MIMEType: "application/json", Text: string(text)}}}, nil
	})
	return server
}

func addTool[In any](server *sdk.Server, s Server, name, description string) {
	sdk.AddTool[In, any](server, &sdk.Tool{Name: name, Description: description, Annotations: &sdk.ToolAnnotations{ReadOnlyHint: true}}, func(ctx context.Context, req *sdk.CallToolRequest, input In) (*sdk.CallToolResult, any, error) {
		timeout := 3 * time.Minute
		switch name {
		case "get-resource", "list-resources", "get-logs", "list-events", "config", "list-filters":
			timeout = 30 * time.Second
		}
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		data, err := json.Marshal(input)
		if err != nil {
			return nil, nil, err
		}
		args := map[string]any{}
		if err := json.Unmarshal(data, &args); err != nil {
			return nil, nil, err
		}
		value, err := s.callTool(ctx, name, args)
		if err != nil {
			return nil, nil, fmt.Errorf("%s", redact.Text(err.Error()))
		}
		safe, text, err := safeResult(value)
		if err != nil {
			return nil, nil, err
		}
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: text}}, StructuredContent: safe}, nil, nil
	})
}

func addShadowTool(server *sdk.Server, s Server) {
	sdk.AddTool[shadowInput, any](server, &sdk.Tool{Name: "shadow-verify", Description: "Create a temporary isolated shadow Pod and NetworkPolicy to verify an eligible patch; requires server opt-in and confirm=true; never delivers the patch.", Annotations: &sdk.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: boolPointer(false)}}, func(ctx context.Context, req *sdk.CallToolRequest, input shadowInput) (*sdk.CallToolResult, any, error) {
		value, err := s.callShadow(ctx, input)
		if err != nil {
			return nil, nil, fmt.Errorf("%s", redact.Text(err.Error()))
		}
		safe, text, err := safeResult(value)
		if err != nil {
			return nil, nil, err
		}
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: text}}, StructuredContent: safe}, nil, nil
	})
}

func boolPointer(value bool) *bool { return &value }
