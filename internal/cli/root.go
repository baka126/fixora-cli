package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/fixora/kubectl-fixora/internal/ai"
	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/bundle"
	"github.com/fixora/kubectl-fixora/internal/cache"
	"github.com/fixora/kubectl-fixora/internal/config"
	"github.com/fixora/kubectl-fixora/internal/custom"
	"github.com/fixora/kubectl-fixora/internal/debug"
	"github.com/fixora/kubectl-fixora/internal/fix"
	"github.com/fixora/kubectl-fixora/internal/graph"
	"github.com/fixora/kubectl-fixora/internal/integration"
	"github.com/fixora/kubectl-fixora/internal/kube"
	"github.com/fixora/kubectl-fixora/internal/memory"
	"github.com/fixora/kubectl-fixora/internal/output"
	"github.com/fixora/kubectl-fixora/internal/repo"
	"github.com/fixora/kubectl-fixora/internal/report"
	"github.com/fixora/kubectl-fixora/internal/server"
	"github.com/fixora/kubectl-fixora/internal/termui"
	"github.com/fixora/kubectl-fixora/internal/version"
)

type options struct {
	namespace   string
	allNS       bool
	context     string
	output      string
	includeLogs bool
	useAI       bool
	autoFix     bool
	apply       bool
	outFile     string
	verbose     bool
	redact      bool
	filters     string
	wide        bool
	noColor     bool
	proof       bool
	paranoid    bool
	preview     bool
	repoPath    string
	branch      string
	commit      bool
	profile     string
	aiBudget    int
	container   string
	image       string
	memRequest  string
	memLimit    string
	cpuRequest  string
	envName     string
	configMap   string
	configKey   string
}

