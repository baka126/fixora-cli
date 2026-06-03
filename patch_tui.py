import re
with open('internal/termui/tui.go', 'r') as f:
    content = f.read()

# 1. Add imports
content = re.sub(
    r'("github.com/charmbracelet/bubbles/table")',
    r'"github.com/charmbracelet/bubbles/list"\n\t\1',
    content
)

# 2. Add fields to tuiModel
content = re.sub(
    r'(scanning\s+bool)',
    r'\1\n\tzoomed      bool\n\tnsList      list.Model\n\tswitchingNS bool\n\tgraphList   list.Model',
    content
)

# 3. Add to RunTUI
# right before m := tuiModel{...}
content = re.sub(
    r'(m := tuiModel{ctx: ctx, k: k, a: a, opts: opts, table: t, renderer: renderer, spinner: s, scanning: true})',
    r'''nl := list.New([]list.Item{}, list.NewDefaultDelegate(), 0, 0)
	nl.Title = "Select Namespace"
	nl.SetShowStatusBar(false)
	nl.SetFilteringEnabled(true)
	
	gl := list.New([]list.Item{}, list.NewDefaultDelegate(), 0, 0)
	gl.Title = "Graph Pivot Nodes"
	gl.SetShowStatusBar(false)
	gl.SetFilteringEnabled(false)

	m := tuiModel{ctx: ctx, k: k, a: a, opts: opts, table: t, renderer: renderer, spinner: s, scanning: true, nsList: nl, graphList: gl}''',
    content
)

# 4. Add custom types & commands
content += '''

type nsListMsg []string

func fetchNamespacesCmd() tea.Cmd {
	return func() tea.Msg {
		out, err := exec.Command("kubectl", "get", "ns", "-o", "name").Output()
		if err != nil {
			return nil
		}
		lines := strings.Split(string(out), "\\n")
		var nss []string
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

type graphNodeItem graph.Node
func (i graphNodeItem) Title() string       { return i.Kind + "/" + i.Name }
func (i graphNodeItem) Description() string { return i.Status }
func (i graphNodeItem) FilterValue() string { return i.Kind + "/" + i.Name }

type editFixCommand struct {
	patch string
}
func (c editFixCommand) Run() error {
	f, err := os.CreateTemp("", "fixora-*.yaml")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	f.Write([]byte(c.patch))
	f.Close()

	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vim"
	}
	cmd := exec.Command(editor, f.Name())
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return err
	}

	newPatch, err := os.ReadFile(f.Name())
	if err != nil {
		return err
	}
	if ConfirmApply(string(newPatch)) {
		applyCmd := exec.Command("kubectl", "apply", "-f", "-")
		applyCmd.Stdin = strings.NewReader(string(newPatch))
		applyCmd.Stdout = os.Stdout
		applyCmd.Stderr = os.Stderr
		return applyCmd.Run()
	}
	return nil
}
func (c editFixCommand) SetStdin(r io.Reader)  {}
func (c editFixCommand) SetStdout(w io.Writer) {}
func (c editFixCommand) SetStderr(w io.Writer) {}
'''

# 5. WindowSizeMsg
content = re.sub(
    r'(m\.table\.SetHeight\(maxInt\(8, msg\.Height-12\)\))',
    r'\1\n\t\tm.nsList.SetSize(msg.Width, msg.Height)\n\t\tm.graphList.SetSize(maxInt(50, msg.Width/2-8), msg.Height-12)',
    content
)

# 6. View function
content = re.sub(
    r'(func \(m tuiModel\) View\(\) string {\n\tif m\.width == 0 {)',
    r'func (m tuiModel) View() string {\n\tif m.switchingNS {\n\t\treturn m.nsList.View()\n\t}\n\tif m.zoomed {\n\t\treturn m.detailView(m.width)\n\t}\n\tif m.width == 0 {',
    content
)

# 7. Update graphList in graphView or in View? Actually wait, the instruction says "If m.tab == 8 (Graph), populate a list.Model of graph nodes"
# I'll modify graphView to return m.graphList.View()
content = re.sub(
    r'(func \(m tuiModel\) graphView\(width int\) string {\n\tif m\.selected\.ID == "" {\n\t\treturn mutedStyle\.Render\("No incident selected\."\)\n\t}\n\tk, ok := m\.k\.\(kube\.Kubectl\)\n\tif !ok {\n\t\treturn mutedStyle\.Render\("Dependency Graph requires a standard kubectl client\."\)\n\t}\n\tg := graph\.Build\(m\.ctx, k, m\.selected\)\n\treturn graph\.Text\(g\))',
    r'''func (m tuiModel) graphView(width int) string {
	if m.selected.ID == "" {
		return mutedStyle.Render("No incident selected.")
	}
	return m.graphList.View()''',
    content
)

