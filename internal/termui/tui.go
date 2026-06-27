package termui

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/fixora/kubectl-fixora/internal/ai"
	"github.com/fixora/kubectl-fixora/internal/analyzer"
	"github.com/fixora/kubectl-fixora/internal/config"
	"github.com/fixora/kubectl-fixora/internal/fix"
	"github.com/fixora/kubectl-fixora/internal/kube"
	"github.com/fixora/kubectl-fixora/internal/ops"
	"github.com/fixora/kubectl-fixora/internal/repo"
	"github.com/fixora/kubectl-fixora/internal/shadow"
)

type TUIOptions struct {
	Context       string
	Namespace     string
	AllNS         bool
	IncludeLogs   bool
	Redact        bool
	UnsafeAI      bool
	Filters       []string
	LabelSelector string
	Refresh       time.Duration
	ScanTimeout   time.Duration
	ApplyDryRun   bool
	ShadowTimeout time.Duration
	ShadowRetries int
	KeepShadow    bool
	ShadowEgress  string
	RepoPath      string
	Branch        string
	PRBase        string
	PRTitle       string
	AIProvider    string
	Output        io.Writer
	NoAltScreen   bool
}

type tuiModel struct {
	ctx         context.Context
	k           kube.Reader
	a           analyzer.Analyzer
	opts        TUIOptions
	table       table.Model
	report      analyzer.ScanReport
	selected    analyzer.Finding
	plan        fix.Plan
	tab         int
	palette     bool
	command     bool
	filter      string
	commandIn   string
	help        bool
	width       int
	height      int
	lastScan    time.Time
	err         error
	message     string
	renderer    *glamour.TermRenderer
	spinner     spinner.Model
	scanning    bool
	zoomed      bool
	nsList      list.Model
	switchingNS bool
	graphList   list.Model
	shadow      shadow.Result
	shadowing   bool
	aiRunning   bool
	deepScan    bool
}

type tickMsg time.Time
type scanMsg struct {
	report analyzer.ScanReport
	err    error
}
type applyMsg struct {
	message string
	err     error
}
type shadowMsg struct {
	result shadow.Result
	err    error
}
type aiMsg struct {
	result *analyzer.AIResult
	err    error
}
type deliveryMsg struct {
	message string
	err     error
}
type configReloadMsg struct{}

var (
	borderColor = lipgloss.Color("62")
	mutedColor  = lipgloss.Color("245")
	highColor   = lipgloss.Color("203")
	medColor    = lipgloss.Color("220")
	okColor     = lipgloss.Color("78")
	panelStyle  = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(borderColor).Padding(0, 1)
	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("81"))
	mutedStyle  = lipgloss.NewStyle().Foreground(mutedColor)
)

var tuiTabs = []string{"Incidents", "Workloads", "Network", "Storage", "Security", "Fix Plans", "Events", "Logs", "Graph", "Settings"}

func RunTUI(ctx context.Context, k kube.Reader, opts TUIOptions) error {
	if opts.AIProvider == "" {
		if cfg, err := config.Load(); err == nil {
			opts.AIProvider = cfg.AIProvider
		}
	}
	if opts.Refresh <= 0 {
		opts.Refresh = 30 * time.Second
	}
	if opts.ScanTimeout <= 0 {
		opts.ScanTimeout = 60 * time.Second
	}
	if len(opts.Filters) == 0 {
		opts.Filters = fastTUIFilters()
	}
	a := newTUIAnalyzer(k, opts)
	columns := []table.Column{
		{Title: "SEV", Width: 8},
		{Title: "NS", Width: 12},
		{Title: "RESOURCE", Width: 28},
		{Title: "STATUS", Width: 20},
	}
	t := table.New(table.WithColumns(columns), table.WithFocused(true), table.WithHeight(12))
	t.SetStyles(tableStyles())
	renderer, _ := glamour.NewTermRenderer(glamour.WithAutoStyle(), glamour.WithWordWrap(78))

	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("205"))

	nl := list.New([]list.Item{}, list.NewDefaultDelegate(), 0, 0)
	nl.Title = "Select Namespace"
	nl.SetShowStatusBar(false)
	nl.SetFilteringEnabled(true)

	gl := list.New([]list.Item{}, list.NewDefaultDelegate(), 0, 0)
	gl.Title = "Graph Pivot Nodes"
	gl.SetShowStatusBar(false)
	gl.SetFilteringEnabled(false)

	m := tuiModel{ctx: ctx, k: k, a: a, opts: opts, table: t, renderer: renderer, spinner: s, scanning: true, nsList: nl, graphList: gl, deepScan: !isFastTUIFilters(opts.Filters)}
	options := []tea.ProgramOption{}
	if !opts.NoAltScreen {
		options = append(options, tea.WithAltScreen())
	}
	if opts.Output != nil {
		options = append(options, tea.WithOutput(opts.Output))
	}
	_, err := tea.NewProgram(m, options...).Run()
	return err
}

func (m tuiModel) Init() tea.Cmd {
	return tea.Batch(m.scanCmd(), tick(m.opts.Refresh), m.spinner.Tick)
}

func newTUIAnalyzer(k kube.Reader, opts TUIOptions) analyzer.Analyzer {
	return analyzer.New(k, analyzer.Options{
		Namespace:     opts.Namespace,
		AllNS:         opts.AllNS,
		IncludeLogs:   opts.IncludeLogs,
		Redact:        opts.Redact,
		Filters:       opts.Filters,
		LabelSelector: opts.LabelSelector,
	})
}

func fastTUIFilters() []string {
	return analyzer.DefaultIncidentFilters(true)
}

func isFastTUIFilters(filters []string) bool {
	return len(filters) == 1 && strings.EqualFold(filters[0], "pod")
}