func Execute(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printHelp(stdout)
		return 0
	}
	if args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		printHelp(stdout)
		return 0
	}
	if args[0] == "version" {
		fmt.Fprintf(stdout, "%s %s\n", version.Name, version.Version)
		return 0
	}
	if args[0] == "auth" {
		return runAuth(args[1:], stdout, stderr)
	}
	if args[0] == "config" {
		return runConfig(args[1:], stdout, stderr)
	}
	if args[0] == "cache" {
		return runCache(args[1:], stdout, stderr)
	}
	if args[0] == "ai" {
		return runAI(args[1:], stdout, stderr)
	}
	if args[0] == "memory" {
		return runMemory(args[1:], stdout, stderr)
	}

	cmd := args[0]
	opts, rest, err := parseFlags(args[1:])
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 2
	}
	var ctx context.Context
	var cancel context.CancelFunc
	if cmd == "serve" {
		ctx, cancel = signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	} else {
		ctx, cancel = context.WithTimeout(context.Background(), 90*time.Second)
	}
	defer cancel()

	k := kube.NewKubectl(opts.context)
	a := analyzer.New(k, analyzer.Options{
		Namespace:   opts.namespace,
		AllNS:       opts.allNS,
		IncludeLogs: opts.includeLogs,
		Redact:      opts.redact || opts.paranoid,
		Filters:     splitCSV(opts.filters),
	})

	switch cmd {
	case "status":
		return runStatus(ctx, stdout, stderr, opts, k)
	case "doctor":
		return runDoctorReport(ctx, stdout, stderr, opts, k)
	case "filters", "analyzers":
		return output.Write(stdout, opts.output, analyzer.ListAnalyzers(splitCSV(opts.filters)))
	case "integrations":
		return output.Write(stdout, opts.output, integration.List(ctx, k))
	case "custom-analyzers":
		return runCustomAnalyzers(ctx, stdout, stderr, opts, a, rest)
	case "serve":
		addr := "127.0.0.1:8089"
		if len(rest) > 0 {
			addr = rest[0]
		}
		fmt.Fprintf(stdout, "serving local Fixora CLI API on http://%s\n", addr)
		err := server.Serve(ctx, server.Options{
			Addr:        addr,
			Kubectl:     k,
			AnalyzerOpt: analyzer.Options{Namespace: opts.namespace, AllNS: opts.allNS, IncludeLogs: opts.includeLogs, Redact: opts.redact, Filters: splitCSV(opts.filters)},
			Token:       os.Getenv("FIXORA_SERVE_TOKEN"),
		})
		if err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
		return 0
	case "incidents":
		findings, err := a.ScanIncidents(ctx)
		return writeFindings(stdout, stderr, opts, findings, err)
	case "analyze", "explain", "why":
		if len(rest) == 0 {
			fmt.Fprintln(stderr, "error: analyze requires a resource, for example deployment/api")
			return 2
		}
		finding, err := a.AnalyzeResource(ctx, rest[0])
		if err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
		if cmd == "explain" || opts.useAI {
			augmentWithAI(ctx, &finding, opts, stderr)
		}
		if cmd == "why" {
			plan := fix.BuildPlan(finding)
			termui.Why(stdout, finding, plan, opts.proof, termui.Options{Wide: opts.wide, NoColor: opts.noColor})
			_ = memory.Add(finding, plan, "inspected")
			return 0
		}
		return writeFindings(stdout, stderr, opts, []analyzer.Finding{finding}, nil)
	case "plan", "diff", "patch":
		if len(rest) == 0 {
			fmt.Fprintf(stderr, "error: %s requires a resource, for example deployment/api\n", cmd)
			return 2
		}
		finding, err := a.AnalyzeResource(ctx, rest[0])
		if err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
		if opts.useAI {
			augmentWithAI(ctx, &finding, opts, stderr)
		}
		plan := fix.BuildPlan(finding)
		plan = fix.Concretize(plan, concreteOptions(opts))
		if opts.repoPath != "" {
			mode, repoErr := repo.Plan(ctx, opts.repoPath, finding, plan)
			if repoErr == nil {
				plan.Steps = append(plan.Steps, "Repo mode detected "+mode.Type+" source at "+mode.Path+". "+mode.ValidationNote)
			} else {
				plan.BlockedReasons = append(plan.BlockedReasons, repoErr.Error())
			}
		}
		if opts.autoFix {
			plan.AutoFixRequested = true
		}
		if cmd == "diff" {
			return output.Write(stdout, opts.output, plan.DiffView())
		}
		if cmd == "patch" {
			if opts.preview {
				termui.Plan(stdout, plan, termui.Options{Wide: opts.wide, NoColor: opts.noColor})
				return 0
			}
			if opts.outFile == "" {
				opts.outFile = "fixora-patch.yaml"
			}
			if err := os.WriteFile(opts.outFile, []byte(plan.PatchYAML()), 0o600); err != nil {
				fmt.Fprintf(stderr, "error: write patch: %v\n", err)
				return 1
			}
			fmt.Fprintf(stdout, "wrote %s\n", opts.outFile)
			if opts.apply {
				if !plan.CanApply {
					fmt.Fprintln(stderr, "error: generated patch is advisory only; review and make it concrete before applying")
					return 1
				}
				if err := k.Apply(ctx, opts.outFile); err != nil {
					fmt.Fprintf(stderr, "error: apply patch: %v\n", err)
					return 1
				}
				fmt.Fprintln(stdout, "applied patch")
			}
			if opts.repoPath != "" && (opts.branch != "" || opts.commit) {
				if err := repo.PrepareBranch(ctx, opts.repoPath, opts.branch, opts.commit, "fixora: add remediation patch for "+finding.ResourceKind+"/"+finding.ResourceName); err != nil {
					fmt.Fprintf(stderr, "error: repo workflow: %v\n", err)
					return 1
				}
			}
			_ = memory.Add(finding, plan, "patch-generated")
			return 0
		}
		return output.Write(stdout, opts.output, plan)
	case "graph":
		if len(rest) == 0 {
			fmt.Fprintln(stderr, "error: graph requires a resource")
			return 2
		}
		finding, err := a.AnalyzeResource(ctx, rest[0])
		if err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
		g := graph.Build(ctx, k, finding)
		if opts.output == "mermaid" {
			fmt.Fprint(stdout, graph.Mermaid(g))
			return 0
		}
		if opts.output == "text" {
			fmt.Fprint(stdout, graph.Text(g))
			return 0
		}
		return output.Write(stdout, opts.output, g)
	case "trace":
		target := ""
		if len(rest) > 0 {
			target = rest[0]
		}
		return output.Write(stdout, opts.output, debug.Trace(ctx, k, opts.namespace, target))
	case "storage":
		return output.Write(stdout, opts.output, debug.Storage(ctx, k, opts.namespace))
	case "rbac":
		sa, verb, resource := "", "get", "pods"
		if len(rest) > 0 {
			sa = rest[0]
		}
		if len(rest) > 1 {
			verb = rest[1]
		}
		if len(rest) > 2 {
			resource = rest[2]
		}
		return output.Write(stdout, opts.output, debug.RBAC(ctx, k, opts.namespace, sa, verb, resource))
	case "dns":
		return output.Write(stdout, opts.output, debug.DNS(ctx, k, opts.namespace))
	case "security":
		return output.Write(stdout, opts.output, debug.Security(ctx, k, opts.namespace))
	case "node-pressure":
		return output.Write(stdout, opts.output, debug.NodePressure(ctx, k))
	case "repo":
		mode, err := repo.Detect(firstArg(rest, opts.repoPath, "."))
		return output.WriteOrError(stdout, stderr, opts.output, mode, err)
	case "validate":
		mode, err := repo.Detect(firstArg(rest, opts.repoPath, "."))
		if err != nil {
			return output.WriteOrError(stdout, stderr, opts.output, nil, err)
		}
		err = repo.Validate(ctx, mode)
		return output.WriteOrError(stdout, stderr, opts.output, map[string]any{"repo": mode, "valid": err == nil}, err)
	case "report":
		if len(rest) == 0 {
			fmt.Fprintln(stderr, "error: report requires a resource, for example deployment/api")
			return 2
		}
		finding, err := a.AnalyzeResource(ctx, rest[0])
		if err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
		if opts.useAI {
			augmentWithAI(ctx, &finding, opts, stderr)
		}
		content := report.Markdown(finding)
		if opts.outFile != "" {
			if err := os.WriteFile(opts.outFile, []byte(content), 0o600); err != nil {
				fmt.Fprintf(stderr, "error: write report: %v\n", err)
				return 1
			}
			fmt.Fprintf(stdout, "wrote %s\n", opts.outFile)
			return 0
		}
		fmt.Fprint(stdout, content)
		return 0
	case "bundle":
		if len(rest) == 0 {
			fmt.Fprintln(stderr, "error: bundle requires a resource")
			return 2
		}
		finding, err := a.AnalyzeResource(ctx, rest[0])
		if err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
		plan := fix.BuildPlan(finding)
		out := opts.outFile
		if out == "" {
			out = "fixora-bundle.tgz"
		}
		if err := bundle.Write(ctx, k, out, finding, plan); err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "wrote %s\n", out)
		return 0
	case "ui":
		findings, err := a.ScanIncidents(ctx)
		if err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
		termui.Findings(stdout, findings, termui.Options{Wide: true, NoColor: opts.noColor})
		fmt.Fprintln(stdout, "\nTip: run `kubectl fixora why <resource> --proof` for an incident detail view.")
		return 0
	case "cost":
		return runCost(ctx, stdout, stderr, opts, a, rest)
	case "predict":
		predictions, err := a.Predict(ctx)
		return output.WriteOrError(stdout, stderr, opts.output, predictions, err)
	case "lint":
		if len(rest) == 0 {
			fmt.Fprintln(stderr, "error: lint requires -f, --helm, or --kustomize path")
			return 2
		}
		results, err := analyzer.Lint(rest)
		return output.WriteOrError(stdout, stderr, opts.output, results, err)
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n", cmd)
		printHelp(stderr)
		return 2
	}
}