# 8. Update syncSelected to populate graphList
content = re.sub(
    r'(m\.plan = fix\.BuildPlan\(f\)\n\t\t\treturn)',
    r'''m.plan = fix.BuildPlan(f)
			if k, ok := m.k.(kube.Kubectl); ok {
				g := graph.Build(m.ctx, k, f)
				var items []list.Item
				for _, n := range g.Nodes {
					items = append(items, graphNodeItem(n))
				}
				m.graphList.SetItems(items)
			}
			return''',
    content
)

# 9. KeyMsg handling in Update
# We need to insert logic at the top of KeyMsg:
#	case tea.KeyMsg:
#		if m.switchingNS { ... }
key_msg_replacement = '''	case tea.KeyMsg:
		if m.switchingNS {
			switch msg.String() {
			case "esc":
				m.switchingNS = false
			case "enter":
				if i, ok := m.nsList.SelectedItem().(nsItem); ok {
					m.opts.Namespace = string(i)
					m.opts.AllNS = false
					m.switchingNS = false
					m.a = analyzer.New(m.k, analyzer.Options{
						Namespace:   m.opts.Namespace,
						AllNS:       false,
						IncludeLogs: m.opts.IncludeLogs,
						Redact:      m.opts.Redact,
						Filters:     m.opts.Filters,
					})
					return m, m.scanCmd()
				}
			}
			var cmd tea.Cmd
			m.nsList, cmd = m.nsList.Update(msg)
			return m, cmd
		}
		if m.palette || m.command {'''
content = content.replace('\tcase tea.KeyMsg:\n\t\tif m.palette || m.command {', key_msg_replacement)

# 10. Key cases for 'z', 'e', 'l', 'n', 'enter'
cases_replacement = '''		case "1", "2", "3", "4", "5", "6", "7", "8", "9":
			m.tab = int(msg.String()[0] - '1')
			if m.tab >= len(tuiTabs) {
				m.tab = len(tuiTabs) - 1
			}
		case "z":
			m.zoomed = !m.zoomed
		case "n":
			m.switchingNS = true
			return m, fetchNamespacesCmd()
		case "e":
			if m.tab == 5 {
				return m, tea.Exec(editFixCommand{patch: m.plan.PatchTemplate}, func(err error) tea.Msg {
					return nil
				})
			}
		case "l":
			if m.selected.ID != "" {
				ns := m.selected.Namespace
				if ns == "" {
					ns = "default"
				}
				name := m.selected.ResourceKind + "/" + m.selected.ResourceName
				c := exec.Command("kubectl", "logs", "-f", name, "-n", ns)
				return m, tea.ExecProcess(c, func(err error) tea.Msg {
					return nil
				})
			}
		case "enter":
			if m.tab == 8 {
				if i, ok := m.graphList.SelectedItem().(graphNodeItem); ok {
					m.opts.Filters = []string{i.Kind + "/" + i.Name}
					m.a = analyzer.New(m.k, analyzer.Options{
						Namespace:   m.opts.Namespace,
						AllNS:       m.opts.AllNS,
						IncludeLogs: m.opts.IncludeLogs,
						Redact:      m.opts.Redact,
						Filters:     m.opts.Filters,
					})
					return m, m.scanCmd()
				}
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
		case "r":'''

content = re.sub(
    r'(\t\tcase "1", "2", "3", "4", "5", "6", "7", "8", "9":\n\t\t\tm\.tab = int\(msg\.String\(\)\[0\] - \'1\'\)\n\t\t\tif m\.tab >= len\(tuiTabs\) {\n\t\t\t\tm\.tab = len\(tuiTabs\) - 1\n\t\t\t}\n\t\tcase "j", "down":\n\t\t\tm\.table\.MoveDown\(1\)\n\t\t\tm\.syncSelected\(\)\n\t\tcase "k", "up":\n\t\t\tm\.table\.MoveUp\(1\)\n\t\t\tm\.syncSelected\(\)\n\t\tcase "r":)',
    cases_replacement,
    content
)

# 11. Handle nsListMsg
msg_cases_replacement = '''	case nsListMsg:
		var items []list.Item
		for _, ns := range msg {
			items = append(items, nsItem(ns))
		}
		m.nsList.SetItems(items)
		return m, nil
	case tickMsg:'''
content = content.replace('\tcase tickMsg:', msg_cases_replacement)

with open('internal/termui/tui.go', 'w') as f:
    f.write(content)