func scanModeLabel(opts TUIOptions, deep bool) string {
	mode := "fast"
	if deep {
		mode = "deep"
	}
	logs := "logs=off"
	if opts.IncludeLogs {
		logs = "logs=on"
	}
	return mode + " " + logs
}

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.table.SetWidth(maxInt(60, msg.Width/2-4))
		m.table.SetHeight(maxInt(8, msg.Height-12))
		m.nsList.SetSize(msg.Width, msg.Height)
		m.graphList.SetSize(maxInt(50, msg.Width/2-8), msg.Height-12)
	case tea.KeyMsg:
		if m.switchingNS {
			switch msg.String() {
			case "esc":
				m.switchingNS = false
			case "enter":
				if i, ok := m.nsList.SelectedItem().(nsItem); ok {
					ns := string(i)
					if ns == "<All Namespaces>" {
						m.opts.Namespace = ""
						m.opts.AllNS = true
					} else {
						m.opts.Namespace = ns
						m.opts.AllNS = false
					}
					m.switchingNS = false
					m.a = newTUIAnalyzer(m.k, m.opts)
					m.scanning = true
					return m, m.scanCmd()
				}
			}
			var cmd tea.Cmd
			m.nsList, cmd = m.nsList.Update(msg)
			return m, cmd
		}
		if m.palette || m.command {
			return m.updateInput(msg)
		}
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "tab":
			m.tab = (m.tab + 1) % len(tuiTabs)
		case "shift+tab":
			m.tab = (m.tab + len(tuiTabs) - 1) % len(tuiTabs)
		case "1", "2", "3", "4", "5", "6", "7", "8", "9":
			m.tab = int(msg.String()[0] - '1')
			if m.tab >= len(tuiTabs) {
				m.tab = len(tuiTabs) - 1
			}
		case "z":
			m.zoomed = !m.zoomed
		case "n":
			m.switchingNS = true
			return m, fetchNamespacesCmd(m.ctx, m.k)
		case "C":
			m.opts.AllNS = !m.opts.AllNS
			if m.opts.AllNS {
				m.opts.Namespace = ""
			}
			m.a = newTUIAnalyzer(m.k, m.opts)
			m.scanning = true
			return m, m.scanCmd()
		case "D":
			m.deepScan = !m.deepScan
			if m.deepScan {
				m.opts.Filters = nil
				m.message = "deep scan enabled: all analyzers"
			} else {
				m.opts.Filters = fastTUIFilters()
				m.message = "fast scan enabled: failing pods only"
			}
			m.a = newTUIAnalyzer(m.k, m.opts)
			m.scanning = true
			return m, m.scanCmd()
		case "L":
			m.opts.IncludeLogs = !m.opts.IncludeLogs
			if m.opts.IncludeLogs {
				m.message = "log collection enabled for next scans"
			} else {
				m.message = "log collection disabled"
			}
			m.a = newTUIAnalyzer(m.k, m.opts)
			m.scanning = true
			return m, m.scanCmd()
		case "enter":
			return m, nil
		case "a":
			if m.tab == 5 {
				return m.applyPatchCmd(false)
			}
		case "s":
			if m.tab == 5 {
				return m.shadowVerifyCmd()
			}
		case "i":
			return m.aiAnalyzeCmd()
		case "p":
			if m.tab == 5 {
				return m.pushVerifiedCmd()
			}
		case "e":
			if m.tab == 5 {
				return m.applyPatchCmd(true)
			} else if m.tab == 9 {
				path, _ := config.Path()
				if _, err := os.Stat(path); os.IsNotExist(err) {
					b, _ := json.MarshalIndent(config.Default(), "", "  ")
					os.WriteFile(path, b, 0644)
				}
				editor := os.Getenv("EDITOR")
				if editor == "" {
					editor = "vim"
				}
				cmd := exec.Command(editor, path)
				return m, tea.ExecProcess(cmd, func(err error) tea.Msg {
					return configReloadMsg{}
				})
			}
		case "j", "down":
			if m.tab == 8 {
				m.graphList.CursorDown()
			} else {
				m.table.MoveDown(1)
				m.syncSelected()
			}
		case "k", "up":
			if m.tab == 8 {
				m.graphList.CursorUp()
			} else {
				m.table.MoveUp(1)
				m.syncSelected()
			}
		case "r":
			return m, m.scanCmd()
		case "/":
			m.palette = true
			m.filter = ""
			m.updateRows()
		case ":":
			m.command = true
			m.commandIn = ""
		case "f":
			m.tab = 5
		case "g":
			m.tab = 8
		case "?":
			m.help = !m.help
		}
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	case nsListMsg:
		var items []list.Item
		for _, ns := range msg {
			items = append(items, nsItem(ns))
		}
		m.nsList.SetItems(items)
		return m, nil
	case tickMsg:
		m.scanning = true
		return m, tea.Batch(m.scanCmd(), tick(m.opts.Refresh))
	case scanMsg:
		m.scanning = false
		m.err = msg.err
		if msg.err == nil {
			m.report = msg.report
			m.lastScan = time.Now()
			m.updateRows()
			m.syncSelected()
		}
	case applyMsg:
		if msg.err != nil {
			m.message = "apply failed: " + msg.err.Error()
		} else {
			m.message = msg.message
		}
	case shadowMsg:
		m.shadowing = false
		if msg.err != nil {
			m.message = "shadow verification failed: " + msg.err.Error()
		} else {
			m.shadow = msg.result
			if msg.result.Verified {
				m.message = fmt.Sprintf("shadow verified: parity %d%%", msg.result.Parity)
			} else {
				m.message = "shadow verification did not pass"
			}
		}
	case aiMsg:
		m.aiRunning = false
		if msg.err != nil {
			m.message = "ai analysis failed: " + msg.err.Error()
		} else if msg.result != nil {
			m.selected.AI = msg.result
			for i := range m.report.Findings {
				if m.report.Findings[i].ID == m.selected.ID {
					m.report.Findings[i].AI = msg.result
					break
				}
			}
			m.message = "ai root-cause analysis attached"
		}
	case deliveryMsg:
		if msg.err != nil {
			m.message = "delivery failed: " + msg.err.Error()
		} else {
			m.message = msg.message
		}
	case configReloadMsg:
		cfg, err := config.Load()
		if err == nil {
			m.opts.AIProvider = cfg.AIProvider
			m.opts.Redact = cfg.Redact
			m.opts.ApplyDryRun = cfg.ApplyDryRun
			m.a = newTUIAnalyzer(m.k, m.opts)
		}
	}
	var cmd tea.Cmd
	m.table, cmd = m.table.Update(msg)
	return m, cmd
}