func parseFlags(args []string) (options, []string, error) {
	opts := options{output: "text", namespace: "default", redact: true}
	fs := flag.NewFlagSet("kubectl-fixora", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&opts.namespace, "namespace", "default", "namespace")
	fs.StringVar(&opts.namespace, "n", "default", "namespace")
	fs.BoolVar(&opts.allNS, "all-namespaces", false, "scan all namespaces")
	fs.BoolVar(&opts.allNS, "A", false, "scan all namespaces")
	fs.StringVar(&opts.context, "context", "", "kube context")
	fs.StringVar(&opts.output, "output", "text", "output format: text, json, yaml, markdown")
	fs.StringVar(&opts.output, "o", "text", "output format")
	fs.BoolVar(&opts.includeLogs, "include-logs", false, "include bounded pod logs")
	fs.BoolVar(&opts.useAI, "ai", false, "use OpenAI-compatible AI analysis")
	fs.BoolVar(&opts.autoFix, "auto-fix", false, "generate an explicit local fix plan")
	fs.BoolVar(&opts.apply, "apply", false, "apply generated local patch")
	fs.StringVar(&opts.outFile, "out", "", "output file")
	fs.BoolVar(&opts.verbose, "verbose", false, "verbose output")
	fs.BoolVar(&opts.redact, "redact", true, "redact sensitive values")
	fs.StringVar(&opts.filters, "filter", "", "comma-separated analyzer filters")
	fs.StringVar(&opts.filters, "filters", "", "comma-separated analyzer filters")
	fs.BoolVar(&opts.wide, "wide", false, "wide terminal output")
	fs.BoolVar(&opts.noColor, "no-color", false, "disable terminal color")
	fs.BoolVar(&opts.proof, "proof", false, "show evidence proof")
	fs.BoolVar(&opts.paranoid, "paranoid", false, "avoid secret-sensitive evidence and force redaction")
	fs.BoolVar(&opts.preview, "preview", false, "preview patch plan without writing")
	fs.StringVar(&opts.repoPath, "repo", "", "local manifest/chart/kustomize repo path")
	fs.StringVar(&opts.branch, "branch", "", "local git branch to create for PR-ready output")
	fs.BoolVar(&opts.commit, "commit", false, "commit local repo changes")
	fs.StringVar(&opts.profile, "profile", "", "AI prompt profile")
	fs.IntVar(&opts.aiBudget, "ai-budget-tokens", 0, "maximum estimated AI prompt tokens")
	fs.StringVar(&opts.container, "container", "", "target container for concrete patch generation")
	fs.StringVar(&opts.image, "image", "", "pinned replacement image for concrete image patch")
	fs.StringVar(&opts.memRequest, "memory-request", "", "memory request for concrete resource patch")
	fs.StringVar(&opts.memLimit, "memory-limit", "", "memory limit for concrete resource patch")
	fs.StringVar(&opts.cpuRequest, "cpu-request", "", "cpu request for concrete resource patch")
	fs.StringVar(&opts.envName, "env-name", "", "environment variable name for concrete env patch")
	fs.StringVar(&opts.configMap, "configmap", "", "ConfigMap name for concrete env patch")
	fs.StringVar(&opts.configKey, "config-key", "", "ConfigMap key for concrete env patch")
	if err := fs.Parse(args); err != nil {
		return opts, nil, err
	}
	if opts.allNS {
		opts.namespace = ""
	}
	return opts, fs.Args(), nil
}

