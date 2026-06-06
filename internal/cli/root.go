package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
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
	"github.com/fixora/kubectl-fixora/internal/mcp"
	"github.com/fixora/kubectl-fixora/internal/memory"
	"github.com/fixora/kubectl-fixora/internal/ops"
	"github.com/fixora/kubectl-fixora/internal/output"
	"github.com/fixora/kubectl-fixora/internal/repo"
	"github.com/fixora/kubectl-fixora/internal/report"
	"github.com/fixora/kubectl-fixora/internal/server"
	"github.com/fixora/kubectl-fixora/internal/shadow"
	"github.com/fixora/kubectl-fixora/internal/termui"
	"github.com/fixora/kubectl-fixora/internal/version"
)

type options struct {
	namespace     string
	allNS         bool
	context       string
	output        string
	includeLogs   bool
	useAI         bool
	autoFix       bool
	apply         bool
	yes           bool
	outFile       string
	verbose       bool
	redact        bool
	unsafeAI      bool
	filters       string
	labelSelector string
	wide          bool
	noColor       bool
	proof         bool
	paranoid      bool
	preview       bool
	forceRisky    bool
	typedClient   bool
	tui           bool
	repoPath      string
	strategy      string
	branch        string
	commit        bool
	mcp           bool
	profile       string
	aiBudget      int
	container     string
	image         string
	memRequest    string
	memLimit      string
	cpuRequest    string
	envName       string
	configMap     string
	configKey     string
	timeout       time.Duration
	logTail       int
	maxLogBytes   int
	applyDryRun   bool
	sourcePatch   bool
	shadowVerify  bool
	shadowTimeout time.Duration
	shadowRetries int
	keepShadow    bool
	shadowEgress  string
	delivery      string
	prBase        string
	prTitle       string
	watchInterval time.Duration
	lintFiles     listFlag
	maxFindings   int
	quick         bool
	safe          bool
	gitops        bool
	visited       map[string]bool
}

type listFlag []string

func (l *listFlag) String() string {
	return fmt.Sprint([]string(*l))
}

func (l *listFlag) Set(value string) error {
	*l = append(*l, value)
	return nil
}

