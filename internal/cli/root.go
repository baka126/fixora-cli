package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	flag "github.com/spf13/pflag"

	"github.com/fixora/kubectl-fixora/internal/ai"
	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/bundle"
	"github.com/fixora/kubectl-fixora/internal/cache"
	"github.com/fixora/kubectl-fixora/internal/config"
	"github.com/fixora/kubectl-fixora/internal/custom"
	"github.com/fixora/kubectl-fixora/internal/debug"
	"github.com/fixora/kubectl-fixora/internal/fix"
	"github.com/fixora/kubectl-fixora/internal/graph"
	"github.com/fixora/kubectl-fixora/internal/image"
	"github.com/fixora/kubectl-fixora/internal/integration"
	"github.com/fixora/kubectl-fixora/internal/kube"
	"github.com/fixora/kubectl-fixora/internal/mcp"
	"github.com/fixora/kubectl-fixora/internal/memory"
	"github.com/fixora/kubectl-fixora/internal/ops"
	"github.com/fixora/kubectl-fixora/internal/output"
	"github.com/fixora/kubectl-fixora/internal/redact"
	"github.com/fixora/kubectl-fixora/internal/repo"
	"github.com/fixora/kubectl-fixora/internal/report"
	"github.com/fixora/kubectl-fixora/internal/server"
	"github.com/fixora/kubectl-fixora/internal/shadow"
	"github.com/fixora/kubectl-fixora/internal/termui"
	"github.com/fixora/kubectl-fixora/internal/version"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/yaml"
)

type options struct {
	namespace       string
	allNS           bool
	context         string
	output          string
	includeLogs     bool
	useAI           bool
	noAI            bool
	autoFix         bool
	apply           bool
	yes             bool
	outFile         string
	editPatch       bool
	verbose         bool
	redact          bool
	unsafeAI        bool
	filters         string
	labelSelector   string
	wide            bool
	noColor         bool
	proof           bool
	paranoid        bool
	preview         bool
	forceRisky      bool
	typedClient     bool
	checkCertExpiry bool
	tui             bool
	repoPath        string
	strategy        string
	branch          string
	commit          bool
	mcp             bool
	profile         string
	aiBudget        int
	container       string
	image           string
	memRequest      string
	memLimit        string
	cpuRequest      string
	envName         string
	configMap       string
	configKey       string
	timeout         time.Duration
	logTail         int
	maxLogBytes     int
	applyDryRun     bool
	sourcePatch     bool
	shadowVerify    bool
	shadowTimeout   time.Duration
	rolloutTimeout  time.Duration
	shadowRetries   int
	keepShadow      bool
	shadowEgress    string
	delivery        string
	prBase          string
	prTitle         string
	watchInterval   time.Duration
	lintFiles       listFlag
	maxFindings     int
	quick           bool
	safe            bool
	gitops          bool
	visited         map[string]bool
	promptInput     *bufio.Reader
}

type listFlag []string

func (l *listFlag) String() string {
	return fmt.Sprint([]string(*l))
}

func (l *listFlag) Set(value string) error {
	*l = append(*l, value)
	return nil
}

func (l *listFlag) Type() string {
	return "stringSlice"
}

