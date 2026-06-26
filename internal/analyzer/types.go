package analyzer

import (
	"context"
	"fmt"
	"sync"

	"github.com/fixora/kubectl-fixora/internal/kube"
)

type ScanContext struct {
	context.Context
	Reader  kube.Reader
	Opts    Options
	mu      sync.Mutex
	pods    *kube.PodList
	events  []kube.Event
	nodes   []kube.Node
	items   map[string][]map[string]any
	objects map[string]map[string]any
}

func NewScanContext(ctx context.Context, k kube.Reader, opts Options) *ScanContext {
	return &ScanContext{
		Context: ctx,
		Reader:  k,
		Opts:    opts,
		items:   make(map[string][]map[string]any),
		objects: make(map[string]map[string]any),
	}
}

func (s *ScanContext) GetPods() (kube.PodList, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pods != nil {
		return *s.pods, nil
	}
	pods, err := s.Reader.GetPods(s, s.Opts.Namespace, s.Opts.AllNS)
	if err == nil {
		pods, err = filterPodsByLabelSelector(pods, s.Opts.LabelSelector)
	}
	if err == nil {
		s.pods = &pods
	}
	return pods, err
}

func (s *ScanContext) GetEvents() ([]kube.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.events != nil {
		return s.events, nil
	}
	ns := ""
	if !s.Opts.AllNS {
		ns = s.Opts.Namespace
	}
	events, err := s.Reader.GetEvents(s, ns, "")
	if err == nil {
		s.events = events
	}
	return events, err
}

func (s *ScanContext) GetResourceItems(namespace string, allNS bool, resource string) ([]map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := fmt.Sprintf("%s:%v:%s", namespace, allNS, resource)
	if items, ok := s.items[key]; ok {
		return items, nil
	}
	items, err := s.Reader.GetResourceItems(s, namespace, allNS, resource)
	if err == nil {
		items, err = filterObjectsByLabelSelector(items, s.Opts.LabelSelector)
	}
	if err == nil {
		s.items[key] = items
	}
	return items, err
}

func (s *ScanContext) GetResource(namespace, resource string) (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := namespace + "/" + resource
	if obj, ok := s.objects[key]; ok {
		return obj, nil
	}
	obj, err := s.Reader.GetResource(s, namespace, resource)
	if err == nil {
		s.objects[key] = obj
	}
	return obj, err
}

type AnalyzerPlugin interface {
	Name() string
	Analyze(ctx *ScanContext) ([]Finding, error)
}

type Options struct {
	Namespace      string
	AllNS          bool
	IncludeLogs    bool
	Redact         bool
	Filters        []string
	LabelSelector  string
	MaxConcurrency int
}

type Analyzer struct {
	k    kube.Reader
	opts Options
}

type ScanReport struct {
	Findings []Finding      `json:"findings"`
	Skipped  []SkippedCheck `json:"skipped,omitempty"`
	Summary  ScanSummary    `json:"summary"`
}

type ScanEnvelope struct {
	APIVersion string         `json:"apiVersion"`
	Kind       string         `json:"kind"`
	Status     string         `json:"status"`
	Provider   string         `json:"provider,omitempty"`
	Problems   int            `json:"problems"`
	Results    []Finding      `json:"results"`
	Skipped    []SkippedCheck `json:"skipped,omitempty"`
	Warnings   []string       `json:"warnings,omitempty"`
	Summary    ScanSummary    `json:"summary"`
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
	ID                string           `json:"id"`
	Namespace         string           `json:"namespace"`
	ResourceKind      string           `json:"resourceKind"`
	ResourceName      string           `json:"resourceName"`
	PodName           string           `json:"podName,omitempty"`
	Status            string           `json:"status"`
	Severity          string           `json:"severity"`
	Category          string           `json:"category"`
	Summary           string           `json:"summary"`
	Evidence          []Evidence       `json:"evidence"`
	OwnerChain        []string         `json:"ownerChain,omitempty"`
	GitOps            GitOpsHints      `json:"gitops,omitempty"`
	ChangeCorrelation string           `json:"changeCorrelation,omitempty"`
	RecentChanges     []string         `json:"recentChanges,omitempty"`
	Recommendations   []Recommendation `json:"recommendations"`
	Logs              []LogSnippet     `json:"logs,omitempty"`
	AI                *AIResult        `json:"ai,omitempty"`
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
	PatchYAML      string   `json:"patchYAML,omitempty"`
	Strategy       string   `json:"strategy,omitempty"`
	Confidence     int      `json:"confidence,omitempty"`
	Analyzers      []string `json:"analyzers,omitempty"`
	Commands       []string `json:"commands,omitempty"`
	Warnings       []string `json:"warnings,omitempty"`
	// Unstructured is set by the AI client when the model returned text that
	// could not be parsed as the JSON contract. Callers must ignore the rest
	// of the result and use the deterministic plan instead.
	Unstructured bool `json:"-"`
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