func Execute(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printHelp(stdout)
		return 0
	}
	if args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		if hasArg(args[1:], "--advanced") || hasArg(args[1:], "-a") {
			printAdvancedHelp(stdout)
			return 0
		}
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
	opts, rest, err := parseFlags(reorderFlagArgs(args[1:]))
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 2
	}
	cmd, rest, err = normalizeCommand(cmd, rest)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n\n", err)
		printHelp(stderr)
		return 2
	}
	applyWorkflowDefaults(cmd, &opts)
	baseCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var ctx context.Context
	var cancel context.CancelFunc
	if cmd == "serve" || cmd == "watch" || (cmd == "ui" && opts.tui) {
		ctx, cancel = baseCtx, func() {}
	} else {
		if opts.timeout > 0 {
			ctx, cancel = context.WithTimeout(baseCtx, opts.timeout)
		} else {
			ctx, cancel = baseCtx, func() {}
		}
	}
	defer cancel()

	k := kube.NewKubectl(opts.context)
	k.LogTail = opts.logTail
	k.LogLimitBytes = opts.maxLogBytes
	var reader kube.Reader = k
	if opts.typedClient {
		typed, err := kube.NewTypedClient(opts.context)
		if err != nil {
			fmt.Fprintf(stderr, "error: typed Kubernetes client: %v\n", err)
			return 1
		}
		typed.LogTail = opts.logTail
		typed.LogLimitBytes = opts.maxLogBytes
		reader = typed
	}
	filters := analyzerFiltersForCommand(cmd, rest, opts)
	a := analyzer.New(reader, analyzer.Options{
		Namespace:     opts.namespace,
		AllNS:         opts.allNS,
		IncludeLogs:   opts.includeLogs,
		Redact:        opts.redact || opts.paranoid,
		Filters:       filters,
		LabelSelector: opts.labelSelector,
	})

	switch cmd {
	case "status":
		if opts.output == "text" {
			fmt.Fprintln(stderr, "Gathering status...")
		}
		return runStatus(ctx, stdout, stderr, opts, k)
	case "doctor":
		if opts.output == "text" {
			fmt.Fprintln(stderr, "Running doctor checks...")
		}
		return runDoctorReport(ctx, stdout, stderr, opts, k)
	case "filters", "analyzers":
		return output.Write(stdout, opts.output, analyzer.ListAnalyzers(splitCSV(opts.filters)))
	case "integrations":
		return output.Write(stdout, opts.output, integration.List(ctx, k))
	case "custom-analyzers":
		return runCustomAnalyzers(ctx, stdout, stderr, opts, a, rest)
	case "serve":
		if opts.mcp || len(rest) > 0 && rest[0] == "--mcp" {
			if err := (mcp.Server{Kubectl: k, AnalyzerOpt: analyzer.Options{Namespace: opts.namespace, AllNS: opts.allNS, IncludeLogs: opts.includeLogs, Redact: opts.redact, Filters: splitCSV(opts.filters), LabelSelector: opts.labelSelector}}).ServeStdio(ctx, os.Stdin, stdout); err != nil {
				fmt.Fprintf(stderr, "error: %v\n", err)
				return 1
			}
			return 0
		}
		addr := "127.0.0.1:8089"
		if len(rest) > 0 {
			addr = rest[0]
		}
		fmt.Fprintf(stdout, "serving local Fixora CLI API on http://%s\n", addr)
		err := server.Serve(ctx, server.Options{
			Addr:        addr,
			Kubectl:     k,
			AnalyzerOpt: analyzer.Options{Namespace: opts.namespace, AllNS: opts.allNS, IncludeLogs: opts.includeLogs, Redact: opts.redact, Filters: splitCSV(opts.filters), LabelSelector: opts.labelSelector},
			Token:       os.Getenv("FIXORA_SERVE_TOKEN"),
		})
		if err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
		return 0
	case "incidents":
		if opts.output == "text" {
			fmt.Fprintln(stderr, "Scanning for incidents...")
		}
		scan := a.ScanReport(ctx)
		if opts.output == "text" {
			termui.Findings(stdout, scan.Findings, termui.Options{Wide: opts.wide, NoColor: opts.noColor})
			writeSkipped(stdout, scan.Skipped)
			return 0
		}
		return output.Write(stdout, opts.output, scan.Envelope())
	case "analyze", "explain", "why":
		if len(rest) == 0 {
			fmt.Fprintln(stderr, "error: analyze requires a resource, for example deployment/api")
			return 2
		}
		if opts.output == "text" {
			fmt.Fprintf(stderr, "Analyzing %s...\n", rest[0])
		}
		finding, err := a.AnalyzeResource(ctx, rest[0])
		if err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
		finding = preferSmartFinding(ctx, a, finding)
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
	case "plan", "diff", "patch", "fix", "runbook", "readiness", "rollback":
		if len(rest) == 0 {
			fmt.Fprintf(stderr, "error: %s requires a resource, for example deployment/api\n", cmd)
			return 2
		}
		if opts.output == "text" {
			fmt.Fprintf(stderr, "Analyzing %s...\n", rest[0])
		}
		finding, err := a.AnalyzeResource(ctx, rest[0])
		if err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
		finding = preferSmartFinding(ctx, a, finding)
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
			if opts.repoPath != "" && cmd == "fix" {
				opts.sourcePatch = true
			}
		}
		switch cmd {
		case "runbook":
			fmt.Fprint(stdout, ops.BuildRunbook(finding, plan))
			return 0
		case "readiness":
			return output.Write(stdout, opts.output, ops.FixReadiness(finding, plan))
		case "rollback":
			rollback := ops.BuildRollback(finding, plan, opts.apply)
			if opts.apply {
				if rollback.Command == "" {
					fmt.Fprintln(stderr, "error: no rollback command available")
					return 1
				}
				if _, err := executeRollback(ctx, k, rollback); err != nil {
					fmt.Fprintf(stderr, "error: rollback failed: %v\n", err)
					return 1
				}
				fmt.Fprintln(stdout, "rollback command executed")
				return 0
			}
			return output.Write(stdout, opts.output, rollback)
		}
		if cmd == "diff" {
			return output.Write(stdout, opts.output, plan.DiffView())
		}
		if cmd == "fix" {
			return runGuidedFix(ctx, stdout, stderr, opts, k, finding, plan, rest[0])
		}
		if cmd == "patch" {
			if opts.preview {
				termui.Plan(stdout, plan, termui.Options{Wide: opts.wide, NoColor: opts.noColor})
				return 0
			}
			if opts.outFile == "" {
				opts.outFile = "fixora-patch.yaml"
			}
			if opts.sourcePatch && !opts.shadowVerify {
				sourcePatch, err := repo.WriteSourcePatch(opts.repoPath, opts.outFile, finding, plan)
				if err != nil {
					fmt.Fprintf(stderr, "error: source patch: %v\n", err)
					return 1
				}
				return output.Write(stdout, opts.output, sourcePatch)
			}
			if err := os.WriteFile(opts.outFile, []byte(plan.PatchYAML()), 0o600); err != nil {
				fmt.Fprintf(stderr, "error: write patch: %v\n", err)
				return 1
			}
			fmt.Fprintf(stdout, "wrote %s\n", opts.outFile)
			if opts.shadowVerify {
				return runShadowWorkflow(ctx, stdout, stderr, opts, k, finding, plan)
			}
			if opts.apply {
				if !plan.ApplyEligible {
					fmt.Fprintln(stderr, "error: generated patch is not eligible for production apply; review blockedReasons, provide concrete values, or use --force-risky only after approval")
					return 1
				}
				if opts.applyDryRun {
					if err := k.DryRunApply(ctx, opts.outFile); err != nil {
						fmt.Fprintf(stderr, "error: server dry-run rejected patch: %v\n", err)
						return 1
					}
				}
				if termui.ConfirmApply(ctx, k, plan.PatchTemplate, os.Stdin, stdout) {
					if err := k.Apply(ctx, opts.outFile); err != nil {
						fmt.Fprintf(stderr, "error: apply patch: %v\n", err)
						return 1
					}
					fmt.Fprintln(stdout, "applied patch")
				}
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
	case "health":
		scan := a.ScanReport(ctx)
		return output.Write(stdout, opts.output, ops.BuildHealth(ctx, k, scan, opts.namespace))
	case "changes":
		if len(rest) == 0 {
			fmt.Fprintln(stderr, "error: changes requires a resource, for example deployment/api")
			return 2
		}
		changes, err := ops.DetectChanges(ctx, k, opts.namespace, rest[0])
		return output.WriteOrError(stdout, stderr, opts.output, changes, err)
	case "preflight", "policy-check":
		paths := append([]string{}, opts.lintFiles...)
		paths = append(paths, rest...)
		if len(paths) == 0 {
			fmt.Fprintf(stderr, "error: %s requires -f path\n", cmd)
			return 2
		}
		if cmd == "policy-check" {
			results, err := analyzer.Lint(paths)
			return output.WriteOrError(stdout, stderr, opts.output, results, err)
		}
		results := []ops.Preflight{}
		for _, path := range paths {
			results = append(results, ops.RunPreflight(ctx, k, path))
		}
		return output.Write(stdout, opts.output, results)
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
		if err := bundle.WriteProfile(ctx, k, out, finding, plan, firstArg([]string{opts.profile}, "incident")); err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "wrote %s\n", out)
		return 0
	case "cluster":
		opts.tui = true
		opts.allNS = true
		opts.namespace = ""
		fallthrough
	case "ui":
		if opts.tui {
			if err := termui.RunTUI(ctx, reader, termui.TUIOptions{
				Context:       opts.context,
				Namespace:     opts.namespace,
				AllNS:         opts.allNS,
				IncludeLogs:   opts.includeLogs,
				Redact:        opts.redact,
				UnsafeAI:      opts.unsafeAI,
				Filters:       splitCSV(opts.filters),
				LabelSelector: opts.labelSelector,
				Refresh:       opts.watchInterval,
				ScanTimeout:   opts.timeout,
				ApplyDryRun:   opts.applyDryRun,
				ShadowTimeout: opts.shadowTimeout,
				ShadowRetries: opts.shadowRetries,
				KeepShadow:    opts.keepShadow,
				ShadowEgress:  opts.shadowEgress,
				RepoPath:      opts.repoPath,
				Branch:        opts.branch,
				PRBase:        opts.prBase,
				PRTitle:       opts.prTitle,
				AIProvider:    os.Getenv("FIXORA_AI_PROVIDER"),
				Output:        stdout,
			}); err != nil {
				fmt.Fprintf(stderr, "error: tui: %v\n", err)
				return 1
			}
			return 0
		}
		scan := a.ScanReport(ctx)
		termui.Findings(stdout, scan.Findings, termui.Options{Wide: true, NoColor: opts.noColor})
		writeSkipped(stdout, scan.Skipped)
		fmt.Fprintln(stdout, "\nTip: run `kubectl fixora ui --tui -A --include-logs` for the interactive production triage dashboard.")
		return 0
	case "watch":
		return runWatch(ctx, stdout, stderr, opts, a, rest)
	case "cost":
		return runCost(ctx, stdout, stderr, opts, a, rest)
	case "predict":
		predictions, err := a.Predict(ctx)
		return output.WriteOrError(stdout, stderr, opts.output, predictions, err)
	case "lint":
		paths := append([]string{}, opts.lintFiles...)
		paths = append(paths, rest...)
		if len(paths) == 0 {
			fmt.Fprintln(stderr, "error: lint requires -f, --helm, or --kustomize path")
			return 2
		}
		if opts.output == "text" {
			fmt.Fprintln(stderr, "Linting files...")
		}
		results, err := analyzer.Lint(paths)
		return output.WriteOrError(stdout, stderr, opts.output, results, err)
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n", cmd)
		printHelp(stderr)
		return 2
	}
}