func (m tuiModel) tabsView() string {
	var tabs []string
	for i, t := range tuiTabs {
		style := lipgloss.NewStyle().Padding(0, 1)
		if m.tab == i {
			style = style.Foreground(lipgloss.Color("229")).Background(lipgloss.Color("57"))
		} else {
			style = style.Foreground(lipgloss.Color("245"))
		}
		tabs = append(tabs, style.Render(t))
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, tabs...)
}

func (m tuiModel) View() string {
	if m.switchingNS {
		return m.nsList.View()
	}
	if m.zoomed {
		return m.detailView(m.width)
	}
	if m.width == 0 {
		m.width = 120
	}
	leftW := maxInt(56, m.width/2)
	rightW := maxInt(50, m.width-leftW-4)
	header := m.header()
	tabs := m.tabsView()
	left := panelStyle.Width(leftW - 4).Render(m.sidebar() + "\n" + m.table.View())
	right := panelStyle.Width(rightW - 4).Render(m.detailView(rightW - 8))
	body := lipgloss.JoinHorizontal(lipgloss.Top, left, " ", right)
	footer := m.footer()
	if m.palette {
		footer = panelStyle.Width(m.width - 4).Render("/ " + m.filter + "\n" + mutedStyle.Render("type to filter incidents, enter accept, esc close"))
	}
	if m.command {
		footer = panelStyle.Width(m.width - 4).Render(": " + m.commandIn + "\n" + mutedStyle.Render("commands: incidents workloads network storage security fixes events logview graph refresh deep fast logs quit"))
	}
	if m.help {
		footer = panelStyle.Width(m.width - 4).Render(helpText())
	}
	return header + "\n" + tabs + "\n" + body + "\n" + footer
}

func (m tuiModel) header() string {
	score := 100 - m.report.Summary.HighSeverity*20 - m.report.Summary.MediumSeverity*8 - m.report.Summary.LowSeverity*3
	if score < 0 {
		score = 0
	}
	age := "never"
	if !m.lastScan.IsZero() {
		age = time.Since(m.lastScan).Round(time.Second).String()
	}
	provider := firstNonEmpty(m.opts.AIProvider, "local")
	ns := m.opts.Namespace
	if m.opts.AllNS {
		ns = "<All Namespaces>"
	}
	spinView := "  "
	if m.scanning {
		spinView = " " + m.spinner.View()
	}
	line := fmt.Sprintf("%s Fixora TUI  context=%s  namespace=%s  mode=%s  scan_age=%s  health=%d  provider=%s ",
		spinView, firstNonEmpty(m.opts.Context, "current"), ns, scanModeLabel(m.opts, m.deepScan), age, score, provider)
	return titleStyle.Background(lipgloss.Color("236")).Width(m.width).Render(line)
}

func (m tuiModel) sidebar() string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n%s\n", mutedStyle.Render(fmt.Sprintf("findings=%d high=%d med=%d skipped=%d", m.report.Summary.Findings, m.report.Summary.HighSeverity, m.report.Summary.MediumSeverity, m.report.Summary.SkippedChecks)))
	if m.err != nil {
		fmt.Fprintf(&b, "%s\n", lipgloss.NewStyle().Foreground(highColor).Render("scan: "+m.err.Error()))
	}
	if m.filter != "" {
		fmt.Fprintf(&b, "%s\n", mutedStyle.Render("filter: "+m.filter))
	}
	fmt.Fprintf(&b, "%s\n", mutedStyle.Render("mode: "+scanModeLabel(m.opts, m.deepScan)+"  D deep/fast  L logs"))
	if m.message != "" {
		fmt.Fprintf(&b, "%s\n", mutedStyle.Render(m.message))
	}
	return b.String()
}

func (m tuiModel) detailView(width int) string {
	if m.selected.ID == "" {
		return mutedStyle.Render("No incident selected.")
	}
	switch m.tab {
	case 1:
		return m.workloadView(width)
	case 2:
		return m.networkView(width)
	case 3:
		return m.storageView(width)
	case 4:
		return m.securityView(width)
	case 5:
		return m.fixView(width)
	case 6:
		return m.eventsView(width)
	case 7:
		return m.logsView(width)
	case 8:
		return m.graphView(width)
	case 9:
		return m.settingsView(width)
	default:
		return m.incidentView(width)
	}
}

func (m tuiModel) incidentView(width int) string {
	f := m.selected
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", titleStyle.Render(f.ResourceKind+"/"+f.ResourceName))
	fmt.Fprintf(&b, "severity=%s status=%s namespace=%s\n\n", severityText(f.Severity), f.Status, f.Namespace)
	fmt.Fprintf(&b, "%s\n\n", f.Summary)
	if m.aiRunning {
		fmt.Fprintf(&b, "%s\n", titleStyle.Render("AI Root Cause"))
		fmt.Fprintf(&b, "%s\n\n", mutedStyle.Render("Analyzing selected incident..."))
	} else if f.AI != nil {
		fmt.Fprintf(&b, "%s\n", titleStyle.Render("AI Root Cause"))
		fmt.Fprintf(&b, "Summary: %s\n", trim(f.AI.Summary, width-10))
		fmt.Fprintf(&b, "Root cause: %s\n", trim(f.AI.RootCause, width-12))
		fmt.Fprintf(&b, "Fix: %s\n", trim(f.AI.RecommendedFix, width-6))
		for _, warning := range f.AI.Warnings {
			fmt.Fprintf(&b, "- Warning: %s\n", trim(warning, width-12))
		}
		fmt.Fprintln(&b)
	}
	fmt.Fprintf(&b, "%s\n", titleStyle.Render("Evidence"))
	for _, ev := range limitEvidence(f.Evidence, 8) {
		fmt.Fprintf(&b, "- %s: %s\n", ev.Label, trim(ev.Value, width-14))
	}
	if len(f.RecentChanges) > 0 {
		fmt.Fprintf(&b, "\n%s\n", titleStyle.Render("Recent Changes"))
		for _, item := range f.RecentChanges {
			fmt.Fprintf(&b, "- %s\n", trim(item, width-4))
		}
	}
	return b.String()
}

