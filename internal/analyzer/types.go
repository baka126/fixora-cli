package analyzer

import "github.com/fixora/kubectl-fixora/internal/kube"

type Options struct {
	Namespace   string
	AllNS       bool
	IncludeLogs bool
	Redact      bool
	Filters     []string
}

type Analyzer struct {
	k    kube.Kubectl
	opts Options
}

type ScanReport struct {
	Findings []Finding      `json:"findings"`
	Skipped  []SkippedCheck `json:"skipped,omitempty"`
	Summary  ScanSummary    `json:"summary"`
}

type SkippedCheck struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

type ScanSummary struct {
	Findings       int `json:"findings"`
	SkippedChecks  int `json:"skippedChecks"`
	HighSeverity   int `json:"highSeverity"`
	MediumSeverity int `json:"mediumSeverity"`
	LowSeverity    int `json:"lowSeverity"`
}

type Finding struct {
	ID              string           `json:"id"`
	Namespace       string           `json:"namespace"`
	ResourceKind    string           `json:"resourceKind"`
	ResourceName    string           `json:"resourceName"`
	PodName         string           `json:"podName,omitempty"`
	Status          string           `json:"status"`
	Severity        string           `json:"severity"`
	Category        string           `json:"category"`
	Summary         string           `json:"summary"`
	Evidence        []Evidence       `json:"evidence"`
	OwnerChain      []string         `json:"ownerChain,omitempty"`
	GitOps          GitOpsHints      `json:"gitops,omitempty"`
	Recommendations []Recommendation `json:"recommendations"`
	Logs            []LogSnippet     `json:"logs,omitempty"`
	AI              *AIResult        `json:"ai,omitempty"`
}

type Evidence struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

type Recommendation struct {
	Title         string `json:"title"`
	Description   string `json:"description"`
	PatchType     string `json:"patchType,omitempty"`
	SafeByDefault bool   `json:"safeByDefault"`
}

type LogSnippet struct {
	Source string `json:"source"`
	Text   string `json:"text"`
}

type AIResult struct {
	Summary        string   `json:"summary"`
	RootCause      string   `json:"rootCause"`
	RecommendedFix string   `json:"recommendedFix"`
	Commands       []string `json:"commands,omitempty"`
	Warnings       []string `json:"warnings,omitempty"`
}

type GitOpsHints struct {
	ManagedBy    string `json:"managedBy,omitempty"`
	HelmRelease  string `json:"helmRelease,omitempty"`
	HelmChart    string `json:"helmChart,omitempty"`
	FluxHint     string `json:"fluxHint,omitempty"`
	ArgoHint     string `json:"argoHint,omitempty"`
	TargetAdvice string `json:"targetAdvice,omitempty"`
}

type Prediction struct {
	Namespace   string `json:"namespace"`
	PodName     string `json:"podName"`
	Signal      string `json:"signal"`
	Risk        string `json:"risk"`
	Confidence  int    `json:"confidence"`
	Evidence    string `json:"evidence"`
	Recommended string `json:"recommended"`
}

type CostRow struct {
	Name         string `json:"name"`
	Kind         string `json:"kind"`
	Region       string `json:"region,omitempty"`
	InstanceType string `json:"instanceType,omitempty"`
	MonthlyUSD   string `json:"monthlyUSD,omitempty"`
	Note         string `json:"note,omitempty"`
}

type LintResult struct {
	Path     string `json:"path"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

type Definition struct {
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Resource    string `json:"resource"`
	Scope       string `json:"scope"`
	Description string `json:"description"`
	Enabled     bool   `json:"enabled"`
}