func parseFlags(args []string) (options, []string, error) {
	cfg, _ := config.Load()
	timeout, err := time.ParseDuration(cfg.Timeout)
	if err != nil || timeout <= 0 {
		timeout = 90 * time.Second
	}
	defaultOutput := cfg.DefaultOutput
	if defaultOutput == "" {
		defaultOutput = "text"
	}
	opts := options{
		output:      defaultOutput,
		namespace:   "default",
		redact:      cfg.Redact,
		paranoid:    cfg.Paranoid,
		timeout:     timeout,
		logTail:     cfg.LogTail,
		maxLogBytes: cfg.MaxLogBytes,
		applyDryRun: cfg.ApplyDryRun,
	}
	if opts.logTail <= 0 {
		opts.logTail = 120
	}
	if opts.maxLogBytes <= 0 {
		opts.maxLogBytes = 24000
	}
	fs := flag.NewFlagSet("kubectl-fixora", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&opts.namespace, "namespace", "default", "namespace")
	fs.StringVar(&opts.namespace, "n", "default", "namespace")
	fs.BoolVar(&opts.allNS, "all-namespaces", false, "scan all namespaces")
	fs.BoolVar(&opts.allNS, "A", false, "scan all namespaces")
	fs.StringVar(&opts.context, "context", "", "kube context")
	fs.StringVar(&opts.output, "output", opts.output, "output format: text, json, yaml, markdown")
	fs.StringVar(&opts.output, "o", opts.output, "output format")
	fs.BoolVar(&opts.includeLogs, "include-logs", false, "include bounded pod logs")
	fs.BoolVar(&opts.useAI, "ai", false, "use OpenAI-compatible AI analysis")
	fs.BoolVar(&opts.autoFix, "auto-fix", false, "generate an explicit local fix plan")
	fs.BoolVar(&opts.apply, "apply", false, "apply generated local patch")
	fs.BoolVar(&opts.yes, "yes", false, "confirm non-interactive verified PR/MR delivery")
	fs.StringVar(&opts.outFile, "out", "", "output file")
	fs.BoolVar(&opts.verbose, "verbose", false, "verbose output")
	fs.BoolVar(&opts.redact, "redact", opts.redact, "redact sensitive values")
	fs.BoolVar(&opts.unsafeAI, "unsafe-ai-no-redact", false, "allow AI calls with unredacted cluster data")
	fs.StringVar(&opts.filters, "filter", "", "comma-separated analyzer filters")
	fs.StringVar(&opts.filters, "filters", "", "comma-separated analyzer filters")
	fs.StringVar(&opts.labelSelector, "selector", "", "label selector for analyzer resource lists")
	fs.StringVar(&opts.labelSelector, "l", "", "label selector for analyzer resource lists")
	fs.StringVar(&opts.labelSelector, "L", "", "label selector for analyzer resource lists")
	fs.BoolVar(&opts.wide, "wide", false, "wide terminal output")
	fs.BoolVar(&opts.noColor, "no-color", false, "disable terminal color")
	fs.BoolVar(&opts.proof, "proof", false, "show evidence proof")
	fs.BoolVar(&opts.paranoid, "paranoid", opts.paranoid, "avoid secret-sensitive evidence and force redaction")
	fs.BoolVar(&opts.preview, "preview", false, "preview patch plan without writing")
	fs.BoolVar(&opts.forceRisky, "force-risky", false, "allow risky concrete fixes to pass apply eligibility after review")
	fs.BoolVar(&opts.typedClient, "typed-client", false, "use client-go/controller-runtime typed client for analyzer reads")
	fs.BoolVar(&opts.tui, "tui", false, "enable interactive terminal dashboard for the ui command")
	fs.BoolVar(&opts.quick, "quick", false, "use fast incident defaults")
	fs.BoolVar(&opts.safe, "safe", false, "use production-safe defaults")
	fs.BoolVar(&opts.gitops, "gitops", false, "prefer GitOps source patch delivery")
	fs.StringVar(&opts.repoPath, "repo", "", "local manifest/chart/kustomize repo path")
	fs.StringVar(&opts.strategy, "strategy", "", "fix strategy such as rollback, right-size, repair-selector, add-requests")
	fs.StringVar(&opts.branch, "branch", "", "local git branch to create for PR-ready output")
	fs.BoolVar(&opts.commit, "commit", false, "commit local repo changes")
	fs.BoolVar(&opts.mcp, "mcp", false, "serve MCP stdio mode")
	fs.StringVar(&opts.profile, "profile", "", "AI prompt profile or bundle profile")
	fs.IntVar(&opts.aiBudget, "ai-budget-tokens", 0, "maximum estimated AI prompt tokens")
	fs.StringVar(&opts.container, "container", "", "target container for concrete patch generation")
	fs.StringVar(&opts.image, "image", "", "pinned replacement image for concrete image patch")
	fs.StringVar(&opts.memRequest, "memory-request", "", "memory request for concrete resource patch")
	fs.StringVar(&opts.memLimit, "memory-limit", "", "memory limit for concrete resource patch")
	fs.StringVar(&opts.cpuRequest, "cpu-request", "", "cpu request for concrete resource patch")
	fs.StringVar(&opts.envName, "env-name", "", "environment variable name for concrete env patch")
	fs.StringVar(&opts.configMap, "configmap", "", "ConfigMap name for concrete env patch")
	fs.StringVar(&opts.configKey, "config-key", "", "ConfigMap key for concrete env patch")
	fs.DurationVar(&opts.timeout, "timeout", opts.timeout, "overall command timeout, for example 30s or 2m")
	fs.IntVar(&opts.logTail, "log-tail", opts.logTail, "pod log lines to collect when --include-logs is set")
	fs.IntVar(&opts.maxLogBytes, "max-logs-bytes", opts.maxLogBytes, "maximum bytes to collect per pod log stream")
	fs.BoolVar(&opts.applyDryRun, "apply-dry-run", opts.applyDryRun, "run server-side dry-run before --apply")
	fs.BoolVar(&opts.sourcePatch, "source-patch", false, "write patch into --repo for GitOps source review")
	fs.BoolVar(&opts.shadowVerify, "shadow", false, "deploy an isolated shadow clone and verify the patch before delivery")
	fs.DurationVar(&opts.shadowTimeout, "shadow-timeout", 5*time.Minute, "shadow clone verification timeout")
	fs.IntVar(&opts.shadowRetries, "shadow-retries", 0, "number of shadow re-clone attempts after failure")
	fs.BoolVar(&opts.keepShadow, "keep-shadow", false, "keep shadow Pod and NetworkPolicy after verification")
	fs.StringVar(&opts.shadowEgress, "shadow-egress", "allow", "shadow egress policy: allow or deny")
	fs.StringVar(&opts.delivery, "delivery", "patch", "verified shadow delivery: patch, cluster, or pr")
	fs.StringVar(&opts.prBase, "pr-base", "", "base branch for --delivery=pr")
	fs.StringVar(&opts.prTitle, "pr-title", "", "pull request title for --delivery=pr")
	fs.DurationVar(&opts.watchInterval, "watch-interval", 5*time.Second, "watch polling interval")
	fs.IntVar(&opts.maxFindings, "max-findings", 8, "maximum findings to display in watch mode")
	fs.Var(&opts.lintFiles, "f", "manifest, chart, or kustomize path to lint")
	fs.Var(&opts.lintFiles, "filename", "manifest, chart, or kustomize path to lint")
	if err := fs.Parse(args); err != nil {
		return opts, nil, err
	}
	visited := visitedFlags(fs)
	opts.visited = visited
	if settings := cfg.ContextSettings(opts.context); opts.context != "" {
		if settings.Namespace != "" && !visited["namespace"] && !visited["n"] {
			opts.namespace = settings.Namespace
		}
		if settings.DefaultOutput != "" && !visited["output"] && !visited["o"] {
			opts.output = settings.DefaultOutput
		}
		if settings.Redact != nil && !visited["redact"] {
			opts.redact = *settings.Redact
		}
		if settings.Paranoid != nil && !visited["paranoid"] {
			opts.paranoid = *settings.Paranoid
		}
		if settings.Timeout != "" && !visited["timeout"] {
			if timeout, err := time.ParseDuration(settings.Timeout); err == nil {
				opts.timeout = timeout
			}
		}
		if settings.LogTail != nil && !visited["log-tail"] {
			opts.logTail = *settings.LogTail
		}
		if settings.MaxLogBytes != nil && !visited["max-logs-bytes"] {
			opts.maxLogBytes = *settings.MaxLogBytes
		}
		if settings.ApplyDryRun != nil && !visited["apply-dry-run"] {
			opts.applyDryRun = *settings.ApplyDryRun
		}
	}
	if opts.allNS {
		opts.namespace = ""
	}
	return opts, fs.Args(), nil
}