func (m tuiModel) workloadView(width int) string {
	f := m.selected
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", titleStyle.Render("Owner Chain"))
	for _, owner := range f.OwnerChain {
		fmt.Fprintf(&b, "- %s\n", owner)
	}
	fmt.Fprintf(&b, "\n%s\n", titleStyle.Render("GitOps"))
	fmt.Fprintf(&b, "managedBy=%s helm=%s chart=%s\n", firstNonEmpty(f.GitOps.ManagedBy, "-"), firstNonEmpty(f.GitOps.HelmRelease, "-"), firstNonEmpty(f.GitOps.HelmChart, "-"))
	if f.GitOps.TargetAdvice != "" {
		fmt.Fprintf(&b, "%s\n", trim(f.GitOps.TargetAdvice, width-4))
	}
	return b.String()
}

func (m tuiModel) networkView(width int) string {
	f := m.selected
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", titleStyle.Render("Network Signals"))
	found := false
	for _, ev := range f.Evidence {
		if containsAny(ev.Label+" "+ev.Value, "service", "endpoint", "ingress", "route", "gateway", "dns", "network", "connection") {
			found = true
			fmt.Fprintf(&b, "- %s: %s\n", ev.Label, trim(ev.Value, width-14))
		}
	}
	if !found {
		fmt.Fprintln(&b, mutedStyle.Render("No network-specific evidence attached to this incident."))
	}
	return b.String()
}

func (m tuiModel) storageView(width int) string {
	f := m.selected
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", titleStyle.Render("Storage Signals"))
	found := false
	for _, ev := range f.Evidence {
		if containsAny(ev.Label+" "+ev.Value, "pvc", "pv", "volume", "storage", "mount", "disk") {
			found = true
			fmt.Fprintf(&b, "- %s: %s\n", ev.Label, trim(ev.Value, width-14))
		}
	}
	if !found {
		fmt.Fprintln(&b, mutedStyle.Render("No storage-specific evidence attached to this incident."))
	}
	return b.String()
}

func (m tuiModel) securityView(width int) string {
	f := m.selected
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", titleStyle.Render("Security / Policy Signals"))
	found := false
	for _, ev := range f.Evidence {
		if containsAny(ev.Label+" "+ev.Value, "rbac", "admission", "webhook", "policy", "security", "serviceaccount", "privilege") {
			found = true
			fmt.Fprintf(&b, "- %s: %s\n", ev.Label, trim(ev.Value, width-14))
		}
	}
	for _, rec := range f.Recommendations {
		if containsAny(rec.Title+" "+rec.Description, "rbac", "admission", "webhook", "policy", "security", "serviceaccount", "privilege") {
			found = true
			fmt.Fprintf(&b, "- %s: %s\n", rec.Title, trim(rec.Description, width-14))
		}
	}
	if !found {
		fmt.Fprintln(&b, mutedStyle.Render("No security or policy-specific evidence attached to this incident."))
	}
	return b.String()
}

func (m tuiModel) fixView(width int) string {
	p := m.plan
	var md strings.Builder
	fmt.Fprintf(&md, "# Fix Plan\n\n")
	fmt.Fprintf(&md, "- Strategy: `%s`\n", p.Strategy)
	fmt.Fprintf(&md, "- Confidence: `%d`\n", p.Confidence)
	fmt.Fprintf(&md, "- Risk: `%s`\n", p.Risk)
	fmt.Fprintf(&md, "- Apply eligible: `%t`\n\n", p.ApplyEligible)
	if p.ApplyEligible {
		fmt.Fprintf(&md, "> Press `i` for AI RCA, `s` to verify in a shadow clone, `a` to apply, `e` to edit/apply, or `p` to push a verified PR/MR. Server dry-run is `%t`.\n\n", m.opts.ApplyDryRun)
	} else {
		fmt.Fprintf(&md, "> Live apply is disabled until the plan passes Fixora's production gates.\n\n")
	}
	if m.shadowing {
		fmt.Fprintf(&md, "## Shadow Verification\n\n%s\n\n", "Running shadow clone verification...")
	} else if m.shadow.Resource != "" {
		fmt.Fprintf(&md, "## Shadow Verification\n\n")
		fmt.Fprintf(&md, "- Verified: `%t`\n", m.shadow.Verified)
		fmt.Fprintf(&md, "- Parity: `%d%%`\n", m.shadow.Parity)
		if m.shadow.CloneName != "" {
			fmt.Fprintf(&md, "- Clone: `%s`\n", m.shadow.CloneName)
		}
		if m.shadow.NetworkPolicyName != "" {
			fmt.Fprintf(&md, "- NetworkPolicy: `%s`\n", m.shadow.NetworkPolicyName)
		}
		for _, attempt := range m.shadow.Attempts {
			fmt.Fprintf(&md, "- Attempt %d: phase `%s`, ready `%t`, restarts `%d`", attempt.Number, attempt.Phase, attempt.Ready, attempt.Restarts)
			if attempt.ExitReason != "" {
				fmt.Fprintf(&md, ", reason `%s`", attempt.ExitReason)
			}
			if attempt.Message != "" {
				fmt.Fprintf(&md, ", %s", attempt.Message)
			}
			fmt.Fprintf(&md, "\n")
		}
		for _, warning := range m.shadow.Warnings {
			fmt.Fprintf(&md, "- Warning: %s\n", warning)
		}
		if m.shadow.Verified {
			fmt.Fprintf(&md, "\n> Shadow is verified. Press `a` for direct apply or `p` to push/open a GitHub PR or GitLab MR.\n")
		}
		fmt.Fprintf(&md, "\n")
	}
	if m.selected.AI != nil {
		fmt.Fprintf(&md, "## AI Root Cause\n\n")
		fmt.Fprintf(&md, "- Summary: %s\n", m.selected.AI.Summary)
		fmt.Fprintf(&md, "- Root cause: %s\n", m.selected.AI.RootCause)
		fmt.Fprintf(&md, "- Recommended fix: %s\n", m.selected.AI.RecommendedFix)
		for _, command := range m.selected.AI.Commands {
			fmt.Fprintf(&md, "- Command: `%s`\n", command)
		}
		fmt.Fprintf(&md, "\n")
	}
	if len(p.BlockedReasons) > 0 {
		fmt.Fprintf(&md, "## Blocked\n")
		for _, reason := range p.BlockedReasons {
			fmt.Fprintf(&md, "- %s\n", reason)
		}
	}
	if len(p.Verification) > 0 {
		fmt.Fprintf(&md, "\n## Verify\n")
		for _, cmd := range p.Verification {
			fmt.Fprintf(&md, "- `%s`\n", cmd)
		}
	}
	if p.RollbackCommand != "" {
		fmt.Fprintf(&md, "\n## Rollback\n`%s`\n", p.RollbackCommand)
	}
	if m.selected.ID != "" {
		fmt.Fprintf(&md, "\n## Operator Runbook\n\n%s", ops.BuildRunbook(m.selected, p))
	}
	out := md.String()
	if m.renderer != nil {
		rendered, err := m.renderer.Render(out)
		if err == nil {
			return rendered
		}
	}
	return out
}

