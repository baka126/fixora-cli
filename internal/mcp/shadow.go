package mcp

import (
	"context"
	"fmt"
	"time"

	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/fix"
	"github.com/fixora/kubectl-fixora/internal/kube"
	"github.com/fixora/kubectl-fixora/internal/shadow"
)

type shadowInput struct {
	Resource       string `json:"resource" jsonschema:"Kubernetes kind/name to verify"`
	Confirm        bool   `json:"confirm" jsonschema:"Must be true after operator approval to create a temporary shadow Pod and NetworkPolicy"`
	Container      string `json:"container,omitempty"`
	Image          string `json:"image,omitempty"`
	MemoryRequest  string `json:"memoryRequest,omitempty"`
	MemoryLimit    string `json:"memoryLimit,omitempty"`
	CPURequest     string `json:"cpuRequest,omitempty"`
	Strategy       string `json:"strategy,omitempty"`
	TimeoutSeconds int    `json:"timeoutSeconds,omitempty" jsonschema:"Verification timeout in seconds, maximum 600"`
}

type shadowSummary struct {
	Verified       bool     `json:"verified"`
	Parity         int      `json:"parity"`
	Resource       string   `json:"resource"`
	Namespace      string   `json:"namespace"`
	FailureClass   string   `json:"failureClass,omitempty"`
	FailureSummary string   `json:"failureSummary,omitempty"`
	Cleanup        []string `json:"cleanup,omitempty"`
	Warnings       []string `json:"warnings,omitempty"`
}

func (s Server) callShadow(ctx context.Context, input shadowInput) (shadowSummary, error) {
	if !s.EnableShadow {
		return shadowSummary{}, fmt.Errorf("MCP shadow verification is disabled at server startup")
	}
	if !input.Confirm {
		return shadowSummary{}, fmt.Errorf("shadow verification requires confirm=true after operator approval")
	}
	if err := allowResourceRef(input.Resource); err != nil {
		return shadowSummary{}, err
	}
	if input.TimeoutSeconds < 0 || input.TimeoutSeconds > 600 {
		return shadowSummary{}, fmt.Errorf("timeoutSeconds must be between 0 and 600")
	}
	var finding analyzer.Finding
	var err error
	if s.analyzeResource != nil {
		finding, err = s.analyzeResource(ctx, input.Resource)
	} else {
		finding, err = analyzer.New(s.reader(), s.AnalyzerOpt).AnalyzeResource(ctx, input.Resource)
	}
	if err != nil {
		return shadowSummary{}, err
	}
	plan := fix.Concretize(fix.BuildPlan(finding), fix.ConcreteOptions{Container: input.Container, Image: input.Image, MemoryRequest: input.MemoryRequest, MemoryLimit: input.MemoryLimit, CPURequest: input.CPURequest, Strategy: input.Strategy})
	if !plan.ApplyEligible {
		return shadowSummary{}, fmt.Errorf("shadow verification requires an apply-eligible concrete patch")
	}
	patch := plan.PatchYAML()
	if err := shadow.ValidateCandidatePatch(patch, plan.Strategy, plan.Resource, finding.Namespace); err != nil {
		return shadowSummary{}, fmt.Errorf("candidate patch rejected: %w", err)
	}
	clientFactory := s.newTypedClient
	if clientFactory == nil {
		clientFactory = func() (*kube.TypedClient, error) {
			return kube.NewRequiredTypedClient(s.Kubectl.Context, "MCP shadow verification")
		}
	}
	client, err := clientFactory()
	if err != nil {
		return shadowSummary{}, err
	}
	runner := s.runShadow
	if runner == nil {
		runner = shadow.Run
	}
	timeout := time.Duration(input.TimeoutSeconds) * time.Second
	if timeout == 0 {
		timeout = 10 * time.Minute
	}
	req := shadow.Request{Namespace: finding.Namespace, Resource: plan.Resource, Patch: patch, Finding: finding, Plan: plan, Timeout: timeout, Retries: 0, Keep: false, Egress: "deny", Delivery: shadow.DeliveryPatch, Redact: true}
	runCtx, cancel := context.WithTimeout(ctx, timeout+2*time.Minute)
	defer cancel()
	result, err := runner(runCtx, client, req)
	summary := shadowSummary{Verified: result.Verified, Parity: result.Parity, Resource: result.Resource, Namespace: result.Namespace, FailureClass: result.FailureClass, FailureSummary: result.FailureSummary, Cleanup: result.Cleanup, Warnings: result.Warnings}
	if err != nil {
		return summary, err
	}
	return summary, nil
}