func normalizeCommand(cmd string, rest []string) (string, []string, error) {
	switch cmd {
	case "scan":
		return "incidents", rest, nil
	case "rca":
		return "why", rest, nil
	case "repair":
		return "fix", rest, nil
	case "dashboard":
		return "cluster", rest, nil
	case "debug":
		if len(rest) == 0 {
			return "", rest, fmt.Errorf("debug requires one of: trace, graph, storage, rbac, dns, security, node-pressure, changes, readiness, rollback")
		}
		sub := rest[0]
		switch sub {
		case "trace", "graph", "storage", "rbac", "dns", "security", "node-pressure", "changes", "readiness", "rollback":
			return sub, rest[1:], nil
		default:
			return "", rest, fmt.Errorf("unknown debug command %q", sub)
		}
	case "source":
		if len(rest) == 0 {
			return "", rest, fmt.Errorf("source requires one of: repo, validate, lint, preflight, policy-check")
		}
		sub := rest[0]
		switch sub {
		case "repo", "validate", "lint", "preflight", "policy-check":
			return sub, rest[1:], nil
		default:
			return "", rest, fmt.Errorf("unknown source command %q", sub)
		}
	default:
		return cmd, rest, nil
	}
}

func applyWorkflowDefaults(cmd string, opts *options) {
	if opts.visited == nil {
		opts.visited = map[string]bool{}
	}
	incidentCmd := cmd == "incidents" || cmd == "why" || cmd == "fix" || cmd == "watch"
	if incidentCmd || opts.quick || opts.safe || opts.gitops {
		if !opts.visited["include-logs"] {
			opts.includeLogs = true
		}
		if !opts.visited["typed-client"] {
			opts.typedClient = true
		}
		if !opts.visited["redact"] {
			opts.redact = true
		}
	}
	if cmd == "fix" || opts.safe {
		if !opts.visited["paranoid"] {
			opts.paranoid = true
		}
	}
	if cmd == "fix" {
		if !opts.visited["shadow"] && !opts.quick {
			opts.shadowVerify = true
		}
		if opts.repoPath != "" || opts.gitops {
			opts.sourcePatch = true
		}
	}
	if opts.gitops {
		opts.sourcePatch = true
		if !opts.visited["shadow"] {
			opts.shadowVerify = false
		}
	}
	if cmd == "ui" || cmd == "cluster" {
		if !opts.visited["typed-client"] {
			opts.typedClient = true
		}
		if !opts.visited["redact"] {
			opts.redact = true
		}
	}
	if cmd == "cluster" {
		opts.tui = true
		opts.allNS = true
		opts.namespace = ""
	}
}

func reorderFlagArgs(args []string) []string {
	valueFlags := map[string]bool{
		"--namespace": true, "-n": true, "--context": true, "--output": true, "-o": true,
		"--out": true, "--filter": true, "--filters": true, "--selector": true, "-l": true, "-L": true, "--repo": true, "--strategy": true,
		"--branch": true, "--profile": true, "--ai-budget-tokens": true, "--container": true,
		"--image": true, "--memory-request": true, "--memory-limit": true, "--cpu-request": true,
		"--env-name": true, "--configmap": true, "--config-key": true, "--timeout": true,
		"--log-tail": true, "--max-logs-bytes": true, "--shadow-timeout": true,
		"--shadow-retries": true, "--shadow-egress": true, "--delivery": true, "--pr-base": true,
		"--pr-title": true, "--watch-interval": true, "--max-findings": true, "-f": true,
		"--filename": true,
	}
	boolFlags := map[string]bool{
		"--all-namespaces": true, "-A": true, "--include-logs": true, "--ai": true,
		"--auto-fix": true, "--apply": true, "--yes": true, "--verbose": true,
		"--redact": true, "--unsafe-ai-no-redact": true, "--wide": true, "--no-color": true,
		"--proof": true, "--paranoid": true, "--preview": true, "--force-risky": true,
		"--typed-client": true, "--tui": true, "--quick": true, "--safe": true,
		"--gitops": true, "--commit": true, "--mcp": true, "--apply-dry-run": true,
		"--source-patch": true, "--shadow": true, "--keep-shadow": true,
	}
	var flags []string
	var positionals []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		name := arg
		if strings.HasPrefix(arg, "--") {
			if idx := strings.Index(arg, "="); idx > 0 {
				name = arg[:idx]
			}
		}
		switch {
		case strings.Contains(arg, "=") && valueFlags[name]:
			flags = append(flags, arg)
		case valueFlags[arg]:
			flags = append(flags, arg)
			if i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
		case boolFlags[name] || boolFlags[arg]:
			flags = append(flags, arg)
		case strings.HasPrefix(arg, "-"):
			flags = append(flags, arg)
		default:
			positionals = append(positionals, arg)
		}
	}
	return append(flags, positionals...)
}