func (m tuiModel) footer() string {
	hints := " j/k move  tab views  r refresh  D deep  L logs  C cluster  i ai  f fix  s shadow  p push  ? help  q quit "
	return mutedStyle.Background(lipgloss.Color("236")).Width(m.width).Render(hints)
}

func (m tuiModel) eventsView(width int) string {
	f := m.selected
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", titleStyle.Render("Events / Evidence"))
	for _, ev := range f.Evidence {
		fmt.Fprintf(&b, "- %s: %s\n", ev.Label, trim(ev.Value, width-14))
	}
	if len(f.Evidence) == 0 {
		fmt.Fprintln(&b, mutedStyle.Render("No event evidence attached to this incident."))
	}
	return b.String()
}

func (m tuiModel) logsView(width int) string {
	f := m.selected
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", titleStyle.Render("Logs / Timeline"))
	for _, log := range f.Logs {
		fmt.Fprintf(&b, "[%s]\n%s\n", log.Source, highlightLog(trim(log.Text, width*4)))
	}
	if len(f.Logs) == 0 {
		fmt.Fprintln(&b, mutedStyle.Render("No logs collected. Run with --include-logs for bounded log snippets."))
	}
	return b.String()
}

func (m tuiModel) graphView(width int) string {
	if m.selected.ID == "" {
		return mutedStyle.Render("No incident selected.")
	}
	return m.graphList.View()
}

func (m tuiModel) settingsView(width int) string {
	configPath, _ := config.Path()
	cfg, err := config.Load()

	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", titleStyle.Render("Settings & Configuration"))
	fmt.Fprintf(&b, "Path: %s\n\n", configPath)

	if err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(&b, "%s\n", lipgloss.NewStyle().Foreground(highColor).Render("Error loading config: "+err.Error()))
		return b.String()
	}

	fmt.Fprintf(&b, "- AIProvider: `%s`\n", cfg.AIProvider)
	fmt.Fprintf(&b, "- LogTail: `%d`\n", cfg.LogTail)
	fmt.Fprintf(&b, "- CacheEnabled: `%t`\n", cfg.CacheEnabled)
	fmt.Fprintf(&b, "- Redact: `%t`\n", cfg.Redact)
	fmt.Fprintf(&b, "\n> Press `e` to edit configuration file.\n")

	return b.String()
}

func (m tuiModel) updateInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.palette = false
		m.command = false
	case "backspace":
		if m.palette && len(m.filter) > 0 {
			m.filter = m.filter[:len(m.filter)-1]
			m.updateRows()
		}
		if m.command && len(m.commandIn) > 0 {
			m.commandIn = m.commandIn[:len(m.commandIn)-1]
		}
	case "enter":
		if m.palette {
			m.palette = false
			return m, nil
		}
		if m.command {
			cmd := strings.ToLower(strings.TrimSpace(m.commandIn))
			m.command = false
			switch cmd {
			case "q", "quit", "exit":
				return m, tea.Quit
			case "r", "refresh", "rescan":
				return m, m.scanCmd()
			case "deep":
				m.deepScan = true
				m.opts.Filters = nil
				m.a = newTUIAnalyzer(m.k, m.opts)
				m.scanning = true
				m.message = "deep scan enabled: all analyzers"
				return m, m.scanCmd()
			case "fast":
				m.deepScan = false
				m.opts.Filters = fastTUIFilters()
				m.a = newTUIAnalyzer(m.k, m.opts)
				m.scanning = true
				m.message = "fast scan enabled: failing pods only"
				return m, m.scanCmd()
			case "logs":
				m.opts.IncludeLogs = !m.opts.IncludeLogs
				m.a = newTUIAnalyzer(m.k, m.opts)
				m.scanning = true
				if m.opts.IncludeLogs {
					m.message = "log collection enabled for next scans"
				} else {
					m.message = "log collection disabled"
				}
				return m, m.scanCmd()
			case "incident", "incidents":
				m.tab = 0
			case "workload", "workloads":
				m.tab = 1
			case "network":
				m.tab = 2
			case "storage":
				m.tab = 3
			case "security", "policy":
				m.tab = 4
			case "fix", "fixes", "plan", "plans":
				m.tab = 5
			case "event", "events":
				m.tab = 6
			case "log", "logview":
				m.tab = 7
			case "graph":
				m.tab = 8
			case "settings", "config":
				m.tab = 9
			}
		}
	default:
		if len(msg.String()) == 1 {
			if m.palette {
				m.filter += msg.String()
				m.updateRows()
			}
			if m.command {
				m.commandIn += msg.String()
			}
		}
	}
	return m, nil
}

func (m tuiModel) scanCmd() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, m.opts.ScanTimeout)
		defer cancel()
		report := m.a.ScanReport(ctx)
		return scanMsg{report: report}
	}
}