func runStatus(ctx context.Context, stdout, stderr io.Writer, opts options, k kube.Kubectl) int {
	status, err := k.Status(ctx)
	return output.WriteOrError(stdout, stderr, opts.output, status, err)
}

func runDoctor(ctx context.Context, stdout, stderr io.Writer, opts options, k kube.Kubectl) int {
	checks, err := k.Doctor(ctx, opts.namespace, opts.allNS)
	return output.WriteOrError(stdout, stderr, opts.output, checks, err)
}

func runDoctorReport(ctx context.Context, stdout, stderr io.Writer, opts options, k kube.Kubectl) int {
	report, err := k.DoctorReport(ctx, opts.namespace, opts.allNS)
	return output.WriteOrError(stdout, stderr, opts.output, report, err)
}

func runCost(ctx context.Context, stdout, stderr io.Writer, opts options, a analyzer.Analyzer, rest []string) int {
	costs, err := a.Cost(ctx, rest)
	return output.WriteOrError(stdout, stderr, opts.output, costs, err)
}

func writeFindings(stdout, stderr io.Writer, opts options, findings []analyzer.Finding, err error) int {
	if err == nil && opts.output == "text" {
		termui.Findings(stdout, findings, termui.Options{Wide: opts.wide, NoColor: opts.noColor})
		return 0
	}
	return output.WriteOrError(stdout, stderr, opts.output, findings, err)
}

