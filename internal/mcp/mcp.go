package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/config"
	"github.com/fixora/kubectl-fixora/internal/fix"
	"github.com/fixora/kubectl-fixora/internal/kube"
	"github.com/fixora/kubectl-fixora/internal/ops"
)

type Server struct {
	Kubectl     kube.Kubectl
	AnalyzerOpt analyzer.Options
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string         `json:"jsonrpc"`
	ID      any            `json:"id,omitempty"`
	Result  any            `json:"result,omitempty"`
	Error   *responseError `json:"error,omitempty"`
}

type responseError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (s Server) ServeStdio(ctx context.Context, in io.Reader, out io.Writer) error {
	scanner := bufio.NewScanner(in)
	encoder := json.NewEncoder(out)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var req request
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			_ = encoder.Encode(response{JSONRPC: "2.0", Error: &responseError{Code: -32700, Message: err.Error()}})
			continue
		}
		result, err := s.handle(ctx, req)
		resp := response{JSONRPC: "2.0", ID: req.ID, Result: result}
		if err != nil {
			resp.Result = nil
			resp.Error = &responseError{Code: -32000, Message: err.Error()}
		}
		if err := encoder.Encode(resp); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func (s Server) handle(ctx context.Context, req request) (any, error) {
	switch req.Method {
	case "initialize":
		return map[string]any{
			"protocolVersion": "2024-11-05",
			"serverInfo":      map[string]string{"name": "fixora-cli", "version": "v1alpha1"},
			"capabilities":    map[string]any{"tools": map[string]any{}, "prompts": map[string]any{}, "resources": map[string]any{}},
		}, nil
	case "tools/list":
		return map[string]any{"tools": tools()}, nil
	case "tools/call":
		var params struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return nil, err
		}
		return s.callTool(ctx, params.Name, params.Arguments)
	case "prompts/list":
		return map[string]any{"prompts": prompts()}, nil
	case "prompts/get":
		var params struct {
			Name      string            `json:"name"`
			Arguments map[string]string `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return nil, err
		}
		return prompt(params.Name, params.Arguments), nil
	case "resources/list":
		return map[string]any{"resources": []map[string]string{
			{"uri": "fixora://cluster/info", "name": "Cluster Info"},
			{"uri": "fixora://config", "name": "Fixora Config"},
		}}, nil
	default:
		return nil, fmt.Errorf("unsupported MCP method %q", req.Method)
	}
}

func (s Server) callTool(ctx context.Context, name string, args map[string]any) (any, error) {
	a := analyzer.New(s.Kubectl, s.AnalyzerOpt)
	switch name {
	case "analyze":
		resource := stringArg(args, "resource")
		if resource == "" {
			return nil, fmt.Errorf("resource is required")
		}
		return a.AnalyzeResource(ctx, resource)
	case "incidents":
		return a.ScanReport(ctx).Envelope(), nil
	case "health":
		return ops.BuildHealth(ctx, s.Kubectl, a.ScanReport(ctx), s.AnalyzerOpt.Namespace), nil
	case "runbook":
		resource := stringArg(args, "resource")
		if resource == "" {
			return nil, fmt.Errorf("resource is required")
		}
		finding, err := a.AnalyzeResource(ctx, resource)
		if err != nil {
			return nil, err
		}
		return ops.BuildRunbook(finding, fix.BuildPlan(finding)), nil
	case "plan-fix", "preview-fix", "validate-fix":
		resource := stringArg(args, "resource")
		if resource == "" {
			return nil, fmt.Errorf("resource is required")
		}
		finding, err := a.AnalyzeResource(ctx, resource)
		if err != nil {
			return nil, err
		}
		plan := fix.Concretize(fix.BuildPlan(finding), fix.ConcreteOptions{
			Container:     stringArg(args, "container"),
			Image:         stringArg(args, "image"),
			MemoryRequest: stringArg(args, "memoryRequest"),
			MemoryLimit:   stringArg(args, "memoryLimit"),
			CPURequest:    stringArg(args, "cpuRequest"),
			Strategy:      stringArg(args, "strategy"),
			ForceRisky:    boolArg(args, "forceRisky"),
		})
		if name == "preview-fix" {
			return map[string]any{"plan": plan, "diff": plan.DiffView()}, nil
		}
		if name == "validate-fix" {
			return map[string]any{"applyEligible": plan.ApplyEligible, "blockedReasons": plan.BlockedReasons, "verification": plan.Verification}, nil
		}
		return plan, nil
	case "list-resources":
		resource := firstNonEmpty(stringArg(args, "type"), "pods")
		return s.Kubectl.GetResourceItems(ctx, s.AnalyzerOpt.Namespace, s.AnalyzerOpt.AllNS, resource)
	case "get-resource":
		resource := stringArg(args, "resource")
		if resource == "" {
			return nil, fmt.Errorf("resource is required")
		}
		return s.Kubectl.GetResource(ctx, s.AnalyzerOpt.Namespace, resource)
	case "get-logs":
		pod := stringArg(args, "pod")
		if pod == "" {
			return nil, fmt.Errorf("pod is required")
		}
		return map[string]string{"logs": mustString(s.Kubectl.Logs(ctx, firstNonEmpty(stringArg(args, "namespace"), s.AnalyzerOpt.Namespace), pod, boolArg(args, "previous")))}, nil
	case "list-events":
		return s.Kubectl.GetEvents(ctx, s.AnalyzerOpt.Namespace)
	case "list-filters":
		return analyzer.ListAnalyzers(nil), nil
	case "config":
		cfg, err := config.Load()
		if err != nil {
			return nil, err
		}
		return config.Public(cfg), nil
	default:
		return nil, fmt.Errorf("unknown MCP tool %q", name)
	}
}

func tools() []map[string]any {
	names := []string{"analyze", "incidents", "health", "runbook", "plan-fix", "preview-fix", "validate-fix", "list-resources", "get-resource", "get-logs", "list-events", "list-filters", "config"}
	out := make([]map[string]any, 0, len(names))
	for _, name := range names {
		out = append(out, map[string]any{"name": name, "description": "Fixora Kubernetes SRE tool: " + name, "inputSchema": map[string]any{"type": "object"}})
	}
	return out
}

func prompts() []map[string]string {
	return []map[string]string{
		{"name": "troubleshoot-pod", "description": "Guide an SRE through pod incident triage."},
		{"name": "troubleshoot-deployment", "description": "Guide an SRE through deployment rollout triage."},
		{"name": "troubleshoot-cluster", "description": "Guide an SRE through cluster-wide incident triage."},
		{"name": "incident-runbook", "description": "Create a production incident runbook."},
	}
}

func prompt(name string, args map[string]string) map[string]any {
	target := firstNonEmpty(args["resource"], args["pod"], args["deployment"], "<target>")
	text := "Use Fixora tools to gather status, events, logs, owner chain, recent changes, policy, networking, storage, and safe rollback evidence for " + target + ". Prefer GitOps-safe fixes and call out verification commands."
	return map[string]any{"description": name, "messages": []map[string]any{{"role": "user", "content": map[string]string{"type": "text", "text": text}}}}
}

func stringArg(args map[string]any, key string) string {
	if args[key] == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(args[key]))
}

func boolArg(args map[string]any, key string) bool {
	value, _ := args[key].(bool)
	return value
}

func mustString(value string, err error) string {
	if err != nil {
		return err.Error()
	}
	return value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" && value != "<nil>" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