func (m *tuiModel) updateRows() {
	findings := append([]analyzer.Finding{}, m.report.Findings...)
	sort.Slice(findings, func(i, j int) bool {
		return severityRank(findings[i].Severity) > severityRank(findings[j].Severity)
	})
	rows := []table.Row{}
	filter := strings.ToLower(strings.TrimSpace(m.filter))
	for _, f := range findings {
		if filter != "" && !strings.Contains(strings.ToLower(f.Namespace+" "+f.ResourceKind+" "+f.ResourceName+" "+f.Status+" "+f.Summary), filter) {
			continue
		}
		sevStr := severityText(strings.ToUpper(f.Severity))
		rows = append(rows, table.Row{sevStr, f.Namespace, f.ResourceKind + "/" + f.ResourceName, f.Status})
	}
	m.table.SetRows(rows)
}

func (m *tuiModel) syncSelected() {
	previousID := m.selected.ID
	row := m.table.SelectedRow()
	if len(row) < 4 {
		m.selected = analyzer.Finding{}
		m.plan = fix.Plan{}
		m.shadow = shadow.Result{}
		m.shadowing = false
		return
	}
	resource := row[2]
	for _, f := range m.report.Findings {
		if f.ResourceKind+"/"+f.ResourceName == resource && f.Namespace == row[1] && f.Status == row[3] {
			m.selected = f
			if previousID != "" && previousID != f.ID {
				m.shadow = shadow.Result{}
				m.shadowing = false
			}
			m.plan = fix.BuildPlan(f)
			if m.graphList.Title != "" {
				m.graphList.SetItems(graphItemsForFinding(f))
			}
			return
		}
	}
}

func tick(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func tableStyles() table.Styles {
	s := table.DefaultStyles()
	s.Header = s.Header.Bold(true).Foreground(lipgloss.Color("81")).BorderStyle(lipgloss.NormalBorder()).BorderBottom(true)
	s.Selected = s.Selected.Foreground(lipgloss.Color("229")).Background(lipgloss.Color("57")).Bold(false)
	return s
}

func severityText(severity string) string {
	style := lipgloss.NewStyle()
	switch strings.ToLower(severity) {
	case "critical", "high":
		style = style.Foreground(highColor).Bold(true)
	case "medium":
		style = style.Foreground(medColor).Bold(true)
	default:
		style = style.Foreground(okColor)
	}
	return style.Render(severity)
}

func severityRank(value string) int {
	switch strings.ToLower(value) {
	case "critical":
		return 4
	case "high":
		return 3
	case "medium":
		return 2
	case "low":
		return 1
	default:
		return 0
	}
}

func limitEvidence(items []analyzer.Evidence, n int) []analyzer.Evidence {
	if len(items) <= n {
		return items
	}
	return items[:n]
}

func highlightLog(value string) string {
	lines := strings.Split(value, "\n")
	style := lipgloss.NewStyle().Foreground(highColor).Bold(true)
	for i, line := range lines {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "panic") || strings.Contains(lower, "exception") || strings.Contains(lower, "error") || strings.Contains(lower, "fatal") {
			lines[i] = style.Render(line)
		}
	}
	return strings.Join(lines, "\n")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func containsAny(value string, terms ...string) bool {
	value = strings.ToLower(value)
	for _, term := range terms {
		if strings.Contains(value, term) {
			return true
		}
	}
	return false
}

func helpText() string {
	return strings.Join([]string{
		"Fixora TUI keys",
		"j/k or arrows: move selected incident",
		"1-9: switch production views, tab/shift+tab: cycle views",
		"r: rescan, D: toggle fast/deep analyzers, L: toggle logs, n: namespace picker, C: toggle cluster-wide",
		"/: filter incidents, colon: command palette",
		"i: AI root cause, f: fix plan, s: shadow verify, a: apply, e: edit/apply",
		"p: push/open verified GitHub PR or GitLab MR, g: dependency graph",
		"?: close help, q: quit",
	}, "\n")
}

func (m tuiModel) applyPatchCmd(edit bool) (tea.Model, tea.Cmd) {
	if m.selected.ID == "" {
		m.message = "no incident selected"
		return m, nil
	}
	if m.tab != 5 {
		m.tab = 5
	}
	if !m.plan.ApplyEligible {
		m.message = "apply blocked: plan is not apply eligible"
		return m, nil
	}
	if strings.TrimSpace(m.plan.PatchTemplate) == "" {
		m.message = "apply blocked: patch template is empty"
		return m, nil
	}
	cmd := gatedApplyCommand{
		ctx:       m.ctx,
		k:         m.k,
		patch:     m.plan.PatchTemplate,
		edit:      edit,
		dryRun:    m.opts.ApplyDryRun,
		timeout:   m.opts.ScanTimeout,
		resource:  m.plan.Resource,
		namespace: m.plan.Namespace,
	}
	return m, tea.Exec(&cmd, func(err error) tea.Msg {
		if err != nil {
			return applyMsg{err: err}
		}
		if edit {
			return applyMsg{message: "edited patch applied"}
		}
		return applyMsg{message: "patch applied"}
	})
}

func (m tuiModel) shadowVerifyCmd() (tea.Model, tea.Cmd) {
	if m.selected.ID == "" {
		m.message = "no incident selected"
		return m, nil
	}
	if m.tab != 5 {
		m.tab = 5
	}
	if !m.plan.ApplyEligible {
		m.message = "shadow blocked: plan is not apply eligible"
		return m, nil
	}
	if strings.TrimSpace(m.plan.PatchTemplate) == "" {
		m.message = "shadow blocked: patch template is empty"
		return m, nil
	}
	m.shadowing = true
	cmd := shadowVerifyCommand{
		ctx:     m.ctx,
		context: m.opts.Context,
		req: shadow.Request{
			Namespace: m.selected.Namespace,
			Resource:  m.plan.Resource,
			Patch:     m.plan.PatchTemplate,
			Finding:   m.selected,
			Plan:      m.plan,
			Timeout:   firstDuration(m.opts.ShadowTimeout, 10*time.Minute),
			Retries:   m.opts.ShadowRetries,
			Keep:      m.opts.KeepShadow,
			Egress:    firstNonEmpty(m.opts.ShadowEgress, "allow"),
			Delivery:  shadow.DeliveryPatch,
			Redact:    m.opts.Redact,
		},
		stdin:  os.Stdin,
		stdout: os.Stdout,
	}
	return m, tea.Exec(&cmd, func(err error) tea.Msg {
		if err != nil {
			return shadowMsg{err: err}
		}
		return shadowMsg{result: cmd.result}
	})
}

func (m tuiModel) aiAnalyzeCmd() (tea.Model, tea.Cmd) {
	if m.selected.ID == "" {
		m.message = "no incident selected"
		return m, nil
	}
	m.aiRunning = true
	finding := m.selected
	if !m.opts.Redact && !m.opts.UnsafeAI {
		m.aiRunning = false
		m.message = "AI blocked: enable redaction or pass --unsafe-ai-no-redact"
		return m, nil
	}
	if m.opts.Redact {
		finding = analyzer.RedactFindingForAI(finding)
	}
	timeout := firstDuration(m.opts.ScanTimeout, 90*time.Second)
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, timeout)
		defer cancel()
		client, err := ai.NewFromEnv()
		if err != nil {
			return aiMsg{err: err}
		}
		result, err := client.Explain(ctx, finding)
		if err != nil {
			return aiMsg{err: err}
		}
		return aiMsg{result: result}
	}
}