func augmentWithAI(ctx context.Context, finding *analyzer.Finding, opts options, stderr io.Writer) {
	if opts.aiBudget > 0 && estimateTokens(*finding) > opts.aiBudget {
		if opts.verbose {
			fmt.Fprintf(stderr, "ai skipped: estimated prompt exceeds --ai-budget-tokens\n")
		}
		return
	}
	if opts.profile != "" {
		cfg, _ := config.Load()
		cfg.Profile = opts.profile
		_ = config.Save(cfg)
	}
	cfg, _ := config.Load()
	if cfg.CacheEnabled {
		store := cache.New()
		var cached analyzer.AIResult
		if store.Get(cache.Key(*finding), &cached) {
			finding.AI = &cached
			return
		}
	}
	client, err := ai.NewFromEnv()
	if err != nil {
		if opts.verbose {
			fmt.Fprintf(stderr, "ai disabled: %v\n", err)
		}
		return
	}
	result, err := client.Explain(ctx, *finding)
	if err != nil {
		if opts.verbose {
			fmt.Fprintf(stderr, "ai failed: %v\n", err)
		}
		return
	}
	finding.AI = result
	if cfg.CacheEnabled {
		_ = cache.New().Set(cache.Key(*finding), result)
	}
}

func runAI(args []string, stdout, stderr io.Writer) int {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if len(args) == 0 || args[0] == "doctor" {
		return output.Write(stdout, "json", ai.Doctor(ctx))
	}
	if args[0] == "profiles" {
		return output.Write(stdout, "json", config.Profiles())
	}
	fmt.Fprintf(stderr, "error: unknown ai command %q\n", args[0])
	return 2
}

func runMemory(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "clear" {
		if err := memory.Clear(); err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, "memory cleared")
		return 0
	}
	records, err := memory.List()
	return output.WriteOrError(stdout, stderr, "json", records, err)
}

func runAuth(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" {
		fmt.Fprintln(stdout, "usage: kubectl fixora auth set <provider> <api-key> [base-url] [model]")
		return 0
	}
	if args[0] != "set" {
		fmt.Fprintf(stderr, "error: unknown auth command %q\n", args[0])
		return 2
	}
	if err := config.Auth(args[1:]); err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, "AI credentials saved")
	return 0
}

func runConfig(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "view" {
		cfg, err := config.Load()
		if err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
		return output.Write(stdout, "json", config.Public(cfg))
	}
	if args[0] == "path" {
		path, err := config.Path()
		if err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, path)
		return 0
	}
	if args[0] == "set" {
		if err := config.Set(args[1:]); err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, "configuration updated")
		return 0
	}
	fmt.Fprintf(stderr, "error: unknown config command %q\n", args[0])
	return 2
}

func runCache(args []string, stdout, stderr io.Writer) int {
	store := cache.New()
	if len(args) == 0 || args[0] == "path" {
		fmt.Fprintln(stdout, store.Dir)
		return 0
	}
	if args[0] == "stats" {
		return output.Write(stdout, "json", store.Stats())
	}
	if args[0] == "clear" {
		if err := store.Clear(); err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, "cache cleared")
		return 0
	}
	fmt.Fprintf(stderr, "error: unknown cache command %q\n", args[0])
	return 2
}

func runCustomAnalyzers(ctx context.Context, stdout, stderr io.Writer, opts options, a analyzer.Analyzer, rest []string) int {
	if len(rest) == 0 || rest[0] == "list" {
		items, err := custom.List()
		return output.WriteOrError(stdout, stderr, opts.output, items, err)
	}
	if rest[0] == "add" {
		if len(rest) < 2 {
			fmt.Fprintln(stderr, "error: custom-analyzers add requires executable path")
			return 2
		}
		if err := config.AddCustomAnalyzer(rest[1]); err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, "custom analyzer added")
		return 0
	}
	if rest[0] == "run" {
		if len(rest) < 2 {
			fmt.Fprintln(stderr, "error: custom-analyzers run requires resource, for example pod/api")
			return 2
		}
		finding, err := a.AnalyzeResource(ctx, rest[1])
		if err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
		results, err := custom.Run(ctx, finding)
		return output.WriteOrError(stdout, stderr, opts.output, results, err)
	}
	fmt.Fprintf(stderr, "error: unknown custom-analyzers command %q\n", rest[0])
	return 2
}