func visitedFlags(fs *flag.FlagSet) map[string]bool {
	visited := map[string]bool{}
	fs.Visit(func(f *flag.Flag) {
		visited[f.Name] = true
	})
	return visited
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

func runShadowWorkflow(ctx context.Context, stdout, stderr io.Writer, opts options, k kube.Kubectl, finding analyzer.Finding, plan fix.Plan) int {
	if !plan.ApplyEligible {
		fmt.Fprintln(stderr, "error: shadow verification requires an apply-eligible concrete patch; provide concrete values or use --force-risky after approval")
		return 1
	}
	mode := shadow.DeliveryMode(strings.ToLower(strings.TrimSpace(opts.delivery)))
	if mode == "" {
		mode = shadow.DeliveryPatch
	}
	if mode != shadow.DeliveryPatch && mode != shadow.DeliveryCluster && mode != shadow.DeliveryPR {
		fmt.Fprintf(stderr, "error: unsupported --delivery %q; use patch, cluster, or pr\n", opts.delivery)
		return 2
	}
	if mode == shadow.DeliveryPR {
		if opts.repoPath == "" {
			fmt.Fprintln(stderr, "error: --delivery=pr requires --repo")
			return 2
		}
		if !opts.yes {
			fmt.Fprintln(stderr, "error: --delivery=pr requires --yes because it commits, pushes, and opens a review request")
			return 2
		}
	}
	diff := shadow.PatchDiff(plan.Resource, plan.PatchYAML())
	if !termui.ConfirmShadowDeploy(diff, os.Stdin, stdout) {
		fmt.Fprintln(stdout, "shadow verification cancelled")
		return 0
	}
	typed, err := kube.NewRequiredTypedClient(opts.context, "shadow verification")
	if err != nil {
		fmt.Fprintf(stderr, "error: typed Kubernetes client: %v\n", err)
		return 1
	}
	typed.LogTail = opts.logTail
	typed.LogLimitBytes = opts.maxLogBytes
	req := shadow.Request{
		Namespace:   finding.Namespace,
		Resource:    plan.Resource,
		Patch:       plan.PatchYAML(),
		Finding:     finding,
		Plan:        plan,
		Timeout:     opts.shadowTimeout,
		Retries:     opts.shadowRetries,
		Keep:        opts.keepShadow,
		Egress:      opts.shadowEgress,
		Delivery:    mode,
		RepoPath:    opts.repoPath,
		Branch:      opts.branch,
		PRBase:      opts.prBase,
		PRTitle:     opts.prTitle,
		OutFile:     opts.outFile,
		ApplyDryRun: opts.applyDryRun,
		Redact:      opts.redact || opts.paranoid,
	}
	result, err := shadow.Run(ctx, typed, req)
	if err != nil {
		fmt.Fprintf(stderr, "error: shadow verification: %v\n", err)
		return 1
	}
	if !result.Verified {
		_ = output.Write(stdout, opts.output, result)
		return 1
	}
	switch mode {
	case shadow.DeliveryPatch:
		if opts.output == "text" {
			fmt.Fprintf(stdout, "Fix Verified - Parity %d%%\n", result.Parity)
			for _, warning := range result.Warnings {
				fmt.Fprintf(stdout, "warning: %s\n", warning)
			}
			return 0
		}
		return output.Write(stdout, opts.output, result)
	case shadow.DeliveryCluster:
		if opts.applyDryRun {
			if err := k.DryRunApply(ctx, opts.outFile); err != nil {
				fmt.Fprintf(stderr, "error: server dry-run rejected verified patch: %v\n", err)
				return 1
			}
		}
		if !termui.ConfirmApply(ctx, k, plan.PatchTemplate, os.Stdin, stdout) {
			fmt.Fprintln(stdout, "verified apply cancelled")
			return 0
		}
		if err := k.Apply(ctx, opts.outFile); err != nil {
			fmt.Fprintf(stderr, "error: apply verified patch: %v\n", err)
			return 1
		}
		result.Delivery = "cluster"
		if opts.output == "text" {
			fmt.Fprintf(stdout, "Fix Verified - Parity %d%%\napplied verified patch\n", result.Parity)
			return 0
		}
		return output.Write(stdout, opts.output, result)
	case shadow.DeliveryPR:
		sourcePatch, err := repo.WriteSourcePatch(opts.repoPath, opts.outFile, finding, plan)
		if err != nil {
			fmt.Fprintf(stderr, "error: source patch: %v\n", err)
			return 1
		}
		branch := firstArg([]string{opts.branch, defaultShadowBranch(finding)})
		if err := repo.PrepareBranchFiles(ctx, opts.repoPath, branch, true, "fixora: verified remediation for "+finding.ResourceKind+"/"+finding.ResourceName, []string{sourcePatch.Path}); err != nil {
			fmt.Fprintf(stderr, "error: repo workflow: %v\n", err)
			return 1
		}
		pr, err := repo.OpenPullRequest(ctx, opts.repoPath, branch, opts.prBase, firstArg([]string{opts.prTitle}, "fixora: verified remediation for "+finding.ResourceKind+"/"+finding.ResourceName), prBody(result, sourcePatch), true)
		if err != nil {
			fmt.Fprintf(stderr, "error: open pull request: %v\n", err)
			return 1
		}
		result.Delivery = "pr"
		result.PRURL = pr.URL
		result.Warnings = append(result.Warnings, pr.Warnings...)
		if opts.output == "text" {
			fmt.Fprintf(stdout, "Fix Verified - Parity %d%%\n", result.Parity)
			if pr.URL != "" {
				fmt.Fprintf(stdout, "opened PR: %s\n", pr.URL)
			} else {
				fmt.Fprintf(stdout, "prepared PR branch: %s\n", pr.Branch)
			}
			return 0
		}
		return output.Write(stdout, opts.output, result)
	}
	return output.Write(stdout, opts.output, result)
}

func runGuidedFix(ctx context.Context, stdout, stderr io.Writer, opts options, k kube.Kubectl, finding analyzer.Finding, plan fix.Plan, resourceArg string) int {
	if opts.output != "text" {
		return output.Write(stdout, opts.output, plan)
	}

	fmt.Fprintln(stdout, "Fixora incident fix")
	fmt.Fprintln(stdout, strings.Repeat("=", 20))
	termui.Why(stdout, finding, plan, true, termui.Options{Wide: true, NoColor: opts.noColor})
	termui.Plan(stdout, plan, termui.Options{Wide: true, NoColor: opts.noColor})
	if strings.TrimSpace(plan.PatchYAML()) != "" {
		fmt.Fprintln(stdout, "\nSuggested diff")
		fmt.Fprint(stdout, shadow.PatchDiff(plan.Resource, plan.PatchYAML()))
	}

	if opts.preview {
		return 0
	}

	if !plan.ApplyEligible {
		fmt.Fprintln(stdout, "\nNo production mutation was attempted.")
		fmt.Fprintln(stdout, "Fixora needs a concrete, safe patch before shadow verification or apply.")
		if hint := nextConcreteFixHint(resourceArg, plan); hint != "" {
			fmt.Fprintf(stdout, "\nNext command:\n  %s\n", hint)
		}
		return 0
	}

	if opts.outFile == "" {
		opts.outFile = "fixora-patch.yaml"
	}
	if err := os.WriteFile(opts.outFile, []byte(plan.PatchYAML()), 0o600); err != nil {
		fmt.Fprintf(stderr, "error: write patch: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "\nwrote %s\n", opts.outFile)

	if opts.shadowVerify {
		return runShadowWorkflow(ctx, stdout, stderr, opts, k, finding, plan)
	}
	if opts.sourcePatch {
		if opts.repoPath == "" {
			fmt.Fprintln(stderr, "error: --gitops or --source-patch requires --repo")
			return 2
		}
		sourcePatch, err := repo.WriteSourcePatch(opts.repoPath, opts.outFile, finding, plan)
		if err != nil {
			fmt.Fprintf(stderr, "error: source patch: %v\n", err)
			return 1
		}
		return output.Write(stdout, opts.output, sourcePatch)
	}
	if opts.apply {
		if opts.applyDryRun {
			if err := k.DryRunApply(ctx, opts.outFile); err != nil {
				fmt.Fprintf(stderr, "error: server dry-run rejected patch: %v\n", err)
				return 1
			}
		}
		if termui.ConfirmApply(ctx, k, plan.PatchTemplate, os.Stdin, stdout) {
			if err := k.Apply(ctx, opts.outFile); err != nil {
				fmt.Fprintf(stderr, "error: apply patch: %v\n", err)
				return 1
			}
			fmt.Fprintln(stdout, "applied patch")
		}
	}
	_ = memory.Add(finding, plan, "guided-fix")
	return 0
}

func nextConcreteFixHint(resourceArg string, plan fix.Plan) string {
	if strings.TrimSpace(resourceArg) == "" {
		resourceArg = strings.ToLower(plan.Resource)
	}
	base := "kubectl fixora fix " + resourceArg
	if plan.Namespace != "" {
		base += " -n " + plan.Namespace
	}
	switch strings.ToLower(plan.Strategy) {
	case "image":
		return base + " --container <container> --image <pinned-image>"
	case "resources":
		return base + " --container <container> --memory-request <request> --cpu-request <request> --memory-limit <limit>"
	case "env":
		return base + " --container <container> --env-name <name> --configmap <configmap> --config-key <key>"
	default:
		return "kubectl fixora why " + resourceArg + " --proof"
	}
}

func preferSmartFinding(ctx context.Context, a analyzer.Analyzer, base analyzer.Finding) analyzer.Finding {
	if base.ResourceKind == "" || base.ResourceName == "" {
		return base
	}
	report := a.ScanReport(ctx)
	best := base
	bestRank := findingActionRank(base)
	for _, candidate := range report.Findings {
		if !sameFindingTarget(base, candidate) {
			continue
		}
		if rank := findingActionRank(candidate); rank > bestRank {
			best = candidate
			bestRank = rank
		}
	}
	return best
}

func sameFindingTarget(a, b analyzer.Finding) bool {
	if a.Namespace != "" && b.Namespace != "" && a.Namespace != b.Namespace {
		return false
	}
	if a.PodName != "" && b.PodName == a.PodName {
		return true
	}
	return strings.EqualFold(a.ResourceKind, b.ResourceKind) && a.ResourceName == b.ResourceName
}

func findingActionRank(f analyzer.Finding) int {
	rank := 0
	switch strings.ToLower(f.Severity) {
	case "critical":
		rank += 50
	case "high":
		rank += 40
	case "medium":
		rank += 30
	case "low":
		rank += 20
	case "info":
		rank += 10
	}
	if f.Status != "" && !strings.EqualFold(f.Status, "unknown") {
		rank += 5
	}
	if len(f.Evidence) > 1 {
		rank += 3
	}
	if len(f.Recommendations) > 0 {
		rank += 2
	}
	return rank
}

func writeFindings(stdout, stderr io.Writer, opts options, findings []analyzer.Finding, err error) int {
	if err == nil && opts.output == "text" {
		termui.Findings(stdout, findings, termui.Options{Wide: opts.wide, NoColor: opts.noColor})
		return 0
	}
	return output.WriteOrError(stdout, stderr, opts.output, findings, err)
}

func writeSkipped(stdout io.Writer, skipped []analyzer.SkippedCheck) {
	if len(skipped) == 0 {
		return
	}
	fmt.Fprintln(stdout, "\nSkipped checks:")
	for _, item := range skipped {
		fmt.Fprintf(stdout, "- %s: %s\n", item.Name, item.Reason)
	}
}

func runWatch(ctx context.Context, stdout, stderr io.Writer, opts options, a analyzer.Analyzer, rest []string) int {
	if len(rest) == 0 || rest[0] != "incidents" {
		fmt.Fprintln(stderr, "error: watch supports `watch incidents`")
		return 2
	}
	interval := opts.watchInterval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	for {
		scan := a.ScanReport(ctx)
		fmt.Fprint(stdout, "\033[H\033[2J") // clear screen
		fmt.Fprintf(stdout, "\n%s findings=%d high=%d medium=%d skipped=%d\n", time.Now().Format(time.RFC3339), scan.Summary.Findings, scan.Summary.HighSeverity, scan.Summary.MediumSeverity, scan.Summary.SkippedChecks)
		for i, finding := range scan.Findings {
			if i >= opts.maxFindings {
				fmt.Fprintf(stdout, "... %d more findings\n", len(scan.Findings)-opts.maxFindings)
				break
			}
			fmt.Fprintf(stdout, "- %s %s/%s %s\n", finding.Severity, finding.ResourceKind, finding.ResourceName, finding.Status)
		}
		if !ops.SleepOrDone(ctx, interval) {
			return 0
		}
	}
}

func augmentWithAI(ctx context.Context, finding *analyzer.Finding, opts options, stderr io.Writer) {
	if !opts.redact && !opts.unsafeAI {
		if opts.verbose {
			fmt.Fprintln(stderr, "ai disabled: cluster-data AI calls require --redact or explicit --unsafe-ai-no-redact")
		}
		return
	}
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
	aiFinding := *finding
	if opts.redact {
		aiFinding = analyzer.RedactFindingForAI(aiFinding)
	}
	if cfg.CacheEnabled {
		store := cache.New()
		var cached analyzer.AIResult
		if store.Get(cache.Key(aiFinding), &cached) {
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
	result, err := client.Explain(ctx, aiFinding)
	if err != nil {
		if opts.verbose {
			fmt.Fprintf(stderr, "ai failed: %v\n", err)
		}
		return
	}
	finding.AI = result
	if cfg.CacheEnabled {
		_ = cache.New().Set(cache.Key(aiFinding), result)
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
		if hasArg(args[1:], "--resolved") || hasArg(args[1:], "--show-sources") {
			resolved, err := config.Resolved()
			if err != nil {
				fmt.Fprintf(stderr, "error: %v\n", err)
				return 1
			}
			if !hasArg(args[1:], "--show-sources") {
				flat := map[string]any{}
				for key, value := range resolved {
					flat[key] = value.Value
				}
				return output.Write(stdout, "json", flat)
			}
			return output.Write(stdout, "json", resolved)
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
	if args[0] == "unset" {
		if err := config.Unset(args[1:]); err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, "configuration updated")
		return 0
	}
	if args[0] == "reset" {
		if err := config.Reset(); err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, "configuration reset")
		return 0
	}
	if args[0] == "validate" {
		cfg, err := config.Load()
		if err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
		result := config.Validate(cfg)
		code := output.Write(stdout, "json", result)
		if !result.Valid {
			return 1
		}
		return code
	}
	if args[0] == "export" {
		cfg, err := config.Load()
		if err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
		return output.Write(stdout, "json", config.Export(cfg, hasArg(args[1:], "--show-secrets")))
	}
	if args[0] == "profile" {
		result, err := config.ProfileCommand(args[1:])
		return output.WriteOrError(stdout, stderr, "json", result, err)
	}
	if args[0] == "context" {
		result, err := config.ContextCommand(args[1:])
		return output.WriteOrError(stdout, stderr, "json", result, err)
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
	if args[0] == "add" {
		cfg, err := parseRemoteCache(args[1:])
		if err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 2
		}
		if err := store.SetRemote(cfg); err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
		return output.Write(stdout, "json", cfg)
	}
	if args[0] == "get" {
		cfg, err := store.Remote()
		return output.WriteOrError(stdout, stderr, "json", cfg, err)
	}
	if args[0] == "list" {
		return output.Write(stdout, "json", store.List())
	}
	if args[0] == "purge" {
		if len(args) < 2 {
			fmt.Fprintln(stderr, "error: cache purge requires key")
			return 2
		}
		if err := store.Purge(args[1]); err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, "cache entry purged")
		return 0
	}
	if args[0] == "remove" {
		if err := store.RemoveRemote(); err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, "remote cache removed")
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

func parseRemoteCache(args []string) (cache.RemoteConfig, error) {
	if len(args) == 0 {
		return cache.RemoteConfig{}, fmt.Errorf("cache add requires type: s3, azure, gcs, or interplex")
	}
	cfg := cache.RemoteConfig{Type: args[0]}
	for i := 1; i < len(args); i++ {
		key := strings.TrimLeft(args[i], "-")
		if key == "insecure" {
			cfg.Insecure = true
			continue
		}
		if i+1 >= len(args) {
			return cfg, fmt.Errorf("missing value for --%s", key)
		}
		value := args[i+1]
		i++
		switch key {
		case "bucket":
			cfg.Bucket = value
		case "region":
			cfg.Region = value
		case "endpoint":
			cfg.Endpoint = value
		case "storageacc", "storage-account", "storageaccount":
			cfg.StorageAccount = value
		case "container":
			cfg.Container = value
		case "projectid", "project-id":
			cfg.ProjectID = value
		default:
			return cfg, fmt.Errorf("unknown cache option --%s", key)
		}
	}
	return cfg, nil
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

Production Kubernetes incident fixer.

Usage:
  kubectl fixora <command> [resource] [flags]

Fast incident workflow:
  scan                         List failing workloads, with logs and typed reads by default
  why <kind/name>              Explain root cause, proof, rollback hint, and next step
  fix <kind/name>              Guided RCA -> diff -> shadow verify -> patch/apply/PR flow
  ui                           Compact incident dashboard
  cluster                      Full-screen cluster dashboard
  doctor                       Validate access, RBAC, logs, events, metrics, Helm/GitOps CRDs

Specialist workflows:
  debug <tool>                 trace, graph, storage, rbac, dns, security, node-pressure, changes, readiness, rollback
  source <tool>                repo, validate, lint, preflight, policy-check

Setup:
  auth set provider key        Store AI provider credentials locally
  config                       Manage local CLI configuration
  version                      Print version

Examples:
  kubectl fixora scan -A
  kubectl fixora why deployment/api -n prod
  kubectl fixora fix deployment/api -n prod
  kubectl fixora fix deployment/api -n prod --container api --image ghcr.io/acme/api:v1.2.3
  kubectl fixora fix deployment/api -n prod --repo ./charts/api --gitops
  kubectl fixora debug trace service/api -n prod
  kubectl fixora source validate ./charts/api

Common flags:
  -n, --namespace string       Namespace (default "default")
  -A, --all-namespaces         Scan all namespaces
      --context string         Kube context
  -l, --selector string        Label selector, for example app=api,tier!=cache
  -o, --output string          text, json, yaml, markdown, sarif, junit, prometheus
      --ai                     Use AI via FIXORA_AI_API_KEY and OpenAI-compatible API
      --repo string            Local manifest, Helm chart, or Kustomize overlay path
      --gitops                 Prefer source-controlled patch output
      --quick                  Faster diagnostics; skip default shadow verification
      --safe                   Force paranoid redaction and production-safe defaults
      --apply                  Apply only after concrete diff, dry-run, and confirmation
      --proof                  Show evidence proof
      --container string       Target container for concrete patches
      --image string           Pinned replacement image
      --memory-request string  Concrete memory request
      --memory-limit string    Concrete memory limit

Run kubectl fixora help --advanced for every low-level command and flag.
`, version.Name, version.Version)
}

func printAdvancedHelp(w io.Writer) {
	fmt.Fprintf(w, `%s %s

Advanced command and flag reference.

Usage:
  kubectl fixora <command> [flags] [resource]

Primary commands:
  scan                         Alias for incidents
  rca <kind/name>              Alias for why
  repair <kind/name>           Alias for fix
  status                       Show cluster access and capability summary
  doctor                       Validate RBAC, metrics, logs, events, Helm/GitOps CRDs
  filters                      List available analyzers and active filter selection
  integrations                 Detect local optional integrations and CRDs
  custom-analyzers list|add|run Manage explicit local custom analyzer executables
  serve [addr]                 Serve a local-only HTTP API for incidents/analyze
  serve --mcp                  Serve a local MCP stdio server for AI assistants
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
  health                       Summarize namespace or cluster health
  changes <kind/name>          Correlate rollout metadata, revisions, and recent change signals
  runbook <kind/name>          Generate an operator runbook for an incident
  readiness <kind/name>        Score whether evidence is sufficient for a safe fix
  rollback <kind/name>         Preview or execute a conservative rollback command
  preflight -f path            Lint and server dry-run a manifest before apply
  policy-check -f path         Run production policy checks against manifests
  watch incidents              Poll incident state until interrupted
  ui                           Show a compact terminal incident dashboard
  cluster                      Show an interactive full-screen cluster-wide dashboard
  bundle <kind/name>           Write a redacted audit bundle
  incidents                    List current failing workloads
  analyze <kind/name>          Analyze one resource locally
  explain <kind/name> --ai     Analyze and ask an OpenAI-compatible AI for explanation
  plan <kind/name>             Build a safe local remediation plan
  fix <kind/name>              Guided production remediation flow
  diff <kind/name>             Show suggested local patch diff
  patch <kind/name> --out file Write a suggested local patch file
  report <kind/name> --out md  Export a local markdown report
  cost nodes|workloads         Estimate node/workload costs
  predict                      Show future-risk signals from local evidence
  lint -f path                 Lint manifests, Helm chart, or Kustomize overlay
  version                      Print version
  auth set provider key        Store AI provider credentials locally
  config view|set|unset|validate|export|reset|path
  cache path|stats|list|purge|clear
  cache add|get|remove         Configure K8sGPT-style remote cache metadata
  ai doctor|profiles           Validate AI setup and list prompt profiles
  memory list|clear            Inspect or clear local scenario memory

Global flags:
  -n, --namespace string       Namespace (default "default")
  -A, --all-namespaces         Scan all namespaces
      --context string         Kube context
  -l, -L, --selector string    Label selector, for example app=api,tier!=cache
  -o, --output string          text, json, yaml, markdown, sarif, junit, prometheus
      --include-logs           Include bounded logs in evidence
      --ai                     Use OpenAI-compatible AI analysis
      --auto-fix               Generate explicit local fix plan
      --apply                  Apply generated local patch
      --yes                    Confirm non-interactive verified PR/MR delivery
      --redact                 Redact sensitive values
      --unsafe-ai-no-redact    Allow AI calls with unredacted cluster data
      --filter string          Comma-separated analyzers, for example Pod,Deployment,Service
      --selector string        Scope analyzer resource lists by labels; supports =, ==, !=, key, !key
      --proof                  Show evidence proof
      --tui                    Enable interactive dashboard for the ui command
      --quick                  Use fast incident defaults
      --safe                   Use production-safe defaults
      --gitops                 Prefer GitOps source patch delivery
      --profile string         AI prompt profile or bundle profile
      --paranoid               Force redaction and secret-safe mode
      --repo string            Local repo/chart/overlay path
      --strategy string        Fix strategy such as rollback, right-size, repair-selector
      --preview                Preview patch plan only
      --force-risky            Allow risky concrete fixes after review
      --typed-client           Use client-go/controller-runtime typed client for analyzer reads
      --timeout duration       Overall command timeout
      --log-tail int           Pod log lines to collect with --include-logs
      --max-logs-bytes int     Maximum bytes per pod log stream
      --apply-dry-run          Run server-side dry-run before --apply
      --source-patch           Write patch into --repo for GitOps source review
      --shadow                 Verify patch in an isolated shadow clone before delivery
      --shadow-timeout duration Shadow clone verification timeout
      --shadow-retries int     Shadow re-clone attempts after failure
      --keep-shadow            Keep shadow Pod and NetworkPolicy after verification
      --shadow-egress string   Shadow egress policy: allow or deny
      --delivery string        Verified shadow delivery: patch, cluster, or pr
      --pr-base string         Base branch for --delivery=pr
      --pr-title string        Pull request title for --delivery=pr
      --watch-interval duration Watch polling interval
      --max-findings int       Maximum findings to display in watch mode
  -f, --filename string        Manifest, chart, or Kustomize path for lint
      --container string       Target container for concrete patches
      --image string           Pinned replacement image
      --memory-request string  Concrete memory request
      --memory-limit string    Concrete memory limit
`, version.Name, version.Version)
}

func splitCSV(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func analyzerFiltersForCommand(cmd string, rest []string, opts options) []string {
	if explicit := splitCSV(opts.filters); len(explicit) > 0 {
		return explicit
	}
	switch cmd {
	case "incidents", "watch":
		return analyzer.DefaultIncidentFilters(opts.quick)
	case "why", "fix", "plan", "diff", "patch", "runbook", "readiness", "rollback", "analyze", "explain", "graph", "changes", "report", "bundle":
		if len(rest) > 0 {
			return analyzer.SmartFiltersFor(rest[0], "")
		}
	case "health":
		return analyzer.DefaultIncidentFilters(false)
	}
	return nil
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

func hasArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
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
		Strategy:      opts.strategy,
		ForceRisky:    opts.forceRisky,
	}
}

func defaultShadowBranch(finding analyzer.Finding) string {
	kind := strings.ToLower(strings.ReplaceAll(finding.ResourceKind, "/", "-"))
	name := strings.ToLower(strings.ReplaceAll(finding.ResourceName, "/", "-"))
	return "fixora/verified-" + kind + "-" + name
}

func prBody(result shadow.Result, sourcePatch repo.SourcePatch) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Fixora verified this remediation in a shadow clone before delivery.\n\n")
	fmt.Fprintf(&b, "- Resource: `%s`\n", result.Resource)
	fmt.Fprintf(&b, "- Namespace: `%s`\n", result.Namespace)
	fmt.Fprintf(&b, "- Parity: `%d%%`\n", result.Parity)
	fmt.Fprintf(&b, "- Source patch: `%s`\n", sourcePatch.Path)
	for _, attempt := range result.Attempts {
		fmt.Fprintf(&b, "- Attempt %d: phase `%s`, ready `%t`, restarts `%d`", attempt.Number, attempt.Phase, attempt.Ready, attempt.Restarts)
		if attempt.ExitReason != "" {
			fmt.Fprintf(&b, ", reason `%s`", attempt.ExitReason)
		}
		b.WriteString("\n")
	}
	return b.String()
}