func (m tuiModel) pushVerifiedCmd() (tea.Model, tea.Cmd) {
	if m.selected.ID == "" {
		m.message = "no incident selected"
		return m, nil
	}
	if !m.shadow.Verified {
		m.message = "push blocked: run successful shadow verification first"
		return m, nil
	}
	if strings.TrimSpace(m.opts.RepoPath) == "" {
		m.message = "push blocked: start TUI with --repo"
		return m, nil
	}
	cmd := verifiedDeliveryCommand{
		ctx:      m.ctx,
		repoPath: m.opts.RepoPath,
		branch:   firstNonEmpty(m.opts.Branch, defaultTUIBranch(m.selected)),
		base:     m.opts.PRBase,
		title:    firstNonEmpty(m.opts.PRTitle, "fixora: verified remediation for "+m.selected.ResourceKind+"/"+m.selected.ResourceName),
		finding:  m.selected,
		plan:     m.plan,
		shadow:   m.shadow,
		stdin:    os.Stdin,
		stdout:   os.Stdout,
		stderr:   os.Stderr,
	}
	return m, tea.Exec(&cmd, func(err error) tea.Msg {
		if err != nil {
			return deliveryMsg{err: err}
		}
		return deliveryMsg{message: cmd.message}
	})
}

type nsListMsg []string

func fetchNamespacesCmd(ctx context.Context, k kube.Reader) tea.Cmd {
	return func() tea.Msg {
		nsCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		out, err := k.Run(nsCtx, "get", "ns", "-o", "name")
		if err != nil {
			return nil
		}
		lines := strings.Split(string(out), "\n")
		var nss []string
		nss = append(nss, "<All Namespaces>")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line != "" {
				nss = append(nss, strings.TrimPrefix(line, "namespace/"))
			}
		}
		return nsListMsg(nss)
	}
}

type nsItem string

func (i nsItem) Title() string       { return string(i) }
func (i nsItem) Description() string { return "" }
func (i nsItem) FilterValue() string { return string(i) }

type graphNodeItem struct {
	kind   string
	name   string
	status string
}

func (i graphNodeItem) Title() string       { return i.kind + "/" + i.name }
func (i graphNodeItem) Description() string { return i.status }
func (i graphNodeItem) FilterValue() string { return i.kind + "/" + i.name }

func graphItemsForFinding(f analyzer.Finding) []list.Item {
	items := []list.Item{}
	for _, owner := range f.OwnerChain {
		kind, name := splitGraphRef(owner)
		items = append(items, graphNodeItem{kind: kind, name: name, status: "owner"})
	}
	items = append(items, graphNodeItem{kind: f.ResourceKind, name: f.ResourceName, status: f.Status})
	if f.PodName != "" {
		items = append(items, graphNodeItem{kind: "Pod", name: f.PodName, status: f.Status})
	}
	if f.GitOps.ManagedBy != "" {
		items = append(items, graphNodeItem{kind: "GitOps", name: f.GitOps.ManagedBy, status: f.GitOps.TargetAdvice})
	}
	return items
}

func splitGraphRef(value string) (string, string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "Owner", "-"
	}
	if parts := strings.SplitN(value, "/", 2); len(parts) == 2 {
		return parts[0], parts[1]
	}
	return "Owner", value
}

type gatedApplyCommand struct {
	ctx       context.Context
	k         kube.Reader
	patch     string
	edit      bool
	dryRun    bool
	timeout   time.Duration
	resource  string
	namespace string
	stdin     io.Reader
	stdout    io.Writer
	stderr    io.Writer
}

type shadowVerifyCommand struct {
	ctx     context.Context
	context string
	req     shadow.Request
	result  shadow.Result
	stdin   io.Reader
	stdout  io.Writer
	stderr  io.Writer
}

type verifiedDeliveryCommand struct {
	ctx      context.Context
	repoPath string
	branch   string
	base     string
	title    string
	finding  analyzer.Finding
	plan     fix.Plan
	shadow   shadow.Result
	message  string
	stdin    io.Reader
	stdout   io.Writer
	stderr   io.Writer
}

func (c *verifiedDeliveryCommand) Run() error {
	stdout := firstWriter(c.stdout, os.Stdout)
	if !c.shadow.Verified {
		return fmt.Errorf("shadow verification has not passed")
	}
	preview, err := repo.PreviewSourcePatch(c.repoPath, "", c.finding, c.plan)
	if err != nil {
		return err
	}
	summary := repo.SummarizePreview(c.ctx, c.repoPath, c.branch, preview)
	if !ConfirmVerifiedDelivery(summary, c.stdin, stdout) {
		c.message = "verified PR/MR delivery cancelled"
		fmt.Fprintln(stdout, c.message)
		return nil
	}
	sourcePatch, err := repo.WriteSourcePatch(c.repoPath, "", c.finding, c.plan)
	if err != nil {
		return err
	}
	if err := repo.PrepareBranchFiles(c.ctx, c.repoPath, c.branch, true, "fixora: verified remediation for "+c.finding.ResourceKind+"/"+c.finding.ResourceName, []string{sourcePatch.Path}); err != nil {
		return err
	}
	pr, err := repo.OpenPullRequest(c.ctx, c.repoPath, c.branch, c.base, c.title, tuiPRBody(c.shadow, sourcePatch), true)
	if err != nil {
		return err
	}
	if pr.URL != "" {
		c.message = "opened " + firstNonEmpty(pr.Platform, "review") + " review: " + pr.URL
		fmt.Fprintln(stdout, c.message)
		return nil
	}
	c.message = "pushed verified branch " + pr.Branch
	if len(pr.Warnings) > 0 {
		c.message += ": " + strings.Join(pr.Warnings, "; ")
	}
	fmt.Fprintln(stdout, c.message)
	return nil
}