func printHelp(w io.Writer) {
	fmt.Fprintf(w, `%s %s

Standalone free kubectl plugin for local Kubernetes diagnostics.

Usage:
  kubectl fixora <command> [flags] [resource]

Commands:
  status                       Show cluster access and capability summary
  doctor                       Validate RBAC, metrics, logs, events, Helm/GitOps CRDs
  filters                      List available analyzers and active filter selection
  integrations                 Detect local optional integrations and CRDs
  custom-analyzers list|add|run Manage explicit local custom analyzer executables
  serve [addr]                 Serve a local-only HTTP API for incidents/analyze
  why <kind/name>              Explain what is broken, why, proof, and next step
  graph <kind/name>            Show dependency graph as text, JSON, YAML, or Mermaid
  trace <resource>             Debug Ingress/HTTPRoute/Service connectivity path
  storage                      Debug PVC/PV/StorageClass issues
  rbac [sa] [verb] [resource]  Debug service account authorization
  dns                          Debug Service DNS and CoreDNS signals
  security                     Debug policy and securityContext failures
  node-pressure                Debug node pressure, readiness, and eviction signals
  repo [path]                  Detect raw, Helm, or Kustomize source mode
  validate [path]              Render/validate local raw, Helm, or Kustomize source
  ui                           Show a compact terminal incident dashboard
  bundle <kind/name>           Write a redacted audit bundle
  incidents                    List current failing workloads
  analyze <kind/name>          Analyze one resource locally
  explain <kind/name> --ai     Analyze and ask an OpenAI-compatible AI for explanation
  plan <kind/name>             Build a safe local remediation plan
  diff <kind/name>             Show suggested local patch diff
  patch <kind/name> --out file Write a suggested local patch file
  report <kind/name> --out md  Export a local markdown report
  cost nodes|workloads         Estimate node/workload costs
  predict                      Show future-risk signals from local evidence
  lint -f path                 Lint manifests, Helm chart, or Kustomize overlay
  version                      Print version
  auth set provider key        Store AI provider credentials locally
  config view|set|path         Manage local CLI configuration
  cache path|clear             Inspect or clear local AI cache
  ai doctor|profiles           Validate AI setup and list prompt profiles
  memory list|clear            Inspect or clear local scenario memory

Global flags:
  -n, --namespace string       Namespace (default "default")
  -A, --all-namespaces         Scan all namespaces
      --context string         Kube context
  -o, --output string          text, json, yaml, markdown (default "text")
      --include-logs           Include bounded logs in evidence
      --ai                     Use AI via FIXORA_AI_API_KEY and OpenAI-compatible API
      --auto-fix               Generate explicit local fix plan
      --apply                  Apply generated local patch (never default)
      --redact                 Redact sensitive values (default true)
      --filter string          Comma-separated analyzers, for example Pod,Deployment,Service
      --proof                  Show evidence proof
      --paranoid               Force redaction and secret-safe mode
      --repo string            Local repo/chart/overlay path
      --preview                Preview patch plan only
      --container string       Target container for concrete patches
      --image string           Pinned replacement image
      --memory-request string  Concrete memory request
      --memory-limit string    Concrete memory limit
`, version.Name, version.Version)
}

func splitCSV(value string) []string {
	if value == "" {
		return nil
	}
	return []string{value}
}

func firstArg(values []string, fallbacks ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	for _, value := range fallbacks {
		if value != "" {
			return value
		}
	}
	return ""
}

func estimateTokens(f analyzer.Finding) int {
	total := len(f.Summary) + len(f.Status) + len(f.Category)
	for _, ev := range f.Evidence {
		total += len(ev.Label) + len(ev.Value)
	}
	for _, log := range f.Logs {
		total += len(log.Text)
	}
	if total == 0 {
		return 0
	}
	return total/4 + 1
}

func concreteOptions(opts options) fix.ConcreteOptions {
	return fix.ConcreteOptions{
		Container:     opts.container,
		Image:         opts.image,
		MemoryRequest: opts.memRequest,
		MemoryLimit:   opts.memLimit,
		CPURequest:    opts.cpuRequest,
		EnvName:       opts.envName,
		ConfigMap:     opts.configMap,
		ConfigKey:     opts.configKey,
	}
}
