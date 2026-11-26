package mcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/config"
	"github.com/fixora/kubectl-fixora/internal/fix"
	"github.com/fixora/kubectl-fixora/internal/kube"
	"github.com/fixora/kubectl-fixora/internal/ops"
	"github.com/fixora/kubectl-fixora/internal/shadow"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Server exposes only scoped, redacted diagnostics over local MCP stdio.
type Server struct {
	Kubectl         kube.Kubectl
	AnalyzerOpt     analyzer.Options
	Reader          kube.Reader // optional test/alternate reader; defaults to Kubectl
	EnableShadow    bool
	analyzeResource func(context.Context, string) (analyzer.Finding, error)                         // optional test seam
	newTypedClient  func() (*kube.TypedClient, error)                                               // optional test seam
	runShadow       func(context.Context, *kube.TypedClient, shadow.Request) (shadow.Result, error) // optional test seam
}

func (s Server) reader() kube.Reader {
	if s.Reader != nil {
		return s.Reader
	}
	return s.Kubectl
}

func (s Server) ServeStdio(ctx context.Context, in io.Reader, out io.Writer) error {
	err := s.newSDKServer().Run(ctx, &sdk.IOTransport{Reader: io.NopCloser(in), Writer: nopWriteCloser{out}, MaxLineLength: 10 * 1024 * 1024})
	if errors.Is(err, context.Canceled) && ctx.Err() != nil {
		return nil
	}
	return err
}

func (s Server) callTool(ctx context.Context, name string, args map[string]any) (any, error) {
	reader := s.reader()
	a := analyzer.New(reader, s.AnalyzerOpt)
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
			Container: stringArg(args, "container"), Image: stringArg(args, "image"), MemoryRequest: stringArg(args, "memoryRequest"), MemoryLimit: stringArg(args, "memoryLimit"), CPURequest: stringArg(args, "cpuRequest"), Strategy: stringArg(args, "strategy"),
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
		if err := allowResourceType(resource); err != nil {
			return nil, err
		}
		items, err := reader.GetResourceItems(ctx, s.AnalyzerOpt.Namespace, s.AnalyzerOpt.AllNS, resource)
		if err != nil {
			return nil, err
		}
		page, next, err := pageItems(items, intArg(args, "offset"), intArg(args, "limit"))
		if err != nil {
			return nil, err
		}
		return map[string]any{"items": page, "nextOffset": next}, nil
	case "get-resource":
		resource := stringArg(args, "resource")
		if err := allowResourceRef(resource); err != nil {
			return nil, err
		}
		return reader.GetResource(ctx, s.AnalyzerOpt.Namespace, resource)
	case "get-logs":
		pod := stringArg(args, "pod")
		if pod == "" {
			return nil, fmt.Errorf("pod is required")
		}
		namespace := firstNonEmpty(stringArg(args, "namespace"), s.AnalyzerOpt.Namespace)
		if !s.AnalyzerOpt.AllNS && namespace != s.AnalyzerOpt.Namespace {
			return nil, fmt.Errorf("namespace is outside server scope")
		}
		if namespace == "" {
			return nil, fmt.Errorf("namespace is required for Pod logs")
		}
		logs, err := reader.Logs(ctx, namespace, pod, boolArg(args, "previous"))
		if err != nil {
			return nil, err
		}
		return map[string]string{"logs": logs}, nil
	case "list-events":
		namespace := s.AnalyzerOpt.Namespace
		if s.AnalyzerOpt.AllNS {
			namespace = ""
		}
		events, err := reader.GetEvents(ctx, namespace, "")
		if err != nil {
			return nil, err
		}
		page, next, err := pageItems(events, intArg(args, "offset"), intArg(args, "limit"))
		if err != nil {
			return nil, err
		}
		return map[string]any{"items": page, "nextOffset": next}, nil
	case "list-filters":
		return analyzer.ListAnalyzers(nil), nil
	case "config":
		cfg, err := config.Load()
		if err != nil {
			return nil, err
		}
		return mcpPublicConfig(cfg), nil
	default:
		return nil, fmt.Errorf("unknown MCP tool %q", name)
	}
}

func stringArg(args map[string]any, key string) string {
	v, _ := args[key].(string)
	return strings.TrimSpace(v)
}
func boolArg(args map[string]any, key string) bool { v, _ := args[key].(bool); return v }
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func intArg(args map[string]any, key string) int { value, _ := args[key].(float64); return int(value) }

func pageItems[T any](items []T, offset, limit int) ([]T, int, error) {
	if offset < 0 || limit < 0 || limit > 100 {
		return nil, 0, fmt.Errorf("offset must be nonnegative and limit must be between 0 and 100")
	}
	if limit == 0 {
		limit = 50
	}
	if offset >= len(items) {
		return []T{}, 0, nil
	}
	end := offset + limit
	if end > len(items) {
		end = len(items)
	}
	next := 0
	if end < len(items) {
		next = end
	}
	return items[offset:end], next, nil
}