func (c *verifiedDeliveryCommand) SetStdin(r io.Reader) {
	c.stdin = r
}

func (c *verifiedDeliveryCommand) SetStdout(w io.Writer) {
	c.stdout = w
}

func (c *verifiedDeliveryCommand) SetStderr(w io.Writer) {
	c.stderr = w
}

func (c *shadowVerifyCommand) Run() error {
	stdout := firstWriter(c.stdout, os.Stdout)
	stderr := firstWriter(c.stderr, os.Stderr)
	if c.req.Timeout <= 0 {
		c.req.Timeout = 10 * time.Minute
	}
	diff := shadow.PatchDiff(c.req.Resource, c.req.Patch)
	if !ConfirmShadowDeploy(diff, c.stdin, stdout) {
		fmt.Fprintln(stdout, "shadow verification cancelled")
		return nil
	}
	client, err := kube.NewRequiredTypedClient(c.context, "TUI shadow verification")
	if err != nil {
		return err
	}
	result, err := shadow.Run(c.ctx, client, c.req)
	if err != nil {
		return err
	}
	c.result = result
	if result.Verified {
		fmt.Fprintf(stdout, "Fix Verified - Parity %d%%\n", result.Parity)
	} else {
		fmt.Fprintln(stderr, "shadow verification did not pass")
	}
	return nil
}

func (c *shadowVerifyCommand) SetStdin(r io.Reader) {
	c.stdin = r
}

func (c *shadowVerifyCommand) SetStdout(w io.Writer) {
	c.stdout = w
}

func (c *shadowVerifyCommand) SetStderr(w io.Writer) {
	c.stderr = w
}

func (c *gatedApplyCommand) Run() error {
	if c.timeout <= 0 {
		c.timeout = 90 * time.Second
	}
	stdout := firstWriter(c.stdout, os.Stdout)
	stderr := firstWriter(c.stderr, os.Stderr)
	patch := c.patch
	if c.edit {
		edited, err := editPatch(c.patch, c.stdin, stdout, stderr)
		if err != nil {
			return err
		}
		patch = edited
	}
	if strings.TrimSpace(patch) == "" {
		return fmt.Errorf("edited patch is empty")
	}
	fmt.Fprintf(stdout, "\nFixora gated apply for %s in namespace %s\n", firstNonEmpty(c.resource, "resource"), firstNonEmpty(c.namespace, "default"))
	if !ConfirmApply(c.ctx, c.k, patch, c.stdin, stdout) {
		fmt.Fprintln(stdout, "apply cancelled")
		return nil
	}
	file, err := os.CreateTemp("", "fixora-tui-*.yaml")
	if err != nil {
		return err
	}
	path := file.Name()
	defer os.Remove(path)
	if _, err := file.WriteString(patch); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(c.ctx, c.timeout)
	defer cancel()
	if c.dryRun {
		if out, err := c.k.Run(ctx, "apply", "--dry-run=server", "-f", path); err != nil {
			if len(out) > 0 {
				fmt.Fprintln(stderr, strings.TrimSpace(string(out)))
			}
			return fmt.Errorf("server dry-run failed: %w", err)
		}
		fmt.Fprintln(stdout, "server dry-run passed")
	}
	out, err := c.k.Run(ctx, "apply", "-f", path)
	if len(out) > 0 {
		fmt.Fprintln(stdout, strings.TrimSpace(string(out)))
	}
	if err != nil {
		return err
	}
	return nil
}

func (c *gatedApplyCommand) SetStdin(r io.Reader) {
	c.stdin = r
}

func (c *gatedApplyCommand) SetStdout(w io.Writer) {
	c.stdout = w
}

func (c *gatedApplyCommand) SetStderr(w io.Writer) {
	c.stderr = w
}

func editPatch(patch string, stdin io.Reader, stdout, stderr io.Writer) (string, error) {
	file, err := os.CreateTemp("", "fixora-edit-*.yaml")
	if err != nil {
		return "", err
	}
	path := file.Name()
	defer os.Remove(path)
	if _, err := file.WriteString(patch); err != nil {
		file.Close()
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	editor := strings.Fields(firstNonEmpty(os.Getenv("EDITOR"), "vi"))
	cmd := exec.Command(editor[0], append(editor[1:], path)...)
	cmd.Stdin = firstReader(stdin, os.Stdin)
	cmd.Stdout = firstWriter(stdout, os.Stdout)
	cmd.Stderr = firstWriter(stderr, os.Stderr)
	if err := cmd.Run(); err != nil {
		return "", err
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(content), nil
}

func firstWriter(primary io.Writer, fallback io.Writer) io.Writer {
	if primary != nil {
		return primary
	}
	return fallback
}

func firstReader(primary io.Reader, fallback io.Reader) io.Reader {
	if primary != nil {
		return primary
	}
	return fallback
}

func firstDuration(values ...time.Duration) time.Duration {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func defaultTUIBranch(f analyzer.Finding) string {
	kind := strings.ToLower(strings.ReplaceAll(f.ResourceKind, "/", "-"))
	name := strings.ToLower(strings.ReplaceAll(f.ResourceName, "/", "-"))
	return "fixora/verified-" + kind + "-" + name
}

func tuiPRBody(result shadow.Result, sourcePatch repo.SourcePatch) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Fixora TUI verified this remediation in a shadow clone before delivery.\n\n")
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