func Execute(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		args = []string{"dashboard"}
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
	{
		cfg, _ := config.Load()
		shadow.SetPatchPolicy(policyFromConfig(cfg))
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
	if args[0] == "doctor" {
		return runAIDoctor(args[1:], stdout, stderr)
	}
	if args[0] == "profiles" {
		return runProfiles(args[1:], stdout, stderr)
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
	cmd, rest, err = normalizeCommand(cmd, rest)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n\n", err)
		printHelp(stderr)
		return 2
	}
	applyWorkflowDefaults(cmd, &opts)
	if cmd == "fix" {
		reconcileDeliveryFlags(&opts, stderr)
	}
	opts.promptInput = bufio.NewReader(os.Stdin)
	baseCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ctx, cancel := commandContext(baseCtx, cmd, opts)
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
		Namespace:       opts.namespace,
		AllNS:           opts.allNS,
		IncludeLogs:     opts.includeLogs,
		Redact:          opts.redact || opts.paranoid,
		Filters:         filters,
		LabelSelector:   opts.labelSelector,
		CheckCertExpiry: opts.checkCertExpiry,
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
			if err := (mcp.Server{Kubectl: k, AnalyzerOpt: analyzer.Options{Namespace: opts.namespace, AllNS: opts.allNS, IncludeLogs: opts.includeLogs, Redact: opts.redact, Filters: splitCSV(opts.filters), LabelSelector: opts.labelSelector, CheckCertExpiry: opts.checkCertExpiry}}).ServeStdio(ctx, os.Stdin, stdout); err != nil {
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
			AnalyzerOpt: analyzer.Options{Namespace: opts.namespace, AllNS: opts.allNS, IncludeLogs: opts.includeLogs, Redact: opts.redact, Filters: splitCSV(opts.filters), LabelSelector: opts.labelSelector, CheckCertExpiry: opts.checkCertExpiry},
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
	case "why":
		if len(rest) == 0 {
			selectedResource, err := promptIncidentSelection(ctx, a, stdout, stderr, opts.wide, opts.noColor)
			if err != nil {
				fmt.Fprintf(stderr, "error: %v\n", err)
				return 2
			}
			rest = []string{selectedResource}
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
			finding = enrichFindingForAI(ctx, reader, k, opts, rest[0], finding)
			augmentWithAI(ctx, &finding, opts, stderr)
		}
		plan := fix.BuildPlan(finding)
		plan = fix.Concretize(plan, concreteOptions(opts))
		if opts.output != "text" {
			return output.Write(stdout, opts.output, plan)
		}
		fmt.Fprintln(stdout, "Fixora incident explanation")
		fmt.Fprintln(stdout, strings.Repeat("=", 28))
		termui.Why(stdout, finding, plan, true, termui.Options{Wide: true, NoColor: opts.noColor})
		termui.Plan(stdout, plan, termui.Options{Wide: true, NoColor: opts.noColor})
		return 0
	case "fix":
		analysisCtx, analysisCancel := fixAnalysisContext(ctx, opts.timeout)
		defer analysisCancel()
		if len(rest) == 0 {
			selectedResource, err := promptIncidentSelection(analysisCtx, a, stdout, stderr, opts.wide, opts.noColor)
			if err != nil {
				fmt.Fprintf(stderr, "error: %v\n", err)
				return 2
			}
			rest = []string{selectedResource}
		}
		if opts.output == "text" {
			fmt.Fprintf(stderr, "Analyzing %s...\n", rest[0])
		}
		finding, err := a.AnalyzeResource(analysisCtx, rest[0])
		if err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
		finding = preferSmartFinding(analysisCtx, a, finding)
		if opts.useAI {
			finding = enrichFindingForAI(analysisCtx, reader, k, opts, rest[0], finding)
			augmentWithAI(analysisCtx, &finding, opts, stderr)
		}
		plan := fix.BuildPlan(finding)
		plan = fix.Concretize(plan, concreteOptions(opts))
		if opts.useAI {
			plan = applyAIPatchIfSafe(analysisCtx, plan, finding, stderr, opts.verbose)
		}
		if !plan.ApplyEligible {
			plan = applyTrustedImageCandidate(analysisCtx, plan, finding, stderr, opts.verbose)
		}
		if containsString(plan.Guardrails, "trusted-public-image-candidate") {
			opts.shadowVerify = true
		}
		if opts.repoPath != "" {
			mode, repoErr := repo.Plan(analysisCtx, opts.repoPath, finding, plan)
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
		return runGuidedFix(ctx, stdout, stderr, opts, k, finding, plan, rest[0])
	case "coordinate":
		if len(rest) < 2 {
			fmt.Fprintln(stderr, "error: coordinate requires two or more resources (kind/name ...) to apply together")
			return 2
		}
		analysisCtx, analysisCancel := fixAnalysisContext(ctx, opts.timeout)
		defer analysisCancel()
		steps, err := buildCoordinateSteps(analysisCtx, a, opts, rest)
		if err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
		in := inputFor(opts)
		confirmApply := func() bool {
			if opts.yes {
				return true
			}
			return termui.ConfirmRollback(fmt.Sprintf("apply %d coordinated changes", len(steps)), in, stderr)
		}
		confirmRollback := func() bool {
			if opts.yes {
				return false // never auto-rollback non-interactively
			}
			return termui.ConfirmRollback("roll back the already-applied changes", in, stderr)
		}
		deps := coordinateDeps{k: k, timeout: opts.rolloutTimeout}
		return runCoordinateSteps(analysisCtx, stdout, stderr, steps, deps, confirmApply, confirmRollback)
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
		timeout = 3 * time.Minute
	}
	defaultOutput := cfg.DefaultOutput
	if defaultOutput == "" {
		defaultOutput = "text"
	}
	opts := options{
		output:          defaultOutput,
		namespace:       "default",
		redact:          cfg.Redact,
		paranoid:        cfg.Paranoid,
		checkCertExpiry: cfg.CheckCertExpiry,
		timeout:         timeout,
		logTail:         cfg.LogTail,
		maxLogBytes:     cfg.MaxLogBytes,
		applyDryRun:     cfg.ApplyDryRun,
	}
	if opts.logTail <= 0 {
		opts.logTail = 120
	}
	if opts.maxLogBytes <= 0 {
		opts.maxLogBytes = 24000
	}
	fs := flag.NewFlagSet("kubectl-fixora", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVarP(&opts.namespace, "namespace", "n", "default", "namespace")
	fs.BoolVarP(&opts.allNS, "all-namespaces", "A", false, "scan all namespaces")
	fs.StringVar(&opts.context, "context", "", "kube context")
	fs.StringVarP(&opts.output, "output", "o", opts.output, "output format: text, json, yaml, markdown")
	fs.BoolVar(&opts.includeLogs, "include-logs", false, "include bounded pod logs")
	fs.BoolVar(&opts.useAI, "ai", false, "use OpenAI-compatible AI analysis")
	fs.BoolVar(&opts.noAI, "no-ai", false, "disable AI remediation for this command")
	fs.BoolVar(&opts.autoFix, "auto-fix", false, "generate an explicit local fix plan")
	fs.BoolVar(&opts.apply, "apply", false, "apply generated local patch")
	fs.BoolVar(&opts.yes, "yes", false, "confirm non-interactive verified PR/MR delivery")
	fs.StringVar(&opts.outFile, "out", "", "output file")
	fs.BoolVar(&opts.editPatch, "edit-patch", false, "open the generated patch in $VISUAL/$EDITOR before shadow/apply/source delivery")
	fs.BoolVar(&opts.verbose, "verbose", false, "verbose output")
	fs.BoolVar(&opts.redact, "redact", opts.redact, "redact sensitive values")
	fs.BoolVar(&opts.unsafeAI, "unsafe-ai-no-redact", false, "allow AI calls with unredacted cluster data")
	fs.StringVar(&opts.filters, "filter", "", "comma-separated analyzer filters")
	fs.StringVar(&opts.filters, "filters", "", "comma-separated analyzer filters")
	fs.StringVarP(&opts.labelSelector, "selector", "l", "", "label selector for analyzer resource lists")
	fs.BoolVar(&opts.wide, "wide", false, "wide terminal output")
	fs.BoolVar(&opts.noColor, "no-color", false, "disable terminal color")
	fs.BoolVar(&opts.proof, "proof", false, "show evidence proof")
	fs.BoolVar(&opts.paranoid, "paranoid", opts.paranoid, "avoid secret-sensitive evidence and force redaction")
	fs.BoolVar(&opts.preview, "preview", false, "preview patch plan without writing")
	fs.BoolVar(&opts.forceRisky, "force-risky", false, "allow risky concrete fixes to pass apply eligibility after review")
	fs.BoolVar(&opts.typedClient, "typed-client", false, "use client-go/controller-runtime typed client for analyzer reads")
	fs.BoolVar(&opts.checkCertExpiry, "cert-expiry", opts.checkCertExpiry, "check Ingress TLS certificate expiry (reads only the public tls.crt, never the private key)")
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
	fs.DurationVar(&opts.shadowTimeout, "shadow-timeout", 10*time.Minute, "shadow clone verification timeout")
	fs.DurationVar(&opts.rolloutTimeout, "rollout-timeout", 2*time.Minute, "post-apply rollout verification timeout")
	fs.IntVar(&opts.shadowRetries, "shadow-retries", 0, "number of shadow re-clone attempts after failure")
	fs.BoolVar(&opts.keepShadow, "keep-shadow", false, "keep shadow Pod and NetworkPolicy after verification")
	fs.StringVar(&opts.shadowEgress, "shadow-egress", "allow", "shadow egress policy: allow or deny")
	fs.StringVar(&opts.delivery, "delivery", "patch", "verified shadow delivery: patch, cluster, or pr")
	fs.StringVar(&opts.prBase, "pr-base", "", "base branch for --delivery=pr")
	fs.StringVar(&opts.prTitle, "pr-title", "", "pull request title for --delivery=pr")
	fs.DurationVar(&opts.watchInterval, "watch-interval", 5*time.Second, "watch polling interval")
	fs.IntVar(&opts.maxFindings, "max-findings", 8, "maximum findings to display in watch mode")
	fs.VarP(&opts.lintFiles, "filename", "f", "manifest, chart, or kustomize path to lint")
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
	if opts.noAI {
		opts.useAI = false
	}
	return opts, fs.Args(), nil
}

func normalizeCommand(cmd string, rest []string) (string, []string, error) {
	switch cmd {
	case "scan":
		return "incidents", rest, nil
	case "repair":
		return "fix", rest, nil
	case "dashboard":
		return "cluster", rest, nil
	case "fix-set":
		return "coordinate", rest, nil
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

// reconcileDeliveryFlags maps the deprecated --apply/--source-patch/--gitops
// flags onto the canonical --delivery selector. An explicit --delivery always
// wins; a legacy flag only applies when --delivery is still at its default.
func reconcileDeliveryFlags(opts *options, warn io.Writer) {
	// An explicit --delivery=patch is treated as non-explicit (delivery != "patch"
	// gate) so a legacy flag can still map it; only a non-default explicit value wins.
	explicit := opts.visited["delivery"] && strings.TrimSpace(opts.delivery) != "" && opts.delivery != "patch"
	set := func(mode, flag string) {
		fmt.Fprintf(warn, "warning: --%s is deprecated; use --delivery=%s\n", flag, mode)
		if !explicit {
			opts.delivery = mode
		}
	}
	if opts.visited["apply"] && opts.apply {
		set("cluster", "apply")
	}
	if opts.visited["source-patch"] && opts.sourcePatch {
		set("pr", "source-patch")
	}
	if opts.visited["gitops"] && opts.gitops {
		set("pr", "gitops")
		opts.sourcePatch = true // legacy non-shadow path (gitops disables shadow) still writes the source patch
	}
	// Mirror the canonical delivery onto the legacy booleans so the non-shadow
	// path (e.g. --quick --delivery=cluster|pr) performs the requested delivery
	// instead of silently only writing a patch file. The shadow path consumes
	// opts.delivery directly and returns before these booleans are read, so this
	// cannot double-deliver. Idempotent with the legacy-flag mappings above.
	switch strings.ToLower(strings.TrimSpace(opts.delivery)) {
	case "cluster":
		opts.apply = true
	case "pr":
		opts.sourcePatch = true
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
		if !opts.visited["ai"] && !opts.visited["no-ai"] {
			opts.useAI = true
		}
		if !opts.visited["shadow"] && !opts.quick {
			opts.shadowVerify = true
		}
		if !opts.visited["shadow-retries"] && !opts.quick && opts.useAI {
			opts.shadowRetries = 1
		}
	}
	if opts.gitops {
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

func visitedFlags(fs *flag.FlagSet) map[string]bool {
	visited := map[string]bool{}
	fs.Visit(func(f *flag.Flag) {
		visited[f.Name] = true
	})
	return visited
}

func commandContext(parent context.Context, cmd string, opts options) (context.Context, context.CancelFunc) {
	if cmd == "serve" || cmd == "watch" || cmd == "cluster" || (cmd == "ui" && opts.tui) || cmd == "fix" {
		return parent, func() {}
	}
	if opts.timeout > 0 {
		return context.WithTimeout(parent, opts.timeout)
	}
	return parent, func() {}
}

func fixAnalysisContext(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout > 0 {
		return context.WithTimeout(parent, timeout)
	}
	return parent, func() {}
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

func guardDelivery(stderr io.Writer, opts options, finding analyzer.Finding, mode shadow.DeliveryMode) int {
	if mode == shadow.DeliveryCluster && sourceManaged(finding) {
		failNext(stderr, "direct cluster delivery is blocked for Helm/GitOps-managed resources", "use --delivery=pr to deliver a validated source change")
		return 2
	}
	if mode == shadow.DeliveryPR {
		if opts.repoPath == "" {
			failNext(stderr, "--delivery=pr requires --repo", "re-run with --repo <path-to-your-manifests-repo>")
			return 2
		}
		if !opts.yes {
			failNext(stderr, "--delivery=pr requires --yes (it commits, pushes, and opens a review request)", "re-run with --yes to confirm PR/MR delivery")
			return 2
		}
		repoMode, err := repo.Detect(opts.repoPath)
		if err != nil {
			fmt.Fprintf(stderr, "error: detect source repository: %v\n", err)
			return 2
		}
		if repoMode.Type == "helm" {
			failNext(stderr, "automatic Helm PR delivery is blocked; Fixora cannot safely map a rendered patch to chart-native values", "use the review-only source patch and validate with helm template")
			return 2
		}
	}
	return 0
}

func verifyInShadow(ctx context.Context, stdout, stderr io.Writer, opts options, k kube.Kubectl, finding analyzer.Finding, plan fix.Plan, confirm bool) (shadow.Result, bool, int) {
	if !plan.ApplyEligible {
		failNext(stderr, "shadow verification requires an apply-eligible concrete patch", "supply concrete values (e.g. --image/--memory-request) or use --delivery=pr for a review-only source patch")
		return shadow.Result{}, false, 1
	}
	if confirm {
		diff := termui.DisplayDiff(finalPatchDiff(ctx, opts, k, plan), opts.noColor)
		if !termui.ConfirmShadowDeploy(diff, inputFor(opts), stdout) {
			fmt.Fprintln(stdout, "shadow verification cancelled")
			return shadow.Result{}, false, 0
		}
	}
	fmt.Fprintln(stdout, "Starting shadow verification...")
	typed, err := kube.NewRequiredTypedClient(opts.context, "shadow verification")
	if err != nil {
		fmt.Fprintf(stderr, "error: typed Kubernetes client: %v\n", err)
		return shadow.Result{}, false, 1
	}
	typed.LogTail = opts.logTail
	typed.LogLimitBytes = opts.maxLogBytes
	retryProvider, retries := shadowRetryProvider(opts, stderr)
	mode := shadow.DeliveryMode(strings.ToLower(strings.TrimSpace(opts.delivery)))
	if mode == "" {
		mode = shadow.DeliveryPatch
	}
	req := shadow.Request{
		Namespace:   finding.Namespace,
		Resource:    plan.Resource,
		Patch:       plan.PatchYAML(),
		Finding:     finding,
		Plan:        plan,
		Timeout:     opts.shadowTimeout,
		Retries:     retries,
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
		AI:          retryProvider,
	}
	fmt.Fprintln(stdout, "Creating isolated shadow Pod and NetworkPolicy...")
	result, err := shadow.Run(ctx, typed, req)
	if err != nil {
		fmt.Fprintf(stderr, "error: shadow verification: %v\n", err)
		return shadow.Result{}, false, 1
	}
	if !result.Verified {
		if opts.output == "text" {
			writeShadowFailure(stdout, result, opts.outFile, finding, plan)
		} else {
			_ = output.Write(stdout, opts.output, result)
		}
		return result, false, 1
	}
	return result, true, 0
}

type rolloutGate interface {
	ops.RolloutChecker
	ops.CompletionChecker
	Run(ctx context.Context, args ...string) ([]byte, error)
}

// gateRollout verifies the live rollout after an apply and, on failure, offers
// the resource-aware rollback. It never auto-rolls-back without explicit
// interactive confirmation.
func gateRollout(ctx context.Context, stdout, stderr io.Writer, in io.Reader, assumeYes bool, k rolloutGate, finding analyzer.Finding, plan fix.Plan, timeout time.Duration) int {
	var outcome ops.RolloutOutcome
	switch strings.ToLower(strings.TrimSpace(finding.ResourceKind)) {
	case "job", "cronjob":
		outcome = ops.VerifyCompletion(ctx, k, finding, plan, timeout)
	default:
		outcome = ops.VerifyRollout(ctx, k, finding, plan, timeout)
	}
	switch outcome.Class {
	case ops.RolloutHealthy, ops.CompletionSucceeded, ops.CronJobHealthy:
		fmt.Fprintf(stderr, "rollout healthy: %s\n", outcome.Summary)
		return 0
	case ops.RolloutSkipped, ops.RolloutUnknown,
		ops.CompletionPending, ops.CompletionUnknown,
		ops.CronJobSuspended, ops.CronJobUnknown:
		fmt.Fprintf(stderr, "warning: %s\n", outcome.Summary)
		return 0
	}
	fmt.Fprintf(stderr, "rollout failed: %s\n", outcome.Summary)
	for _, ev := range outcome.Events {
		fmt.Fprintf(stderr, "  event: %s\n", ev)
	}
	for _, hint := range outcome.CauseHints {
		fmt.Fprintf(stderr, "  hint: %s\n", hint)
	}
	for _, w := range outcome.Rollback.Warnings {
		fmt.Fprintf(stderr, "  note: %s\n", w)
	}
	cmd := strings.TrimSpace(outcome.Rollback.Command)
	if cmd == "" {
		fmt.Fprintln(stderr, "no deterministic rollback command available; review the workload manually")
		return 1
	}
	if assumeYes {
		fmt.Fprintf(stderr, "rollback not run (non-interactive). To roll back:\n  %s\n", cmd)
		return 1
	}
	if termui.ConfirmRollback(cmd, in, stderr) {
		if outcome.Rollback.Binary == "kubectl" {
			if _, err := k.Run(ctx, outcome.Rollback.Args...); err != nil {
				fmt.Fprintf(stderr, "error: rollback failed: %v\n", err)
				return 1
			}
			fmt.Fprintln(stderr, "rollback applied")
		} else {
			fmt.Fprintf(stderr, "run this rollback manually:\n  %s\n", cmd)
		}
	}
	return 1
}

func deliverVerifiedFix(ctx context.Context, stdout, stderr io.Writer, opts options, k kube.Kubectl, finding analyzer.Finding, plan fix.Plan, result shadow.Result, mode shadow.DeliveryMode) int {
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
		if !termui.ConfirmApply(ctx, k, plan.PatchTemplate, inputFor(opts), stdout) {
			fmt.Fprintln(stdout, "verified apply cancelled")
			return 0
		}
		if err := k.Apply(ctx, opts.outFile); err != nil {
			fmt.Fprintf(stderr, "error: apply verified patch: %v\n", err)
			return 1
		}
		result.Delivery = "cluster"
		if opts.output != "text" {
			if code := output.Write(stdout, opts.output, result); code != 0 {
				return code
			}
		} else {
			fmt.Fprintf(stdout, "Fix Verified - Parity %d%%\napplied verified patch\n", result.Parity)
		}
		return gateRollout(ctx, stdout, stderr, inputFor(opts), false, k, finding, plan, opts.rolloutTimeout)
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

func runShadowWorkflow(ctx context.Context, stdout, stderr io.Writer, opts options, k kube.Kubectl, finding analyzer.Finding, plan fix.Plan) int {
	if !plan.ApplyEligible {
		failNext(stderr, "shadow verification requires an apply-eligible concrete patch", "supply concrete values (e.g. --image/--memory-request) or use --delivery=pr for a review-only source patch")
		return 1
	}
	mode := shadow.DeliveryMode(strings.ToLower(strings.TrimSpace(opts.delivery)))
	if mode == "" {
		mode = shadow.DeliveryPatch
	}
	if mode != shadow.DeliveryPatch && mode != shadow.DeliveryCluster && mode != shadow.DeliveryPR {
		failNext(stderr, fmt.Sprintf("unsupported --delivery %q", opts.delivery), "use --delivery patch, cluster, or pr")
		return 2
	}
	if code := guardDelivery(stderr, opts, finding, mode); code != 0 {
		return code
	}
	result, verified, code := verifyInShadow(ctx, stdout, stderr, opts, k, finding, plan, true)
	if !verified {
		return code
	}
	return deliverVerifiedFix(ctx, stdout, stderr, opts, k, finding, plan, result, mode)
}

func shadowRetryProvider(opts options, stderr io.Writer) (ai.Provider, int) {
	if opts.shadowRetries <= 0 || !opts.useAI {
		return nil, 0
	}
	client, err := ai.NewFromEnv()
	if err != nil {
		if opts.verbose {
			fmt.Fprintf(stderr, "shadow AI retry disabled: %v\n", err)
		}
		return nil, 0
	}
	return client, opts.shadowRetries
}

func writeShadowFailure(w io.Writer, result shadow.Result, patchFile string, finding analyzer.Finding, plan fix.Plan) {
	fmt.Fprintln(w, "\nShadow verification failed")
	fmt.Fprintln(w, "No production mutation, PR/MR, or source delivery was performed.")
	for _, attempt := range result.Attempts {
		phase := attempt.Phase
		if phase == "" {
			phase = "unknown"
		}
		fmt.Fprintf(w, "\nAttempt %d: phase=%s ready=%t restarts=%d", attempt.Number, phase, attempt.Ready, attempt.Restarts)
		if attempt.ExitReason != "" {
			fmt.Fprintf(w, " reason=%s", attempt.ExitReason)
		}
		fmt.Fprintln(w)
		if len(attempt.Logs) > 0 {
			lastLog := redact.KubernetesText(attempt.Logs[len(attempt.Logs)-1])
			fmt.Fprintf(w, "  Last log: %s\n", trimEvidence(lastLog, 220))
		}
	}
	writeShadowFailureGuidance(w, result, finding, plan)
	if patchFile != "" {
		fmt.Fprintf(w, "\nReviewed patch retained at %s\n", patchFile)
	}
	fmt.Fprintln(w, "The candidate was rejected by shadow verification. Review the evidence and choose another patch before retrying.")
}

func writeShadowFailureGuidance(w io.Writer, result shadow.Result, finding analyzer.Finding, plan fix.Plan) {
	diagnosis := shadow.DiagnoseFailure(result, finding, plan)
	if diagnosis.Summary != "" {
		fmt.Fprintln(w, "\nFollow-up diagnosis")
		fmt.Fprintf(w, "  %s\n", diagnosis.Summary)
		if diagnosis.OriginalSymptomResolved {
			fmt.Fprintln(w, "  Original symptom: appears resolved in the shadow evidence.")
		}
		for _, detail := range diagnosis.Details {
			fmt.Fprintf(w, "  %s\n", detail)
		}
		if diagnosis.DeliveryBlocked {
			fmt.Fprintln(w, "  Delivery remains blocked until a candidate passes shadow verification.")
		}
		return
	}
	if !strings.EqualFold(plan.Strategy, "fix-architecture") || !strings.Contains(finding.Status, "ExecFormatError") {
		return
	}
	if !shadowAttemptReason(result, "OOMKilled") {
		return
	}
	fmt.Fprintln(w, "\nFollow-up diagnosis")
	fmt.Fprintln(w, "  The replacement image got past the original architecture mismatch, but the shadow clone was OOMKilled.")
	fmt.Fprintln(w, "  Treat this as a failed candidate, not a verified architecture fix.")
	fmt.Fprintln(w, "  Prefer a same-repository multi-arch tag/digest or rebuild the original image for the node platform.")
	fmt.Fprintln(w, "  If the image is correct and the workload is expected to allocate memory, create a combined source fix with reviewed resource requests/limits and re-run shadow verification.")
}

func shadowAttemptReason(result shadow.Result, reason string) bool {
	for _, attempt := range result.Attempts {
		if strings.EqualFold(attempt.ExitReason, reason) {
			return true
		}
		for _, event := range attempt.Events {
			if strings.Contains(strings.ToLower(event), strings.ToLower(reason)) {
				return true
			}
		}
	}
	return false
}

func runGuidedFix(ctx context.Context, stdout, stderr io.Writer, opts options, k kube.Kubectl, finding analyzer.Finding, plan fix.Plan, resourceArg string) int {
	if opts.output != "text" {
		return output.Write(stdout, opts.output, plan)
	}

	if interactiveFix(opts) {
		return runFixWalkthrough(ctx, stdout, stderr, opts, k, finding, plan, resourceArg)
	}

	fmt.Fprintln(stdout, "Fixora incident fix")
	fmt.Fprintln(stdout, strings.Repeat("=", 20))
	termui.Why(stdout, finding, plan, true, termui.Options{Wide: true, NoColor: opts.noColor})
	termui.Plan(stdout, plan, termui.Options{Wide: true, NoColor: opts.noColor})
	if strings.TrimSpace(plan.PatchYAML()) != "" {
		fmt.Fprintln(stdout, "\nSuggested diff")
		fmt.Fprint(stdout, termui.DisplayDiff(shadow.PatchDiff(plan.Resource, plan.PatchYAML()), opts.noColor))
	}

	if opts.preview {
		return 0
	}

	if !plan.ApplyEligible {
		if hasConcreteReviewPatch(plan) {
			if opts.outFile == "" {
				opts.outFile = "fixora-patch.yaml"
			}
			updatedPlan, err := writeReviewPatch(ctx, stdout, stderr, opts, plan)
			if err != nil {
				fmt.Fprintf(stderr, "error: %v\n", err)
				return 1
			}
			plan = updatedPlan
			if opts.sourcePatch {
				if opts.repoPath == "" {
					failNext(stderr, "--delivery=pr (--gitops/--source-patch) requires --repo", "re-run with --repo <path-to-your-manifests-repo>")
					return 2
				}
				sourcePatch, err := repo.WriteSourcePatch(opts.repoPath, opts.outFile, finding, plan)
				if err != nil {
					fmt.Fprintf(stderr, "error: source patch: %v\n", err)
					return 1
				}
				return output.Write(stdout, opts.output, sourcePatch)
			}
		}
		fmt.Fprintln(stdout, "\nNo production mutation was attempted.")
		fmt.Fprintln(stdout, "Fixora needs a concrete, safe patch before shadow verification or apply.")
		if hasConcreteReviewPatch(plan) {
			fmt.Fprintln(stdout, "A review-only patch was written for source/GitOps review; shadow verification and direct apply remain blocked.")
		}
		if hint := nextConcreteFixHint(resourceArg, plan); hint != "" {
			fmt.Fprintf(stdout, "\nNext command:\n  %s\n", hint)
		}
		return 0
	}

	if opts.outFile == "" {
		opts.outFile = "fixora-patch.yaml"
	}
	updatedPlan, err := writeReviewPatch(ctx, stdout, stderr, opts, plan)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	plan = updatedPlan

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
		if opts.branch != "" || opts.commit {
			branch := firstArg([]string{opts.branch, defaultShadowBranch(finding)})
			if err := repo.PrepareBranchFiles(ctx, opts.repoPath, branch, opts.commit, "fixora: remediation patch for "+finding.ResourceKind+"/"+finding.ResourceName, []string{sourcePatch.Path}); err != nil {
				fmt.Fprintf(stderr, "error: repo workflow: %v\n", err)
				return 1
			}
		}
		return output.Write(stdout, opts.output, sourcePatch)
	}
	if opts.apply {
		if sourceManaged(finding) {
			failNext(stderr, "direct apply is blocked for Helm/GitOps-managed resources", "use --delivery=pr with --repo to deliver a validated source change")
			return 2
		}
		if opts.applyDryRun {
			if err := k.DryRunApply(ctx, opts.outFile); err != nil {
				fmt.Fprintf(stderr, "error: server dry-run rejected patch: %v\n", err)
				return 1
			}
		}
		if termui.ConfirmApply(ctx, k, plan.PatchTemplate, inputFor(opts), stdout) {
			if err := k.Apply(ctx, opts.outFile); err != nil {
				fmt.Fprintf(stderr, "error: apply patch: %v\n", err)
				return 1
			}
			fmt.Fprintln(stdout, "applied patch")
			_ = memory.Add(finding, plan, "guided-fix")
			return gateRollout(ctx, stdout, stderr, inputFor(opts), false, k, finding, plan, opts.rolloutTimeout)
		}
	}
	_ = memory.Add(finding, plan, "guided-fix")
	return 0
}

func sourceManaged(finding analyzer.Finding) bool {
	if strings.TrimSpace(finding.GitOps.TargetAdvice) != "" ||
		strings.TrimSpace(finding.GitOps.HelmRelease) != "" ||
		strings.TrimSpace(finding.GitOps.HelmChart) != "" {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(finding.GitOps.ManagedBy), "helm") {
		return true
	}
	return strings.TrimSpace(finding.GitOps.ArgoHint) != "" || strings.TrimSpace(finding.GitOps.FluxHint) != ""
}

func hasConcreteReviewPatch(plan fix.Plan) bool {
	patch := strings.TrimSpace(plan.PatchYAML())
	return patch != "" && !strings.Contains(patch, "TODO_") && !strings.HasPrefix(patch, "#")
}

func writeReviewPatch(ctx context.Context, stdout, stderr io.Writer, opts options, plan fix.Plan) (fix.Plan, error) {
	if err := os.WriteFile(opts.outFile, []byte(plan.PatchYAML()), 0o600); err != nil {
		return plan, fmt.Errorf("write patch: %w", err)
	}
	fmt.Fprintf(stdout, "\nwrote %s\n", opts.outFile)
	edit := opts.editPatch
	if !edit && !opts.yes && (opts.shadowVerify || opts.apply || opts.sourcePatch) {
		edit = termui.ConfirmEditPatch(opts.outFile, inputFor(opts), stdout)
	}
	if edit {
		fmt.Fprintf(stdout, "opening %s for review; save and close the editor to continue\n", opts.outFile)
		if err := openPatchEditor(ctx, opts.outFile, os.Stdin, stdout, stderr); err != nil {
			return plan, fmt.Errorf("edit patch: %w", err)
		}
		content, err := os.ReadFile(opts.outFile)
		if err != nil {
			return plan, fmt.Errorf("read edited patch: %w", err)
		}
		if strings.TrimSpace(string(content)) == "" {
			return plan, fmt.Errorf("edited patch is empty")
		}
		if err := shadow.ValidateReviewedPatch(plan.PatchYAML(), string(content), plan.Strategy); err != nil {
			return plan, fmt.Errorf("edited patch was rejected before shadow verification or delivery: %w", err)
		}
		plan.PatchTemplate = string(content)
		fmt.Fprintln(stdout, "edited patch saved")
	}
	return plan, nil
}

func finalPatchDiff(ctx context.Context, opts options, k kube.Kubectl, plan fix.Plan) string {
	if opts.outFile != "" {
		if diff, err := k.Diff(ctx, opts.outFile); err == nil && strings.TrimSpace(diff) != "" {
			return diff + "\n"
		}
	}
	return shadow.PatchDiff(plan.Resource, plan.PatchYAML())
}

func openPatchEditor(ctx context.Context, path string, stdin io.Reader, stdout, stderr io.Writer) error {
	editor, err := editorCommand(os.Getenv("VISUAL"), os.Getenv("EDITOR"))
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, editor[0], append(editor[1:], path)...)
	cmd.Stdin = firstReader(stdin, os.Stdin)
	cmd.Stdout = firstWriter(stdout, os.Stdout)
	cmd.Stderr = firstWriter(stderr, os.Stderr)
	return cmd.Run()
}

func editorCommand(visual, editor string) ([]string, error) {
	raw := strings.TrimSpace(visual)
	if raw == "" {
		raw = strings.TrimSpace(editor)
	}
	if raw == "" {
		raw = "vi"
	}
	parts := strings.Fields(raw)
	if len(parts) == 0 || strings.TrimSpace(parts[0]) == "" {
		return nil, fmt.Errorf("no editor command configured")
	}
	if strings.ContainsRune(parts[0], '\x00') {
		return nil, fmt.Errorf("invalid editor command")
	}
	if _, err := exec.LookPath(parts[0]); err != nil {
		return nil, fmt.Errorf("editor %q not found; set VISUAL or EDITOR", parts[0])
	}
	return parts, nil
}

func firstReader(primary io.Reader, fallback io.Reader) io.Reader {
	if primary != nil {
		return primary
	}
	return fallback
}

func inputFor(opts options) io.Reader {
	if opts.promptInput != nil {
		return opts.promptInput
	}
	return os.Stdin
}

func firstWriter(primary io.Writer, fallback io.Writer) io.Writer {
	if primary != nil {
		return primary
	}
	return fallback
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

// handleUnstructuredAI discards an AI result the model failed to structure and
// emits a visible (non-verbose) warning so the user knows the deterministic
// plan is in use.
func handleUnstructuredAI(finding *analyzer.Finding, stderr io.Writer) {
	if finding.AI != nil && finding.AI.Unstructured {
		fmt.Fprintln(stderr, "warning: AI response could not be parsed; using the deterministic plan.")
		finding.AI = nil
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
	aiCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	result, err := client.Explain(aiCtx, aiFinding)
	if err != nil {
		if opts.verbose {
			fmt.Fprintf(stderr, "ai failed: %v\n", err)
		}
		return
	}
	finding.AI = result
	handleUnstructuredAI(finding, stderr)
	if finding.AI != nil && cfg.CacheEnabled {
		_ = cache.New().Set(cache.Key(aiFinding), result)
	}
}

func enrichFindingForAI(ctx context.Context, reader kube.Reader, k kube.Kubectl, opts options, resourceArg string, finding analyzer.Finding) analyzer.Finding {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	filters := splitCSV(opts.filters)
	if len(filters) == 0 {
		filters = analyzer.SmartFiltersFor(resourceArg, finding.Status)
	}
	finding.Evidence = append(finding.Evidence, analyzer.Evidence{Label: "Analyzers selected", Value: strings.Join(filters, ",")})
	if !isTargetPodOnly(filters) {
		relatedAnalyzer := analyzer.New(reader, analyzer.Options{
			Namespace:      opts.namespace,
			AllNS:          opts.allNS,
			IncludeLogs:    false,
			Redact:         opts.redact || opts.paranoid,
			Filters:        filters,
			LabelSelector:  opts.labelSelector,
			MaxConcurrency: 6,
		})
		report := relatedAnalyzer.ScanReport(ctx)
		related := relatedFindings(finding, report.Findings, 8)
		for i, item := range related {
			finding.Evidence = append(finding.Evidence, analyzer.Evidence{Label: fmt.Sprintf("Related analyzer finding %d", i+1), Value: summarizeRelatedFinding(item)})
		}
		for _, skipped := range report.Skipped {
			if len(finding.Evidence) >= 32 {
				break
			}
			finding.Evidence = append(finding.Evidence, analyzer.Evidence{Label: "Analyzer skipped " + skipped.Name, Value: trimEvidence(skipped.Reason, 220)})
		}
	}
	for _, evidence := range collectMetricsEvidence(ctx, k, opts, finding) {
		finding.Evidence = append(finding.Evidence, evidence)
	}
	if strings.EqualFold(finding.Status, "ExecFormatError") {
		finding.Evidence = append(finding.Evidence, inspectCurrentImagePlatforms(ctx, finding)...)
	}
	finding.Evidence = boundEvidence(finding.Evidence, 40)
	finding.Logs = boundLogs(finding.Logs, 6)
	return finding
}

func isTargetPodOnly(filters []string) bool {
	return len(filters) == 1 && (strings.EqualFold(filters[0], "pod") || strings.EqualFold(filters[0], "pods"))
}

func relatedFindings(target analyzer.Finding, candidates []analyzer.Finding, limit int) []analyzer.Finding {
	scored := []struct {
		f     analyzer.Finding
		score int
	}{}
	for _, candidate := range candidates {
		score := relatedScore(target, candidate)
		if score <= 0 {
			continue
		}
		scored = append(scored, struct {
			f     analyzer.Finding
			score int
		}{f: candidate, score: score})
	}
	slices.SortFunc(scored, func(a, b struct {
		f     analyzer.Finding
		score int
	}) int {
		if a.score != b.score {
			return b.score - a.score
		}
		return severityRankForCLI(b.f.Severity) - severityRankForCLI(a.f.Severity)
	})
	out := []analyzer.Finding{}
	for _, item := range scored {
		if len(out) >= limit {
			break
		}
		out = append(out, item.f)
	}
	return out
}

func relatedScore(target, candidate analyzer.Finding) int {
	score := 0
	if candidate.Namespace == target.Namespace {
		score += 2
	}
	if candidate.ResourceKind == target.ResourceKind && candidate.ResourceName == target.ResourceName {
		score += 8
	}
	if target.PodName != "" && candidate.PodName == target.PodName {
		score += 6
	}
	if candidate.Category == target.Category && candidate.Category != "" {
		score += 2
	}
	text := strings.ToLower(candidate.Summary + " " + candidate.Status + " " + strings.Join(candidate.OwnerChain, " "))
	for _, needle := range []string{strings.ToLower(target.ResourceName), strings.ToLower(target.PodName)} {
		if needle != "" && strings.Contains(text, needle) {
			score += 3
		}
	}
	if candidate.ResourceKind == "Node" || candidate.ResourceKind == "PersistentVolumeClaim" || candidate.ResourceKind == "Service" || candidate.ResourceKind == "Ingress" || candidate.ResourceKind == "ConfigMap" {
		score += 1
	}
	return score
}

func summarizeRelatedFinding(f analyzer.Finding) string {
	parts := []string{
		f.Severity,
		f.Namespace,
		f.ResourceKind + "/" + f.ResourceName,
		f.Status,
		trimEvidence(f.Summary, 240),
	}
	if f.PodName != "" {
		parts = append(parts, "pod/"+f.PodName)
	}
	for _, evidence := range f.Evidence {
		if len(parts) >= 8 {
			break
		}
		parts = append(parts, evidence.Label+"="+trimEvidence(evidence.Value, 140))
	}
	return strings.Join(parts, " | ")
}

func collectMetricsEvidence(ctx context.Context, k kube.Kubectl, opts options, finding analyzer.Finding) []analyzer.Evidence {
	out := []analyzer.Evidence{}
	namespace := firstArg([]string{finding.Namespace, opts.namespace}, "default")
	if finding.PodName != "" {
		if text, err := kubectlTop(ctx, k, "pod", finding.PodName, "-n", namespace, "--containers"); err == nil && text != "" {
			out = append(out, analyzer.Evidence{Label: "Metrics pod containers", Value: text})
		}
	}
	if strings.EqualFold(finding.ResourceKind, "Node") || hasEvidenceLabel(finding.Evidence, "Node") {
		if text, err := kubectlTop(ctx, k, "nodes"); err == nil && text != "" {
			out = append(out, analyzer.Evidence{Label: "Metrics nodes", Value: text})
		}
	}
	if text, err := kubectlTop(ctx, k, "pods", "-n", namespace); err == nil && text != "" {
		out = append(out, analyzer.Evidence{Label: "Metrics namespace pods", Value: text})
	}
	return out
}

func inspectCurrentImagePlatforms(ctx context.Context, finding analyzer.Finding) []analyzer.Evidence {
	platform, ok := nodePlatformFromFinding(finding)
	if !ok {
		return nil
	}
	out := []analyzer.Evidence{}
	for _, reference := range findingContainerImages(finding, 2) {
		result, err := image.Inspect(ctx, reference)
		if err != nil {
			out = append(out, analyzer.Evidence{Label: "Image manifest " + reference, Value: trimEvidence(err.Error(), 320)})
			continue
		}
		platforms := make([]string, 0, len(result.Platforms))
		for _, candidate := range result.Platforms {
			platforms = append(platforms, candidate.String())
		}
		value := strings.Join(platforms, ", ")
		if result.Supports(platform.OS, platform.Architecture) {
			value += " | supports target " + platform.String()
		} else {
			value += " | does not support target " + platform.String()
			if candidates, err := image.DiscoverTrusted(ctx, reference, platform, 5); err == nil {
				candidateRefs := make([]string, 0, len(candidates))
				for _, candidate := range candidates {
					candidateRefs = append(candidateRefs, candidate.Reference)
					findingLabel := fmt.Sprintf("Ranked public image candidate (score %d)", candidate.TrustScore)
					out = append(out, analyzer.Evidence{Label: findingLabel, Value: candidate.Reference + " | " + candidate.TrustReason})
				}
				out = append(out, analyzer.Evidence{Label: "Verified compatible image candidates", Value: strings.Join(candidateRefs, ", ")})
			} else {
				out = append(out, analyzer.Evidence{Label: "Compatible image candidate lookup", Value: trimEvidence(err.Error(), 240)})
			}
		}
		out = append(out, analyzer.Evidence{Label: "Image manifest " + reference, Value: value})
	}
	return out
}

func kubectlTop(ctx context.Context, k kube.Kubectl, args ...string) (string, error) {
	full := append([]string{"top"}, args...)
	out, err := k.Run(ctx, full...)
	if err != nil {
		return "", err
	}
	return trimEvidence(string(out), 1600), nil
}

func hasEvidenceLabel(items []analyzer.Evidence, label string) bool {
	for _, item := range items {
		if strings.EqualFold(item.Label, label) && strings.TrimSpace(item.Value) != "" {
			return true
		}
	}
	return false
}

func boundEvidence(items []analyzer.Evidence, limit int) []analyzer.Evidence {
	if len(items) <= limit {
		for i := range items {
			items[i].Value = trimEvidence(items[i].Value, 1800)
		}
		return items
	}
	out := append([]analyzer.Evidence{}, items[:limit]...)
	for i := range out {
		out[i].Value = trimEvidence(out[i].Value, 1800)
	}
	return out
}

func boundLogs(items []analyzer.LogSnippet, limit int) []analyzer.LogSnippet {
	if len(items) > limit {
		items = append([]analyzer.LogSnippet{}, items[:limit]...)
	}
	for i := range items {
		items[i].Text = trimEvidence(items[i].Text, 3000)
	}
	return items
}

func trimEvidence(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	if limit <= 3 {
		return value[:limit]
	}
	return value[:limit-3] + "..."
}

func severityRankForCLI(severity string) int {
	switch strings.ToLower(severity) {
	case "critical":
		return 5
	case "high":
		return 4
	case "medium":
		return 3
	case "low":
		return 2
	case "info":
		return 1
	default:
		return 0
	}
}

func applyAIPatchIfSafe(ctx context.Context, plan fix.Plan, finding analyzer.Finding, stderr io.Writer, verbose bool) fix.Plan {
	if finding.AI == nil {
		return plan
	}
	patch := aiPatchCandidate(*finding.AI)
	if patch == "" {
		return plan
	}
	if strings.Contains(patch, "TODO_") {
		plan.Warnings = appendUniqueString(plan.Warnings, "AI patch was rejected because it still contains TODO placeholders.")
		return plan
	}
	if strings.EqualFold(plan.Strategy, "fix-architecture") {
		if err := verifyArchitecturePatch(ctx, finding, patch); err != nil {
			plan.Warnings = appendUniqueString(plan.Warnings, "AI architecture patch rejected: "+err.Error())
			if verbose {
				fmt.Fprintf(stderr, "ai architecture patch rejected: %v\n", err)
			}
			return plan
		}
	}
	if err := shadow.ValidateRevisedPatch(plan.PatchYAML(), patch, plan.Strategy); err != nil {
		if reviewErr := validateReviewOnlyAIPatch(patch); reviewErr == nil {
			plan = fix.WithReviewOnlyAIPatch(plan, patch, "AI patch is review-only because it is outside the shadow/apply allowlist: "+err.Error())
			if verbose {
				fmt.Fprintf(stderr, "ai patch marked review-only: %v\n", err)
			}
			return plan
		}
		plan.Warnings = appendUniqueString(plan.Warnings, "AI patch rejected by safety validation: "+err.Error())
		if verbose {
			fmt.Fprintf(stderr, "ai patch rejected: %v\n", err)
		}
		return plan
	}
	confidence := finding.AI.Confidence
	if confidence <= 0 {
		confidence = plan.Confidence
	}
	plan = fix.WithValidatedAIPatch(plan, patch, confidence)
	if finding.AI.Strategy != "" && !strings.EqualFold(finding.AI.Strategy, plan.Strategy) {
		plan.Warnings = appendUniqueString(plan.Warnings, "AI strategy "+finding.AI.Strategy+" was normalized to planner strategy "+plan.Strategy+" for safety validation.")
	}
	return plan
}

func applyTrustedImageCandidate(ctx context.Context, plan fix.Plan, finding analyzer.Finding, stderr io.Writer, verbose bool) fix.Plan {
	if !strings.EqualFold(plan.Strategy, "fix-architecture") || plan.ApplyEligible {
		return plan
	}
	candidate, score := bestTrustedImageCandidate(finding)
	if candidate == "" || score < 65 {
		return plan
	}
	if !strings.Contains(candidate, "@sha256:") {
		return plan
	}
	container := findingContainerName(finding)
	if container == "" {
		return plan
	}
	proposed := fix.Concretize(plan, fix.ConcreteOptions{Container: container, Image: candidate})
	if !proposed.ApplyEligible {
		return plan
	}
	// Candidates are produced only after the fixed catalog discovery path confirms
	// their platform and pins the manifest digest. Avoid a second registry request.
	proposed.Warnings = appendUniqueString(proposed.Warnings, fmt.Sprintf("Fixora selected ranked public image candidate %s (trust score %d); review the diff and shadow result before delivery.", candidate, score))
	proposed.Guardrails = appendUniqueString(proposed.Guardrails, "trusted-public-image-candidate")
	return proposed
}

func bestTrustedImageCandidate(finding analyzer.Finding) (string, int) {
	best, bestScore := "", 0
	for _, evidence := range finding.Evidence {
		var score int
		if _, err := fmt.Sscanf(evidence.Label, "Ranked public image candidate (score %d)", &score); err != nil {
			continue
		}
		reference := strings.TrimSpace(strings.SplitN(evidence.Value, "|", 2)[0])
		if reference != "" && score > bestScore {
			best, bestScore = reference, score
		}
	}
	return best, bestScore
}

func findingContainerName(finding analyzer.Finding) string {
	for _, evidence := range finding.Evidence {
		const prefix = "container image "
		if strings.HasPrefix(strings.ToLower(evidence.Label), prefix) {
			return strings.TrimSpace(evidence.Label[len(prefix):])
		}
	}
	return ""
}

func verifyArchitecturePatch(ctx context.Context, finding analyzer.Finding, patch string) error {
	platform, ok := nodePlatformFromFinding(finding)
	if !ok {
		return fmt.Errorf("target node architecture is not available")
	}
	images, err := patchImages(patch)
	if err != nil {
		return err
	}
	if len(images) == 0 {
		return fmt.Errorf("AI patch does not contain an image")
	}
	for _, reference := range images {
		if err := verifyImageRegistry(finding, reference); err != nil {
			return err
		}
		result, err := image.Inspect(ctx, reference)
		if err != nil {
			return err
		}
		if !result.Supports(platform.OS, platform.Architecture) {
			return fmt.Errorf("image %s does not support target platform %s", reference, platform.String())
		}
	}
	return nil
}

func verifyImageRegistry(finding analyzer.Finding, candidate string) error {
	allowedRepositories := map[string]bool{}
	for _, current := range findingContainerImages(finding, 8) {
		repository, err := image.Repository(current)
		if err == nil {
			allowedRepositories[repository] = true
		}
	}
	for _, trusted := range trustedCandidateReferences(finding) {
		repository, err := image.Repository(trusted)
		if err == nil {
			allowedRepositories[repository] = true
		}
	}
	if len(allowedRepositories) == 0 {
		return fmt.Errorf("current image registry is not available for candidate validation")
	}
	repository, err := image.Repository(candidate)
	if err != nil {
		return err
	}
	if !allowedRepositories[repository] {
		return fmt.Errorf("candidate image repository %s is not the current repository or a verified trusted candidate", repository)
	}
	return nil
}

func trustedCandidateReferences(finding analyzer.Finding) []string {
	values := []string{}
	for _, evidence := range finding.Evidence {
		if !strings.HasPrefix(evidence.Label, "Ranked public image candidate (score ") {
			continue
		}
		reference := strings.TrimSpace(strings.SplitN(evidence.Value, "|", 2)[0])
		if reference != "" {
			values = append(values, reference)
		}
	}
	return values
}

func nodePlatformFromFinding(finding analyzer.Finding) (image.Platform, bool) {
	for _, evidence := range finding.Evidence {
		if !strings.EqualFold(evidence.Label, "Node platform") {
			continue
		}
		parts := strings.Split(strings.TrimSpace(evidence.Value), "/")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return image.Platform{}, false
		}
		return image.Platform{OS: parts[0], Architecture: parts[1]}, true
	}
	return image.Platform{}, false
}

func findingContainerImages(finding analyzer.Finding, limit int) []string {
	images := []string{}
	seen := map[string]bool{}
	for _, evidence := range finding.Evidence {
		if !strings.HasPrefix(strings.ToLower(evidence.Label), "container image ") {
			continue
		}
		reference := strings.TrimSpace(evidence.Value)
		if reference == "" || seen[reference] {
			continue
		}
		seen[reference] = true
		images = append(images, reference)
		if len(images) >= limit {
			break
		}
	}
	return images
}

func patchImages(patch string) ([]string, error) {
	data, err := yaml.ToJSON([]byte(patch))
	if err != nil {
		return nil, fmt.Errorf("parse AI patch image: %w", err)
	}
	var obj any
	if err := json.Unmarshal(data, &obj); err != nil {
		return nil, fmt.Errorf("decode AI patch image: %w", err)
	}
	images := []string{}
	seen := map[string]bool{}
	var walk func(any)
	walk = func(value any) {
		switch current := value.(type) {
		case map[string]any:
			for key, nested := range current {
				if key == "image" {
					if reference, ok := nested.(string); ok && strings.TrimSpace(reference) != "" && !seen[reference] {
						seen[reference] = true
						images = append(images, strings.TrimSpace(reference))
					}
				}
				walk(nested)
			}
		case []any:
			for _, nested := range current {
				walk(nested)
			}
		}
	}
	walk(obj)
	return images, nil
}

func validateReviewOnlyAIPatch(patch string) error {
	patch = strings.TrimSpace(patch)
	if patch == "" {
		return fmt.Errorf("empty AI patch")
	}
	if strings.Contains(patch, "\n---") || strings.HasPrefix(patch, "---") {
		return fmt.Errorf("multi-document AI patches are not allowed")
	}
	data, err := yaml.ToJSON([]byte(patch))
	if err != nil {
		return fmt.Errorf("invalid AI patch YAML: %w", err)
	}
	var obj map[string]any
	if err := json.Unmarshal(data, &obj); err != nil {
		return fmt.Errorf("AI patch must be a YAML object: %w", err)
	}
	if strings.EqualFold(fmt.Sprint(obj["kind"]), "Secret") {
		return fmt.Errorf("AI patches must not create or modify Secret resources")
	}
	if meta, ok := obj["metadata"].(map[string]any); ok {
		if _, ok := meta["labels"]; ok {
			return fmt.Errorf("AI review-only patch contains unsafe field metadata.labels")
		}
		if _, ok := meta["annotations"]; ok {
			return fmt.Errorf("AI review-only patch contains unsafe field metadata.annotations")
		}
	}
	lower := strings.ToLower(patch)
	for _, marker := range []string{
		"hostpath:",
		"privileged: true",
		"serviceaccountname:",
		"hostnetwork: true",
		"hostpid: true",
		"hostipc: true",
		"nodeselector:",
		"tolerations:",
		"ownerreferences:",
	} {
		if strings.Contains(lower, marker) {
			return fmt.Errorf("AI review-only patch contains unsafe field %s", strings.TrimSuffix(marker, ":"))
		}
	}
	return nil
}

func aiPatchCandidate(result analyzer.AIResult) string {
	if strings.TrimSpace(result.PatchYAML) != "" {
		return trimFencedYAML(result.PatchYAML)
	}
	if strings.Contains(result.RecommendedFix, "```") {
		return trimFencedYAML(result.RecommendedFix)
	}
	return ""
}

func trimFencedYAML(value string) string {
	value = strings.TrimSpace(value)
	for _, marker := range []string{"```yaml", "```yml", "```"} {
		start := strings.Index(value, marker)
		if start == -1 {
			continue
		}
		after := value[start+len(marker):]
		end := strings.Index(after, "```")
		if end == -1 {
			break
		}
		return strings.TrimSpace(after[:end])
	}
	return value
}

func appendUniqueString(values []string, next string) []string {
	next = strings.TrimSpace(next)
	if next == "" {
		return values
	}
	for _, value := range values {
		if value == next {
			return values
		}
	}
	return append(values, next)
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func runAIDoctor(args []string, stdout, stderr io.Writer) int {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return output.Write(stdout, "json", ai.Doctor(ctx))
}

func runProfiles(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		switch args[0] {
		case "set":
			if len(args) < 2 {
				fmt.Fprintln(stderr, "error: profiles set requires a profile name")
				return 1
			}
			cfg, err := config.Load()
			if err != nil {
				fmt.Fprintf(stderr, "error: %v\n", err)
				return 1
			}
			profileName := args[1]
			// Check if profile exists
			exists := false
			for _, p := range config.Profiles() {
				if strings.EqualFold(p, profileName) {
					profileName = p
					exists = true
					break
				}
			}
			if !exists {
				fmt.Fprintf(stderr, "warning: profile %q is not defined yet, but setting it anyway\n", profileName)
			}
			cfg.Profile = profileName
			if err := config.Save(cfg); err != nil {
				fmt.Fprintf(stderr, "error: %v\n", err)
				return 1
			}
			fmt.Fprintf(stdout, "profile set to %s\n", profileName)
			return 0
		case "show":
			cfg, err := config.Load()
			if err != nil {
				fmt.Fprintf(stderr, "error: %v\n", err)
				return 1
			}
			fmt.Fprintf(stdout, "active profile: %s\n", config.FirstNonEmpty(cfg.Profile, "sre"))
			return 0
		case "create":
			if len(args) < 3 {
				fmt.Fprintln(stderr, "error: profiles create requires a name and prompt text")
				return 1
			}
			cfg, err := config.Load()
			if err != nil {
				fmt.Fprintf(stderr, "error: %v\n", err)
				return 1
			}
			pName := strings.ToLower(args[1])
			pPrompt := strings.Join(args[2:], " ")
			if cfg.CustomProfiles == nil {
				cfg.CustomProfiles = make(map[string]string)
			}
			cfg.CustomProfiles[pName] = pPrompt
			cfg.Profile = pName
			if err := config.Save(cfg); err != nil {
				fmt.Fprintf(stderr, "error: %v\n", err)
				return 1
			}
			fmt.Fprintf(stdout, "custom profile %q created and set as active\n", pName)
			return 0
		default:
			fmt.Fprintf(stderr, "error: unknown profiles command %q\n", args[0])
			return 2
		}
	}

	if isTerminal(os.Stdout) {
		return runInteractiveProfiles(stdout, stderr)
	}

	return output.Write(stdout, "json", config.Profiles())
}

func isTerminal(f *os.File) bool {
	stat, err := f.Stat()
	if err != nil {
		return false
	}
	return (stat.Mode() & os.ModeCharDevice) != 0
}

func runInteractiveProfiles(stdout, stderr io.Writer) int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	reader := bufio.NewReader(os.Stdin)
	promptProfiles := config.Profiles()
	activeProfile := config.FirstNonEmpty(cfg.Profile, "sre")
	activeIdx := 1
	for i, p := range promptProfiles {
		if strings.EqualFold(p, activeProfile) {
			activeIdx = i + 1
			break
		}
	}

	fmt.Fprintln(stdout, "Select AI Prompt Profile:")
	for i, p := range promptProfiles {
		suffix := ""
		if strings.EqualFold(p, activeProfile) {
			suffix = " (active)"
		}
		desc := config.ProfilePrompt(p)
		if len(desc) > 70 {
			desc = desc[:67] + "..."
		}
		fmt.Fprintf(stdout, "  %d. %s%s\n     %s\n", i+1, p, suffix, desc)
	}
	fmt.Fprintf(stdout, "  %d. Create Custom Profile\n", len(promptProfiles)+1)
	fmt.Fprintf(stdout, "Enter choice [%d]: ", activeIdx)

	choiceStr, _ := reader.ReadString('\n')
	choiceStr = strings.TrimSpace(choiceStr)
	choice := activeIdx
	if choiceStr != "" {
		if c, err := strconv.Atoi(choiceStr); err == nil {
			choice = c
		}
	}

	var selectedProfile string
	if choice > 0 && choice <= len(promptProfiles) {
		selectedProfile = promptProfiles[choice-1]
	} else if choice == len(promptProfiles)+1 {
		fmt.Fprint(stdout, "\nEnter Custom Profile Name: ")
		pName, _ := reader.ReadString('\n')
		pName = strings.TrimSpace(pName)
		if pName == "" {
			fmt.Fprintln(stderr, "error: profile name cannot be empty")
			return 1
		}
		fmt.Fprint(stdout, "Enter Custom Prompt Instructions: ")
		pPrompt, _ := reader.ReadString('\n')
		pPrompt = strings.TrimSpace(pPrompt)
		if pPrompt == "" {
			fmt.Fprintln(stderr, "error: prompt instructions cannot be empty")
			return 1
		}
		if cfg.CustomProfiles == nil {
			cfg.CustomProfiles = make(map[string]string)
		}
		cfg.CustomProfiles[strings.ToLower(pName)] = pPrompt
		selectedProfile = pName
	} else {
		fmt.Fprintf(stderr, "error: invalid choice %d\n", choice)
		return 1
	}

	cfg.Profile = selectedProfile
	if err := config.Save(cfg); err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "\nProfile set to: %s\n", selectedProfile)
	return 0
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
	if len(args) > 0 && args[0] == "help" {
		fmt.Fprintln(stdout, "usage: kubectl fixora auth set <provider> <api-key> [base-url] [model]")
		return 0
	}

	var authArgs []string
	if len(args) > 0 {
		if args[0] == "set" {
			authArgs = args[1:]
		} else {
			fmt.Fprintf(stderr, "error: unknown auth command %q\n", args[0])
			return 2
		}
	}

	if err := config.Auth(authArgs); err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, "AI credentials saved")
	return 0
}

func runConfig(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "view" {
		viewArgs := []string{}
		if len(args) > 1 {
			viewArgs = args[1:]
		}
		cfg, err := config.Load()
		if err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
		if hasArg(viewArgs, "--resolved") || hasArg(viewArgs, "--show-sources") {
			resolved, err := config.Resolved()
			if err != nil {
				fmt.Fprintf(stderr, "error: %v\n", err)
				return 1
			}
			if !hasArg(viewArgs, "--show-sources") {
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
		if len(args) == 1 && isTerminal(os.Stdout) {
			return runInteractiveProfileManager(stdout, stderr)
		}
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
  fix <kind/name>              Guided walkthrough: root cause -> fix -> shadow -> deliver.
  coordinate <kind/name>...    Apply an ordered set of fixes together; rolls back the applied prefix on failure
                               Interactive on a TTY; pass -o json or --yes for scripted runs.
    --delivery                 How to ship a verified fix: patch, cluster, or pr (default: patch).
    --yes                      Confirm non-interactive cluster/PR delivery.
                               (--apply, --source-patch, --gitops are deprecated aliases.)
  ui                           Compact incident dashboard
  cluster                      Full-screen cluster dashboard
  doctor                       Validate access, RBAC, logs, events, metrics, Helm/GitOps CRDs

Specialist workflows:
  debug <tool>                 trace, graph, storage, rbac, dns, security, node-pressure, changes, readiness, rollback
  source <tool>                repo, validate, lint, preflight, policy-check

Setup:
  auth                         Configure AI provider credentials (interactive or set direct)
  config                       Manage local CLI configuration
  version                      Print version

Examples:
  kubectl fixora scan -A
  kubectl fixora why deployment/api -n prod
  kubectl fixora fix deployment/api -n prod
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
      --no-ai                  Disable AI remediation for this command
      --repo string            Local manifest, Helm chart, or Kustomize overlay path
      --gitops                 Prefer source-controlled patch output
      --edit-patch             Open generated patch in $VISUAL/$EDITOR before delivery
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
  status                       Show cluster access and capability summary
  doctor                       Validate RBAC, metrics, logs, events, Helm/GitOps CRDs
  filters                      List available analyzers and active filter selection
  integrations                 Detect local optional integrations and CRDs
  custom-analyzers list|add|run Manage explicit local custom analyzer executables
  serve [addr]                 Serve a local-only HTTP API for incidents/analyze
  serve --mcp                  Serve a local MCP stdio server for AI assistants
  trace <resource>             Debug Ingress/HTTPRoute/Service connectivity path
  storage                      Debug PVC/PV/StorageClass issues
  rbac [sa] [verb] [resource]  Debug service account authorization
  dns                          Debug Service DNS and CoreDNS signals
  security                     Debug policy and securityContext failures
  node-pressure                Debug node pressure, readiness, and eviction signals
  repo [path]                  Detect raw, Helm, or Kustomize source mode
  validate [path]              Render/validate local raw, Helm, or Kustomize source
  health                       Summarize namespace or cluster health
  preflight -f path            Lint and server dry-run a manifest before apply
  policy-check -f path         Run production policy checks against manifests
  watch incidents              Poll incident state until interrupted
  ui                           Show a compact terminal incident dashboard
  cluster                      Show an interactive full-screen cluster-wide dashboard
  incidents                    List current failing workloads
  fix [kind/name]              AI-assisted production remediation flow (interactive if no resource provided)
  cost nodes|workloads         Estimate node/workload costs
  predict                      Show future-risk signals from local evidence
  lint -f path                 Lint manifests, Helm chart, or Kustomize overlay
  version                      Print version
  auth                         Configure AI provider credentials (interactive or set direct)
  config view|set|unset|validate|export|reset|path
  cache path|stats|list|purge|clear
  cache add|get|remove         Configure K8sGPT-style remote cache metadata
  doctor                       Validate AI setup and configuration
  profiles                     Manage AI prompt profiles (interactive or show|set|create)
  memory list|clear            Inspect or clear local scenario memory

Global flags:
  -n, --namespace string       Namespace (default "default")
  -A, --all-namespaces         Scan all namespaces
      --context string         Kube context
  -l, -L, --selector string    Label selector, for example app=api,tier!=cache
  -o, --output string          text, json, yaml, markdown, sarif, junit, prometheus
      --include-logs           Include bounded logs in evidence
      --ai                     Use OpenAI-compatible AI analysis
      --no-ai                  Disable AI remediation for this command
      --auto-fix               Generate explicit local fix plan
      --apply                  Apply generated local patch
      --yes                    Confirm non-interactive verified PR/MR delivery
      --edit-patch             Open generated patch in $VISUAL/$EDITOR before shadow/apply/source delivery
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
	case "fix":
		if len(rest) > 0 {
			return analyzer.SmartFiltersFor(rest[0], "")
		}
	case "health":
		return analyzer.ComprehensiveDiagnosticFilters()
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

func runInteractiveProfileManager(stdout, stderr io.Writer) int {
	// Raw load: editing/switching profiles must not fuse resolved profile
	// values into the top-level config on save.
	cfg, err := config.LoadStored()
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	reader := bufio.NewReader(os.Stdin)
	for {
		profiles := []string{}
		for name := range cfg.Profiles {
			profiles = append(profiles, name)
		}
		slices.Sort(profiles)

		fmt.Fprintln(stdout, "\n--- Configuration Profiles ---")
		if len(profiles) == 0 {
			fmt.Fprintln(stdout, "No profiles configured.")
		} else {
			activeProfile := cfg.ActiveProfile
			for i, p := range profiles {
				suffix := ""
				if p == activeProfile {
					suffix = " (active)"
				}
				fmt.Fprintf(stdout, "  %d. %s%s\n", i+1, p, suffix)
			}
		}
		fmt.Fprintf(stdout, "  %d. Create New Profile\n", len(profiles)+1)
		fmt.Fprintln(stdout, "  0. Exit")
		fmt.Fprint(stdout, "Enter choice: ")

		choiceStr, _ := reader.ReadString('\n')
		choiceStr = strings.TrimSpace(choiceStr)
		if choiceStr == "0" || choiceStr == "" {
			break
		}

		choice, err := strconv.Atoi(choiceStr)
		if err != nil || choice < 0 || choice > len(profiles)+1 {
			fmt.Fprintln(stdout, "Invalid choice.")
			continue
		}

		if choice == len(profiles)+1 {
			// Create new profile
			fmt.Fprint(stdout, "Enter new profile name: ")
			pName, _ := reader.ReadString('\n')
			pName = strings.TrimSpace(pName)
			if pName == "" {
				fmt.Fprintln(stdout, "Profile name cannot be empty.")
				continue
			}
			if _, exists := cfg.Profiles[pName]; exists {
				fmt.Fprintf(stdout, "Profile %q already exists.\n", pName)
				continue
			}

			if cfg.Profiles == nil {
				cfg.Profiles = make(map[string]config.Settings)
			}
			cfg.Profiles[pName] = config.BlankProfileSettings()
			cfg.ActiveProfile = pName

			if err := config.Save(cfg); err != nil {
				fmt.Fprintf(stderr, "error saving config: %v\n", err)
				return 1
			}
			fmt.Fprintf(stdout, "Profile %q created, reset to defaults, and set as active.\n", pName)
			continue
		}

		// Show profile details
		pName := profiles[choice-1]
		settings := cfg.Profiles[pName]
		showProfileDetails(stdout, pName, settings, pName == cfg.ActiveProfile)

		fmt.Fprintln(stdout, "\nProfile Options:")
		fmt.Fprintln(stdout, "  1. Switch to this profile (make active)")
		fmt.Fprintln(stdout, "  2. Delete this profile")
		fmt.Fprintln(stdout, "  0. Back")
		fmt.Fprint(stdout, "Enter choice: ")

		optStr, _ := reader.ReadString('\n')
		optStr = strings.TrimSpace(optStr)
		if optStr == "1" {
			cfg.ActiveProfile = pName
			if err := config.Save(cfg); err != nil {
				fmt.Fprintf(stderr, "error saving config: %v\n", err)
				return 1
			}
			fmt.Fprintf(stdout, "Switched to profile %q.\n", pName)
		} else if optStr == "2" {
			delete(cfg.Profiles, pName)
			if cfg.ActiveProfile == pName {
				cfg.ActiveProfile = ""
			}
			if err := config.Save(cfg); err != nil {
				fmt.Fprintf(stderr, "error saving config: %v\n", err)
				return 1
			}
			fmt.Fprintf(stdout, "Profile %q deleted.\n", pName)
		}
	}
	return 0
}

func showProfileDetails(w io.Writer, name string, s config.Settings, isActive bool) {
	fmt.Fprintf(w, "\n--- Profile: %s ---\n", name)
	if isActive {
		fmt.Fprintln(w, "Status: Active")
	} else {
		fmt.Fprintln(w, "Status: Inactive")
	}
	fmt.Fprintf(w, "  AI Provider:  %s\n", config.FirstNonEmpty(s.AIProvider, "<not configured>"))
	fmt.Fprintf(w, "  AI Base URL:  %s\n", config.FirstNonEmpty(s.AIBaseURL, "<default>"))
	fmt.Fprintf(w, "  AI Model:     %s\n", config.FirstNonEmpty(s.AIModel, "<default>"))
	keyStatus := "<not set>"
	if s.AIAPIKey != "" {
		keyStatus = "Configured (Redacted)"
	}
	fmt.Fprintf(w, "  AI API Key:   %s\n", keyStatus)
	fmt.Fprintf(w, "  Prompt Prof:  %s\n", config.FirstNonEmpty(s.Profile, "sre"))
	fmt.Fprintf(w, "  Namespace:    %s\n", config.FirstNonEmpty(s.Namespace, "<not set>"))
	fmt.Fprintf(w, "  Timeout:      %s\n", config.FirstNonEmpty(s.Timeout, "<default>"))
	fmt.Fprintf(w, "  Default Out:  %s\n", config.FirstNonEmpty(s.DefaultOutput, "<default>"))

	valOrD := func(b *bool) string {
		if b == nil {
			return "<default>"
		}
		return fmt.Sprintf("%t", *b)
	}
	fmt.Fprintf(w, "  Redact:       %s\n", valOrD(s.Redact))
	fmt.Fprintf(w, "  Paranoid:     %s\n", valOrD(s.Paranoid))
	fmt.Fprintf(w, "  ApplyDryRun:  %s\n", valOrD(s.ApplyDryRun))
}

// fail writes a standardized error and, when provided, an actionable next step.
// It always reports exit code 1; callers needing another code should print via
// failNext and return their own code.
func fail(w io.Writer, msg, next string) int {
	failNext(w, msg, next)
	return 1
}

func failNext(w io.Writer, msg, next string) {
	fmt.Fprintf(w, "error: %s\n", msg)
	if strings.TrimSpace(next) != "" {
		fmt.Fprintf(w, "Next: %s\n", next)
	}
}

// policyFromConfig merges config overrides onto the safe default patch policy.
// A non-empty allowlist replaces the defaults; an unparseable quantity keeps
// the default for that dimension (never disables the ceiling).
func policyFromConfig(cfg config.Config) shadow.PatchPolicy {
	policy := shadow.DefaultPatchPolicy()
	if allowed := validRegistryPatterns(cfg.AllowedImageRegistries); len(allowed) > 0 {
		policy.AllowedRegistries = allowed
	}
	if cfg.MaxPatchMemory != "" {
		if q, err := resource.ParseQuantity(cfg.MaxPatchMemory); err == nil && q.Sign() > 0 {
			policy.MaxMemoryBytes = q.Value()
		}
	}
	if cfg.MaxPatchCPU != "" {
		if q, err := resource.ParseQuantity(cfg.MaxPatchCPU); err == nil && q.Sign() > 0 {
			policy.MaxCPUMillicores = q.MilliValue()
		}
	}
	return policy
}

func validRegistryPatterns(values []string) []string {
	out := []string{}
	for _, value := range values {
		pattern := strings.ToLower(strings.TrimSpace(value))
		if pattern == "" || strings.Contains(pattern, "/") {
			continue
		}
		if _, err := path.Match(pattern, "registry.example.com"); err != nil {
			continue
		}
		out = append(out, pattern)
	}
	return out
}
